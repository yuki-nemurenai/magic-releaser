// Package publisher publishes releases to GitHub and GitLab.
package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yuki-nemurenai/magic-releaser/internal/repository"
)

// DefaultBaseURL returns the API root for a repository host. Inside a CI job
// on that host the job's own API URL wins over the guess.
func DefaultBaseURL(info repository.Info) string {
	if base := repository.CIAPIURL(info); base != "" {
		return base
	}
	switch info.Provider {
	case repository.ProviderGitHub:
		return "https://api.github.com"
	case repository.ProviderGitLab:
		if info.Host == "gitlab.com" || info.Host == "" {
			return "https://gitlab.com/api/v4"
		}
		return "https://" + info.Host + "/api/v4"
	default:
		return ""
	}
}

// Request describes the release to publish.
type Request struct {
	Repository repository.Info
	TagName    string
	Version    string
	Name       string
	Notes      string
	Target     string
	Draft      bool
	Prerelease bool
}

// Result describes the published release.
type Result struct {
	URL         string
	PublishedAt time.Time
	Provider    repository.Provider
	Created     bool
}

type Publisher interface {
	Provider() repository.Provider
	Publish(ctx context.Context, request Request) (Result, error)
	// BackMerge merges a release into other branches on the forge. A branch
	// that cannot be merged automatically gets a pull or merge request for a
	// person instead of an error.
	BackMerge(ctx context.Context, request BackMergeRequest) ([]BackMergeResult, error)
}

// BackMergeRequest asks to merge a release commit into other branches.
type BackMergeRequest struct {
	Repository repository.Info
	TagName    string
	// Commit is the full SHA of the release commit.
	Commit  string
	Targets []string
}

// BackMergeOutcome tells what happened to one target branch.
type BackMergeOutcome string

const (
	// BackMerged means the release was merged into the branch.
	BackMerged BackMergeOutcome = "merged"
	// BackMergeUpToDate means the branch already contained the release.
	BackMergeUpToDate BackMergeOutcome = "up-to-date"
	// BackMergePending means a pull or merge request waits for a person,
	// because of a conflict or a rule of the forge.
	BackMergePending BackMergeOutcome = "pending"
)

// BackMergeResult is the outcome for one target branch.
type BackMergeResult struct {
	Target  string
	Outcome BackMergeOutcome
	// URL is the pull or merge request of a pending back-merge.
	URL string
	// Reason explains a pending back-merge, e.g. a conflict.
	Reason string
}

// backMergeTitle is the title of the merge commit and of the request.
func backMergeTitle(tag, target string) string {
	return fmt.Sprintf("chore: back-merge %s into %s", tag, target)
}

// backMergeBranch is the branch that carries the release commit into a
// request when the forge cannot merge it right away.
func backMergeBranch(tag, target string) string {
	return "back-merge/" + tag + "/" + target
}

// Options configures a publisher.
type Options struct {
	// BaseURL overrides the API root. Required for self-hosted forges and used
	// by the tests to point at a local server.
	BaseURL string
	Token   repository.Token
	// MaxRetries bounds the retries of a rate limited or unavailable API.
	MaxRetries int
	// Timeout bounds a single HTTP call.
	Timeout time.Duration
	// Verbose receives retry diagnostics, for example in CI logs.
	Verbose io.Writer
	// HTTPClient overrides the transport, mainly for tests.
	HTTPClient *http.Client
	// PollInterval spaces the polls of an asynchronous forge check, mainly
	// for tests.
	PollInterval time.Duration
}

