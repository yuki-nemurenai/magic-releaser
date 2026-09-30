package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yuki-nemurenai/go-magic-releaser/internal/repository"
)

func githubInfo() repository.Info {
	return repository.Info{
		Provider: repository.ProviderGitHub,
		Host:     "github.com",
		Slug:     "octo/demo",
		WebURL:   "https://github.com/octo/demo",
	}
}

func gitlabInfo() repository.Info {
	return repository.Info{
		Provider: repository.ProviderGitLab,
		Host:     "gitlab.com",
		Slug:     "group/subgroup/demo",
		WebURL:   "https://gitlab.com/group/subgroup/demo",
	}
}

func testRequest(info repository.Info) Request {
	return Request{
		Repository: info,
		TagName:    "v1.2.3",
		Version:    "1.2.3",
		Notes:      "## 1.2.3\n\n### Features\n\n- a thing",
	}
}

func newTestPublisher(t *testing.T, info repository.Info, handler http.Handler) (Publisher, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	pub, err := New(info, Options{
		BaseURL:    server.URL,
		Token:      repository.Token{Value: "test-token"},
		MaxRetries: 2,
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return pub, server
}

func TestGitHubCreatesReleaseWhenTagHasNone(t *testing.T) {
	var posted githubReleaseRequest
	var usedAuth string

	pub, _ := newTestPublisher(t, githubInfo(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		usedAuth = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/demo/releases/tags/v1.2.3":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/demo/releases":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/demo/releases":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Errorf("decode request error = %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":42,"html_url":"https://github.com/octo/demo/releases/tag/v1.2.3","tag_name":"v1.2.3","published_at":"2026-08-11T10:00:00Z"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))

	result, err := pub.Publish(context.Background(), testRequest(githubInfo()))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if !result.Created {
		t.Fatal("Created = false, want true")
	}
	if result.URL != "https://github.com/octo/demo/releases/tag/v1.2.3" {
		t.Fatalf("URL = %q", result.URL)
	}
	if result.PublishedAt.IsZero() {
		t.Fatal("PublishedAt is zero")
	}
	if usedAuth != "Bearer test-token" {
		t.Fatalf("Authorization = %q", usedAuth)
	}
	if posted.TagName != "v1.2.3" || posted.Body == "" {
		t.Fatalf("posted payload = %+v", posted)
	}
}

func TestGitHubUpdatesExistingRelease(t *testing.T) {
	var patched string

	pub, _ := newTestPublisher(t, githubInfo(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":42,"html_url":"https://github.com/octo/demo/releases/tag/v1.2.3","tag_name":"v1.2.3"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/octo/demo/releases/42":
			patched = r.URL.Path
			_, _ = w.Write([]byte(`{"id":42,"html_url":"https://github.com/octo/demo/releases/tag/v1.2.3","tag_name":"v1.2.3"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))

	result, err := pub.Publish(context.Background(), testRequest(githubInfo()))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.Created {
		t.Fatal("Created = true, want false for an existing release")
	}
	if patched != "/repos/octo/demo/releases/42" {
		t.Fatalf("PATCH path = %q", patched)
	}
}

func TestGitLabCreatesAndUpdatesRelease(t *testing.T) {
	var gotPath string
	var posted gitlabReleaseRequest

	pub, _ := newTestPublisher(t, gitlabInfo(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"404 Release Not Found"}`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Errorf("decode request error = %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":7,"_links":{"self":"https://gitlab.com/group/subgroup/demo/-/releases/v1.2.3"},"tag_name":"v1.2.3","released_at":"2026-08-11T10:00:00Z"}`))
	}))

	result, err := pub.Publish(context.Background(), testRequest(gitlabInfo()))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	// The nested group has to survive URL encoding.
	if gotPath != "/projects/group%2Fsubgroup%2Fdemo/releases" {
		t.Fatalf("path = %q, want the URL encoded project path", gotPath)
	}
	if !result.Created {
		t.Fatal("Created = false, want true")
	}
	if posted.TagName != "v1.2.3" {
		t.Fatalf("posted tag = %q", posted.TagName)
	}
	// GitLab has no top level url: the release page is in _links.self.
	if result.URL != "https://gitlab.com/group/subgroup/demo/-/releases/v1.2.3" {
		t.Fatalf("URL = %q, want the _links.self page", result.URL)
	}
}

// A CI job token is rejected as a bearer token: it needs its own header.
func TestGitLabSendsJobTokenInItsOwnHeader(t *testing.T) {
	var jobToken, bearer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jobToken, bearer = r.Header.Get("JOB-TOKEN"), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":7,"tag_name":"v1.2.3"}`))
	}))
	t.Cleanup(server.Close)
	pub, err := New(gitlabInfo(), Options{BaseURL: server.URL, Token: repository.Token{Value: "job", Job: true}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := pub.Publish(context.Background(), testRequest(gitlabInfo())); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if jobToken != "job" || bearer != "" {
		t.Fatalf("JOB-TOKEN = %q, Authorization = %q; want the job token in JOB-TOKEN only", jobToken, bearer)
	}
}

// The lookup by tag does not see drafts, so a rerun has to find the draft by
// listing, and update it instead of creating a duplicate.
func TestGitHubUpdatesAnExistingDraft(t *testing.T) {
	var created bool
	var patched string
	pub, _ := newTestPublisher(t, githubInfo(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/demo/releases/tags/v1.2.3":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/demo/releases":
			_, _ = w.Write([]byte(`[{"id":9,"draft":true,"tag_name":"v1.2.3"},{"id":8,"tag_name":"v1.2.2"}]`))
		case r.Method == http.MethodPatch:
			patched = r.URL.Path
			_, _ = w.Write([]byte(`{"id":9,"draft":true,"tag_name":"v1.2.3"}`))
		case r.Method == http.MethodPost:
			created = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":10}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	request := testRequest(githubInfo())
	request.Draft = true
	result, err := pub.Publish(context.Background(), request)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if created || result.Created {
		t.Fatal("a second draft was created, want the existing draft updated")
	}
	if patched != "/repos/octo/demo/releases/9" {
		t.Fatalf("PATCH path = %q, want the draft release", patched)
	}
}

func TestGitLabUpdatesExistingRelease(t *testing.T) {
	var method string
	pub, _ := newTestPublisher(t, gitlabInfo(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":7,"_links":{"self":"https://gitlab.com/g/d/-/releases/v1.2.3"},"tag_name":"v1.2.3"}`))
			return
		}
		method = r.Method
		_, _ = w.Write([]byte(`{"id":7,"_links":{"self":"https://gitlab.com/g/d/-/releases/v1.2.3"},"tag_name":"v1.2.3"}`))
	}))

	result, err := pub.Publish(context.Background(), testRequest(gitlabInfo()))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.Created {
		t.Fatal("Created = true, want false")
	}
	if method != http.MethodPut {
		t.Fatalf("method = %q, want PUT", method)
	}
}

func TestAPIErrorsCarryTheForgeMessage(t *testing.T) {
	pub, _ := newTestPublisher(t, githubInfo(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/repos/octo/demo/releases" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"Release","code":"already_exists"}]}`))
	}))

	_, err := pub.Publish(context.Background(), testRequest(githubInfo()))
	if err == nil {
		t.Fatal("Publish() error = nil, want an API error")
	}
	if !strings.Contains(err.Error(), "Validation Failed") {
		t.Fatalf("error should carry the forge message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "422") {
		t.Fatalf("error should carry the status, got: %v", err)
	}
}

// A rate limited request is retried instead of failing the release.
func TestRetriesOnRateLimit(t *testing.T) {
	calls := 0
	pub, _ := newTestPublisher(t, githubInfo(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":1,"html_url":"https://github.com/octo/demo/releases/tag/v1.2.3"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":1,"html_url":"https://github.com/octo/demo/releases/tag/v1.2.3"}`))
	}))

	if _, err := pub.Publish(context.Background(), testRequest(githubInfo())); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if calls < 3 {
		t.Fatalf("calls = %d, want the request to be retried", calls)
	}
}

func TestRetriesOnServerError(t *testing.T) {
	calls := 0
	pub, _ := newTestPublisher(t, gitlabInfo(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"id":7,"_links":{"self":"https://gitlab.com/g/d/-/releases/v1.2.3"}}`))
	}))

	if _, err := pub.Publish(context.Background(), testRequest(gitlabInfo())); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, want a retry", calls)
	}
}

// A 4xx that is not rate limiting must not be retried.
func TestDoesNotRetryClientErrors(t *testing.T) {
	calls := 0
	pub, _ := newTestPublisher(t, githubInfo(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))

	if _, err := pub.Publish(context.Background(), testRequest(githubInfo())); err == nil {
		t.Fatal("Publish() error = nil, want an error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 without retries", calls)
	}
}

func TestIsNotFound(t *testing.T) {
	err := newAPIError(http.MethodGet, "/x", http.StatusNotFound, []byte(`{"message":"Not Found"}`))
	if !IsNotFound(err) {
		t.Fatal("IsNotFound = false, want true")
	}
	other := newAPIError(http.MethodPost, "/x", http.StatusForbidden, nil)
	if IsNotFound(other) {
		t.Fatal("IsNotFound = true for 403, want false")
	}
	if IsNotFound(fmt.Errorf("network down")) {
		t.Fatal("IsNotFound = true for a plain error, want false")
	}
}

func TestNewValidatesConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		info    repository.Info
		options Options
		wantErr string
	}{
		{"no repository", repository.Info{}, Options{Token: repository.Token{Value: "t"}}, "repository is unknown"},
		{"unknown provider", repository.Info{Host: "git.example.com", Slug: "a/b"}, Options{Token: repository.Token{Value: "t"}}, "could not be detected"},
		{"missing token", githubInfo(), Options{}, "no token found"},
		{"unsupported provider", repository.Info{Host: "git.example.com", Slug: "a/b", Provider: repository.Provider("bitbucket")}, Options{Token: repository.Token{Value: "t"}}, "no API base URL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.info, test.options)
			if err == nil {
				t.Fatalf("New() error = nil, want %q", test.wantErr)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

func TestDefaultBaseURL(t *testing.T) {
	if got := DefaultBaseURL(githubInfo()); got != "https://api.github.com" {
		t.Fatalf("github base = %q", got)
	}
	if got := DefaultBaseURL(gitlabInfo()); got != "https://gitlab.com/api/v4" {
		t.Fatalf("gitlab base = %q", got)
	}
	selfHosted := repository.Info{Provider: repository.ProviderGitLab, Host: "gitlab.example.com"}
	if got := DefaultBaseURL(selfHosted); got != "https://gitlab.example.com/api/v4" {
		t.Fatalf("self hosted base = %q", got)
	}
}

func TestReleaseNameFallsBackToTag(t *testing.T) {
	request := testRequest(githubInfo())
	if got := releaseName(request); got != "v1.2.3" {
		t.Fatalf("releaseName = %q", got)
	}
	request.Name = "Release 1.2.3"
	if got := releaseName(request); got != "Release 1.2.3" {
		t.Fatalf("releaseName = %q", got)
	}
}

// A cancelled context must abort instead of retrying.
func TestRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pub, _ := newTestPublisher(t, githubInfo(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made with a cancelled context")
	}))
	if _, err := pub.Publish(ctx, testRequest(githubInfo())); err == nil {
		t.Fatal("Publish() error = nil, want a context error")
	}
}
