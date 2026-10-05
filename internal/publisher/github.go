package publisher

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yuki-nemurenai/magic-releaser/internal/repository"
)

// githubPublisher talks to the GitHub Releases API.
//
// A release is created or updated depending on whether the tag already has
// one, which makes rerunning the tool after a partial failure safe.
type githubPublisher struct {
	client *Client
}

func (p *githubPublisher) Provider() repository.Provider { return repository.ProviderGitHub }

type githubRelease struct {
	ID          int        `json:"id"`
	HTMLURL     string     `json:"html_url"`
	Name        string     `json:"name"`
	Body        string     `json:"body"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	TagName     string     `json:"tag_name"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

type githubReleaseRequest struct {
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish,omitempty"`
	Name            string `json:"name"`
	Body            string `json:"body"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
}

func (p *githubPublisher) Publish(ctx context.Context, request Request) (Result, error) {
	slug := request.Repository.Slug
	base := fmt.Sprintf("/repos/%s/releases", slug)
	byTag := base + "/tags/" + url.PathEscape(request.TagName)

	payload := githubReleaseRequest{
		TagName:         request.TagName,
		TargetCommitish: request.Target,
		Name:            releaseName(request),
		Body:            request.Notes,
		Draft:           request.Draft,
		Prerelease:      request.Prerelease,
	}

	var existing githubRelease
	getErr := p.client.Do(ctx, http.MethodGet, byTag, nil, &existing)
	if IsNotFound(getErr) {
		// The lookup by tag only sees published releases, so a rerun of a
		// draft release would create a second draft next to the first.
		draft, found, err := p.findDraft(ctx, base, request.TagName)
		if err != nil {
			return Result{}, err
		}
		if found {
			existing, getErr = draft, nil
		}
	}
	created := true

	switch {
	case getErr == nil:
		// The tag already has a release: update it in place.
		created = false
		if err := p.client.Do(ctx, http.MethodPatch, fmt.Sprintf("%s/%d", base, existing.ID), payload, &existing); err != nil {
			return Result{}, err
		}
	case IsNotFound(getErr):
		if err := p.client.Do(ctx, http.MethodPost, base, payload, &existing); err != nil {
			return Result{}, err
		}
	default:
		return Result{}, getErr
	}

	result := Result{
		URL:      existing.HTMLURL,
		Provider: repository.ProviderGitHub,
		Created:  created,
	}
	if existing.PublishedAt != nil {
		result.PublishedAt = *existing.PublishedAt
	}
	return result, nil
}

// draftSearchPages bounds the scan for a draft release. Drafts are listed
// first, so a handful of pages covers any realistic repository.
const draftSearchPages = 3

func (p *githubPublisher) findDraft(ctx context.Context, base, tagName string) (githubRelease, bool, error) {
	for page := 1; page <= draftSearchPages; page++ {
		var releases []githubRelease
		path := fmt.Sprintf("%s?per_page=100&page=%d", base, page)
		if err := p.client.Do(ctx, http.MethodGet, path, nil, &releases); err != nil {
			return githubRelease{}, false, err
		}
		for _, release := range releases {
			if release.Draft && release.TagName == tagName {
				return release, true, nil
			}
		}
		if len(releases) < 100 {
			break
		}
	}
	return githubRelease{}, false, nil
}

// BackMerge merges the release commit into each target with the merges API,
// which makes the merge commit on the server. A conflict leaves a pull request
// from a branch at the release commit, for a person to resolve.
func (p *githubPublisher) BackMerge(ctx context.Context, request BackMergeRequest) ([]BackMergeResult, error) {
	slug := request.Repository.Slug
	owner, _, _ := strings.Cut(slug, "/")
	results := make([]BackMergeResult, 0, len(request.Targets))
	for _, target := range request.Targets {
		title := backMergeTitle(request.TagName, target)
		status, err := p.client.send(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/merges", slug), map[string]string{
			"base":           target,
			"head":           request.Commit,
			"commit_message": title,
		}, nil)
		switch {
		case err == nil && status == http.StatusNoContent:
			results = append(results, BackMergeResult{Target: target, Outcome: BackMergeUpToDate})
			continue
		case err == nil:
			results = append(results, BackMergeResult{Target: target, Outcome: BackMerged})
			continue
		case !hasStatus(err, http.StatusConflict):
			return results, fmt.Errorf("back-merge %s into %s: %w", request.TagName, target, err)
		}

		branch := backMergeBranch(request.TagName, target)
		err = p.client.Do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/git/refs", slug), map[string]string{
			"ref": "refs/heads/" + branch,
			"sha": request.Commit,
		}, nil)
		// The branch is already there when a previous run got this far.
		if err != nil && !hasStatus(err, http.StatusUnprocessableEntity) {
			return results, fmt.Errorf("create branch %s: %w", branch, err)
		}
		url, err := p.openPullRequest(ctx, slug, owner, branch, target, title, request.TagName)
		if err != nil {
			return results, err
		}
		results = append(results, BackMergeResult{Target: target, Outcome: BackMergePending, URL: url, Reason: "conflict"})
	}
	return results, nil
}

// openPullRequest opens the back-merge pull request, or returns the one a
// previous run opened.
func (p *githubPublisher) openPullRequest(ctx context.Context, slug, owner, branch, target, title, tag string) (string, error) {
	var created struct {
		HTMLURL string `json:"html_url"`
	}
	err := p.client.Do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls", slug), map[string]string{
		"title": title,
		"head":  branch,
		"base":  target,
		"body": fmt.Sprintf("Merging %s into %s conflicts. Resolve the conflict here and merge with a merge commit.",
			tag, target),
	}, &created)
	if err == nil {
		return created.HTMLURL, nil
	}
	if !hasStatus(err, http.StatusUnprocessableEntity) {
		return "", fmt.Errorf("open pull request %s -> %s: %w", branch, target, err)
	}
	var existing []struct {
		HTMLURL string `json:"html_url"`
	}
	query := url.Values{"head": {owner + ":" + branch}, "base": {target}, "state": {"open"}}
	if err := p.client.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls?%s", slug, query.Encode()), nil, &existing); err != nil {
		return "", fmt.Errorf("find pull request %s -> %s: %w", branch, target, err)
	}
	if len(existing) == 0 {
		return "", fmt.Errorf("open pull request %s -> %s: %w", branch, target, err)
	}
	return existing[0].HTMLURL, nil
}