// New builds a publisher for the provider of the given repository.
func New(info repository.Info, options Options) (Publisher, error) {
	if !info.Found() {
		return nil, errors.New("cannot publish: repository is unknown, configure a git remote")
	}
	provider := info.Provider
	if provider == "" || provider == repository.ProviderAuto {
		return nil, fmt.Errorf("cannot publish to %s: the provider could not be detected, pass --provider github|gitlab", info.Host)
	}
	if options.Token.Value == "" {
		return nil, fmt.Errorf("no token found for %s: set one of %s", info.Host, strings.Join(repository.DefaultTokenEnv(provider), ", "))
	}
	base := options.BaseURL
	if base == "" {
		base = DefaultBaseURL(info)
	}
	if base == "" {
		return nil, fmt.Errorf("no API base URL known for host %q, pass --api-url", info.Host)
	}
	client := &Client{
		BaseURL:      strings.TrimRight(base, "/"),
		Token:        options.Token,
		MaxRetries:   options.MaxRetries,
		Timeout:      options.Timeout,
		Verbose:      options.Verbose,
		HTTPClient:   options.HTTPClient,
		PollInterval: options.PollInterval,
	}
	switch provider {
	case repository.ProviderGitHub:
		return &githubPublisher{client: client}, nil
	case repository.ProviderGitLab:
		return &gitlabPublisher{client: client}, nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
}

const (
	defaultMaxRetries = 3
	defaultTimeout    = 30 * time.Second
)

// Client is a small JSON HTTP client with retry and rate limit handling.
// It is deliberately forge-agnostic: only the request shape differs.
type Client struct {
	BaseURL      string
	Token        repository.Token
	MaxRetries   int
	Timeout      time.Duration
	Verbose      io.Writer
	HTTPClient   *http.Client
	PollInterval time.Duration
}

func (client *Client) pollInterval() time.Duration {
	if client.PollInterval > 0 {
		return client.PollInterval
	}
	return 2 * time.Second
}

func (client *Client) httpClient() *http.Client {
	if client.HTTPClient != nil {
		return client.HTTPClient
	}
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &http.Client{Timeout: timeout}
}

func (client *Client) retries() int {
	if client.MaxRetries > 0 {
		return client.MaxRetries
	}
	return defaultMaxRetries
}

// Do performs a JSON request. A non 2xx response is returned as an APIError
// so callers can tell a missing release from a rejected one.
func (client *Client) Do(ctx context.Context, method, path string, body, out any) error {
	_, err := client.send(ctx, method, path, body, out)
	return err
}

// send performs a JSON request like Do and also reports the status of a
// successful response, which some endpoints use to tell outcomes apart.
func (client *Client) send(ctx context.Context, method, path string, body, out any) (int, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encode request body: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	attempts := client.retries() + 1
	for attempt := 1; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, method, client.BaseURL+path, payload)
		if err != nil {
			return 0, err
		}
		request.Header.Set("Accept", "application/json")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		client.authorize(request)

		response, err := client.httpClient().Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return 0, err
			}
			if attempt >= attempts {
				return 0, fmt.Errorf("%s %s: %w", method, path, err)
			}
			delay := backoff(attempt)
			client.logf("%s %s failed (%v), retrying in %s", method, path, err, delay)
			if err := sleep(ctx, delay); err != nil {
				return 0, err
			}
			// The body reader is consumed by the failed attempt.
			if body != nil {
				encoded, encodeErr := json.Marshal(body)
				if encodeErr != nil {
					return 0, encodeErr
				}
				payload = bytes.NewReader(encoded)
			}
			continue
		}

		data, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil {
			return 0, fmt.Errorf("read response: %w", readErr)
		}
		if closeErr != nil {
			return 0, fmt.Errorf("close response: %w", closeErr)
		}

		if retryableStatus(response.StatusCode) && attempt < attempts {
			delay := retryDelay(response, attempt)
			client.logf("%s %s returned %d, retrying in %s", method, path, response.StatusCode, delay)
			if err := sleep(ctx, delay); err != nil {
				return 0, err
			}
			if body != nil {
				encoded, encodeErr := json.Marshal(body)
				if encodeErr != nil {
					return 0, encodeErr
				}
				payload = bytes.NewReader(encoded)
			}
			continue
		}

		if response.StatusCode >= 300 {
			return response.StatusCode, newAPIError(method, path, response.StatusCode, data)
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return 0, fmt.Errorf("decode response: %w", err)
			}
		}
		return response.StatusCode, nil
	}
}

// authorize attaches the credential. A GitLab CI job token is rejected as a
// bearer token and has to travel in its own header.
func (client *Client) authorize(request *http.Request) {
	switch {
	case client.Token.Value == "":
	case client.Token.Job:
		request.Header.Set("JOB-TOKEN", client.Token.Value)
	default:
		request.Header.Set("Authorization", "Bearer "+client.Token.Value)
	}
}

func (client *Client) logf(format string, args ...any) {
	if client.Verbose == nil {
		return
	}
	fmt.Fprintf(client.Verbose, "[publisher] "+format+"\n", args...)
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// retryDelay honours Retry-After when the forge sends one, otherwise it backs
// off exponentially.
func retryDelay(response *http.Response, attempt int) time.Duration {
	if header := response.Header.Get("Retry-After"); header != "" {
		if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return backoff(attempt)
}

func backoff(attempt int) time.Duration {
	base := 2 * time.Second
	for i := 1; i < attempt; i++ {
		base *= 2
	}
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	return base
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// APIError describes a non 2xx response from a forge API.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
}

func newAPIError(method, path string, status int, data []byte) *APIError {
	return &APIError{
		Method:     method,
		Path:       path,
		StatusCode: status,
		Message:    extractMessage(data),
	}
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s %s: unexpected status %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("%s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Message)
}

// hasStatus reports whether the error is an APIError with one of the statuses.
func hasStatus(err error, statuses ...int) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, status := range statuses {
		if apiErr.StatusCode == status {
			return true
		}
	}
	return false
}

// IsNotFound reports whether the error is a 404, which both forges use to
// signal that a release has to be created rather than updated.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return false
}

// extractMessage pulls the human readable part out of a forge error body,
// falling back to a trimmed snippet.
func extractMessage(data []byte) string {
	var payload struct {
		Message       string `json:"message"`
		Error         string `json:"error"`
		ErrorMessage  string `json:"error_message"`
		MessageDetail any    `json:"message_details"`
	}
	if err := json.Unmarshal(data, &payload); err == nil {
		for _, candidate := range []string{payload.Message, payload.Error, payload.ErrorMessage} {
			if candidate != "" {
				return candidate
			}
		}
	}
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) > 300 {
		trimmed = trimmed[:300] + "..."
	}
	return trimmed
}
