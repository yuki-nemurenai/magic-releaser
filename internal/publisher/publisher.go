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

	"github.com/yuki-nemurenai/go-magic-releaser/internal/repository"
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
		BaseURL:    strings.TrimRight(base, "/"),
		Token:      options.Token,
		MaxRetries: options.MaxRetries,
		Timeout:    options.Timeout,
		Verbose:    options.Verbose,
		HTTPClient: options.HTTPClient,
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
	BaseURL    string
	Token      repository.Token
	MaxRetries int
	Timeout    time.Duration
	Verbose    io.Writer
	HTTPClient *http.Client
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
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	attempts := client.retries() + 1
	for attempt := 1; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, method, client.BaseURL+path, payload)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "application/json")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		client.authorize(request)

		response, err := client.httpClient().Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			if attempt >= attempts {
				return fmt.Errorf("%s %s: %w", method, path, err)
			}
			delay := backoff(attempt)
			client.logf("%s %s failed (%v), retrying in %s", method, path, err, delay)
			if err := sleep(ctx, delay); err != nil {
				return err
			}
			// The body reader is consumed by the failed attempt.
			if body != nil {
				encoded, encodeErr := json.Marshal(body)
				if encodeErr != nil {
					return encodeErr
				}
				payload = bytes.NewReader(encoded)
			}
			continue
		}

		data, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read response: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close response: %w", closeErr)
		}

		if retryableStatus(response.StatusCode) && attempt < attempts {
			delay := retryDelay(response, attempt)
			client.logf("%s %s returned %d, retrying in %s", method, path, response.StatusCode, delay)
			if err := sleep(ctx, delay); err != nil {
				return err
			}
			if body != nil {
				encoded, encodeErr := json.Marshal(body)
				if encodeErr != nil {
					return encodeErr
				}
				payload = bytes.NewReader(encoded)
			}
			continue
		}

		if response.StatusCode >= 300 {
			return newAPIError(method, path, response.StatusCode, data)
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
		}
		return nil
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
