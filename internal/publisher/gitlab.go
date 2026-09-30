package publisher

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yuki-nemurenai/go-magic-releaser/internal/repository"
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
