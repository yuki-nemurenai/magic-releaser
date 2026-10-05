package publisher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yuki-nemurenai/magic-releaser/internal/repository"
)

const releaseCommit = "0123456789abcdef0123456789abcdef01234567"

func backMergeRequest(info repository.Info, targets ...string) BackMergeRequest {
	return BackMergeRequest{Repository: info, TagName: "v1.2.0", Commit: releaseCommit, Targets: targets}
}

func newBackMergePublisher(t *testing.T, info repository.Info, handler http.HandlerFunc) Publisher {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	pub, err := New(info, Options{
		BaseURL:      server.URL,
		Token:        repository.Token{Value: "t"},
		MaxRetries:   1,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return pub
}

// GitHub merges on the server: 201 is a merge, 204 means nothing to merge, and
// a conflict opens a pull request from a branch at the release commit.
func TestGitHubBackMerge(t *testing.T) {
	var refs, pulls []map[string]string
	pub := newBackMergePublisher(t, githubInfo(), func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/demo/merges":
			if body["head"] != releaseCommit || body["commit_message"] != "chore: back-merge v1.2.0 into "+body["base"] {
				t.Errorf("merge request body = %v", body)
			}
			switch body["base"] {
			case "develop":
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"sha":"abc"}`))
			case "candidate":
				w.WriteHeader(http.StatusNoContent)
			case "release/1.x":
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"Merge conflict"}`))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/demo/git/refs":
			refs = append(refs, body)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/demo/pulls":
			pulls = append(pulls, body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"html_url":"https://github.com/octo/demo/pull/7"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
	})

	results, err := pub.BackMerge(context.Background(), backMergeRequest(githubInfo(), "develop", "candidate", "release/1.x"))
	if err != nil {
		t.Fatalf("BackMerge() error = %v", err)
	}
	want := []BackMergeResult{
		{Target: "develop", Outcome: BackMerged},
		{Target: "candidate", Outcome: BackMergeUpToDate},
		{Target: "release/1.x", Outcome: BackMergePending, URL: "https://github.com/octo/demo/pull/7", Reason: "conflict"},
	}
	if len(results) != len(want) {
		t.Fatalf("results = %+v", results)
	}
	for i := range want {
		if results[i] != want[i] {
			t.Fatalf("results[%d] = %+v, want %+v", i, results[i], want[i])
		}
	}
	if len(refs) != 1 || refs[0]["ref"] != "refs/heads/back-merge/v1.2.0/release/1.x" || refs[0]["sha"] != releaseCommit {
		t.Fatalf("branch = %v", refs)
	}
	if len(pulls) != 1 || pulls[0]["head"] != "back-merge/v1.2.0/release/1.x" || pulls[0]["base"] != "release/1.x" {
		t.Fatalf("pull request = %v", pulls)
	}
}

// A rerun after a conflict finds the branch and the pull request it made.
func TestGitHubBackMergeRerunFindsThePullRequest(t *testing.T) {
	pub := newBackMergePublisher(t, githubInfo(), func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/octo/demo/merges":
			w.WriteHeader(http.StatusConflict)
		case r.URL.Path == "/repos/octo/demo/git/refs", r.Method == http.MethodPost && r.URL.Path == "/repos/octo/demo/pulls":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"already exists"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/demo/pulls":
			if r.URL.Query().Get("head") != "octo:back-merge/v1.2.0/develop" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"html_url":"https://github.com/octo/demo/pull/3"}]`))
		}
	})
	results, err := pub.BackMerge(context.Background(), backMergeRequest(githubInfo(), "develop"))
	if err != nil {
		t.Fatalf("BackMerge() error = %v", err)
	}
	if results[0].URL != "https://github.com/octo/demo/pull/3" || results[0].Outcome != BackMergePending {
		t.Fatalf("results = %+v", results)
	}
}

func TestGitHubBackMergeFailsOnAnAPIError(t *testing.T) {
	pub := newBackMergePublisher(t, githubInfo(), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	})
	_, err := pub.BackMerge(context.Background(), backMergeRequest(githubInfo(), "develop"))
	if err == nil || !strings.Contains(err.Error(), "Resource not accessible") {
		t.Fatalf("BackMerge() error = %v, want the forge message", err)
	}
}

// GitLab merges only merge requests: a branch at the release commit, a merge
// request, and a merge once GitLab has found it mergeable.
func TestGitLabBackMerge(t *testing.T) {
	polls := map[int]int{}
	var merged []int
	pub := newBackMergePublisher(t, gitlabInfo(), func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		project := "/projects/group%2Fsubgroup%2Fdemo"
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case path == project+"/repository/compare":
			if r.URL.Query().Get("to") != releaseCommit {
				t.Errorf("compare query = %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("from") == "candidate" {
				_, _ = w.Write([]byte(`{"commits":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"commits":[{"id":"x"}]}`))
		case path == project+"/repository/branches":
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && path == project+"/merge_requests":
			iid := map[string]int{"develop": 1, "main-next": 2, "strict": 3}[body["target_branch"].(string)]
			if body["remove_source_branch"] != true {
				t.Errorf("merge request body = %v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"iid":` + itoa(iid) + `,"web_url":"https://gitlab.com/mr/` + itoa(iid) + `","detailed_merge_status":"checking"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(path, project+"/merge_requests/"):
			iid := int(path[len(path)-1] - '0')
			polls[iid]++
			status := map[int]string{1: "mergeable", 2: "conflict", 3: "mergeable"}[iid]
			if polls[iid] < 2 {
				status = "checking"
			}
			_, _ = w.Write([]byte(`{"iid":` + itoa(iid) + `,"web_url":"https://gitlab.com/mr/` + itoa(iid) + `","detailed_merge_status":"` + status + `"}`))
		case r.Method == http.MethodPut && strings.HasSuffix(path, "/merge"):
			iid := int(path[len(path)-len("/merge")-1] - '0')
			if iid == 3 {
				w.WriteHeader(http.StatusMethodNotAllowed)
				_, _ = w.Write([]byte(`{"message":"Method Not Allowed"}`))
				return
			}
			if body["merge_commit_message"] != "chore: back-merge v1.2.0 into develop" || body["should_remove_source_branch"] != true {
				t.Errorf("merge body = %v", body)
			}
			merged = append(merged, iid)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, path)
		}
	})

	results, err := pub.BackMerge(context.Background(), backMergeRequest(gitlabInfo(), "develop", "candidate", "main-next", "strict"))
	if err != nil {
		t.Fatalf("BackMerge() error = %v", err)
	}
	outcomes := []BackMergeOutcome{BackMerged, BackMergeUpToDate, BackMergePending, BackMergePending}
	for i, outcome := range outcomes {
		if results[i].Outcome != outcome {
			t.Fatalf("results[%d] = %+v, want %s", i, results[i], outcome)
		}
	}
	if results[2].Reason != "conflict" || results[2].URL != "https://gitlab.com/mr/2" {
		t.Fatalf("conflict result = %+v", results[2])
	}
	if !strings.Contains(results[3].Reason, "405") {
		t.Fatalf("refused merge result = %+v", results[3])
	}
	if len(merged) != 1 || merged[0] != 1 {
		t.Fatalf("merged = %v, want only !1", merged)
	}
}

func itoa(n int) string {
	return string(rune('0' + n))
}
