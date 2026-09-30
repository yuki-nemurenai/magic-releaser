package publisher

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/yuki-nemurenai/go-magic-releaser/internal/repository"
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
