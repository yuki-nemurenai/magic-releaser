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

// gitlabPublisher talks to the GitLab Releases API.
//
// The project is addressed by its URL encoded path, so nested groups work
// without resolving the numeric project id.
type gitlabPublisher struct {
	client *Client
}

func (p *gitlabPublisher) Provider() repository.Provider { return repository.ProviderGitLab }

type gitlabRelease struct {
	ID          int        `json:"id"`
	Name        string     `json:"name"`
	TagName     string     `json:"tag_name"`
	Description string     `json:"description"`
	ReleasedAt  *time.Time `json:"released_at"`
	Links       struct {
		Self string `json:"self"`
	} `json:"_links"`
}

type gitlabReleaseRequest struct {
	Name        string `json:"name"`
	TagName     string `json:"tag_name"`
	Description string `json:"description"`
	Ref         string `json:"ref,omitempty"`
}

func (p *gitlabPublisher) Publish(ctx context.Context, request Request) (Result, error) {
	project := url.PathEscape(request.Repository.Slug)
	base := fmt.Sprintf("/projects/%s/releases", project)
	byTag := base + "/" + url.PathEscape(request.TagName)

	payload := gitlabReleaseRequest{
		Name:        releaseName(request),
		TagName:     request.TagName,
		Description: request.Notes,
		Ref:         request.Target,
	}

	var existing gitlabRelease
	getErr := p.client.Do(ctx, http.MethodGet, byTag, nil, &existing)
	created := true

	switch {
	case getErr == nil:
		// GitLab rejects a duplicate tag with 409, so an existing release is
		// updated rather than recreated.
		created = false
		if err := p.client.Do(ctx, http.MethodPut, byTag, payload, &existing); err != nil {
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
		URL:      existing.Links.Self,
		Provider: repository.ProviderGitLab,
		Created:  created,
	}
	if existing.ReleasedAt != nil {
		result.PublishedAt = *existing.ReleasedAt
	}
	return result, nil
}

// releaseName is the title shown on the release page. It falls back to the
// tag when the caller did not provide a name.
func releaseName(request Request) string {
	if strings.TrimSpace(request.Name) != "" {
		return request.Name
	}
	return request.TagName
}

// mergeStatusPolls bounds the wait for GitLab to check whether a merge request
// can be merged; the check runs in the background after the request opens.
const mergeStatusPolls = 30

// gitlabMergeRequest is the part of a merge request the back-merge reads.
type gitlabMergeRequest struct {
	IID                 int    `json:"iid"`
	WebURL              string `json:"web_url"`
	DetailedMergeStatus string `json:"detailed_merge_status"`
}

// BackMerge merges the release commit into each target. GitLab merges only
// merge requests, so the release commit gets a branch and a merge request,
// which is merged right away when GitLab finds it mergeable. Otherwise, after
// a conflict or a project rule such as a required approval, it stays open for
// a person.
func (p *gitlabPublisher) BackMerge(ctx context.Context, request BackMergeRequest) ([]BackMergeResult, error) {
	base := "/projects/" + url.PathEscape(request.Repository.Slug)
	results := make([]BackMergeResult, 0, len(request.Targets))
	for _, target := range request.Targets {
		var compare struct {
			Commits []struct{} `json:"commits"`
		}
		query := url.Values{"from": {target}, "to": {request.Commit}}
		if err := p.client.Do(ctx, http.MethodGet, base+"/repository/compare?"+query.Encode(), nil, &compare); err != nil {
			return results, fmt.Errorf("compare %s with %s: %w", target, request.TagName, err)
		}
		if len(compare.Commits) == 0 {
			results = append(results, BackMergeResult{Target: target, Outcome: BackMergeUpToDate})
			continue
		}

		title := backMergeTitle(request.TagName, target)
		branch := backMergeBranch(request.TagName, target)
		err := p.client.Do(ctx, http.MethodPost, base+"/repository/branches", map[string]string{
			"branch": branch,
			"ref":    request.Commit,
		}, nil)
		// The branch is already there when a previous run got this far.
		if err != nil && !hasStatus(err, http.StatusBadRequest) {
			return results, fmt.Errorf("create branch %s: %w", branch, err)
		}
		mergeRequest, err := p.openMergeRequest(ctx, base, branch, target, title, request.TagName)
		if err != nil {
			return results, err
		}
		result, err := p.mergeWhenMergeable(ctx, base, mergeRequest, target, title)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

// openMergeRequest opens the back-merge merge request, or returns the one a
// previous run opened.
func (p *gitlabPublisher) openMergeRequest(ctx context.Context, base, branch, target, title, tag string) (gitlabMergeRequest, error) {
	var created gitlabMergeRequest
	err := p.client.Do(ctx, http.MethodPost, base+"/merge_requests", map[string]any{
		"source_branch":        branch,
		"target_branch":        target,
		"title":                title,
		"description":          fmt.Sprintf("Back-merge of the release %s into %s.", tag, target),
		"remove_source_branch": true,
	}, &created)
	if err == nil {
		return created, nil
	}
	if !hasStatus(err, http.StatusConflict) {
		return gitlabMergeRequest{}, fmt.Errorf("open merge request %s -> %s: %w", branch, target, err)
	}
	var existing []gitlabMergeRequest
	query := url.Values{"source_branch": {branch}, "target_branch": {target}, "state": {"opened"}}
	if err := p.client.Do(ctx, http.MethodGet, base+"/merge_requests?"+query.Encode(), nil, &existing); err != nil {
		return gitlabMergeRequest{}, fmt.Errorf("find merge request %s -> %s: %w", branch, target, err)
	}
	if len(existing) == 0 {
		return gitlabMergeRequest{}, fmt.Errorf("open merge request %s -> %s: %w", branch, target, err)
	}
	return existing[0], nil
}

// mergeStatusPending reports whether GitLab is still checking a merge request.
func mergeStatusPending(status string) bool {
	switch status {
	case "", "unchecked", "checking", "preparing", "approvals_syncing":
		return true
	default:
		return false
	}
}

// mergeWhenMergeable waits for GitLab to check the merge request and merges
// it when it can be merged.
func (p *gitlabPublisher) mergeWhenMergeable(ctx context.Context, base string, mergeRequest gitlabMergeRequest, target, title string) (BackMergeResult, error) {
	path := fmt.Sprintf("%s/merge_requests/%d", base, mergeRequest.IID)
	for poll := 0; poll < mergeStatusPolls && mergeStatusPending(mergeRequest.DetailedMergeStatus); poll++ {
		if err := sleep(ctx, p.client.pollInterval()); err != nil {
			return BackMergeResult{}, err
		}
		if err := p.client.Do(ctx, http.MethodGet, path, nil, &mergeRequest); err != nil {
			return BackMergeResult{}, fmt.Errorf("read merge request !%d: %w", mergeRequest.IID, err)
		}
	}
	pending := BackMergeResult{Target: target, Outcome: BackMergePending, URL: mergeRequest.WebURL,
		Reason: mergeRequest.DetailedMergeStatus}
	if mergeRequest.DetailedMergeStatus != "mergeable" {
		if mergeStatusPending(pending.Reason) {
			pending.Reason = "not checked in time"
		}
		return pending, nil
	}
	err := p.client.Do(ctx, http.MethodPut, path+"/merge", map[string]any{
		"merge_commit_message":        title,
		"should_remove_source_branch": true,
	}, nil)
	if err == nil {
		return BackMergeResult{Target: target, Outcome: BackMerged, URL: mergeRequest.WebURL}, nil
	}
	// The project refused the merge (merge method, pipeline or approval rules):
	// the open merge request is left to a person.
	if hasStatus(err, http.StatusMethodNotAllowed, http.StatusNotAcceptable, http.StatusConflict, http.StatusUnprocessableEntity) {
		pending.Reason = err.Error()
		return pending, nil
	}
	return BackMergeResult{}, fmt.Errorf("merge !%d: %w", mergeRequest.IID, err)
}
