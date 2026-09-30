package repository

import (
	"context"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantHost  string
		wantSlug  string
		wantForge Provider
	}{
		{"https github", "https://github.com/octo/demo.git", "github.com", "octo/demo", ProviderGitHub},
		{"https github no suffix", "https://github.com/octo/demo", "github.com", "octo/demo", ProviderGitHub},
		{"scp-like ssh", "git@github.com:octo/demo.git", "github.com", "octo/demo", ProviderGitHub},
		{"ssh url", "ssh://git@github.com/octo/demo.git", "github.com", "octo/demo", ProviderGitHub},
		{"https gitlab", "https://gitlab.com/group/project.git", "gitlab.com", "group/project", ProviderGitLab},
		{"gitlab scp", "git@gitlab.com:group/project.git", "gitlab.com", "group/project", ProviderGitLab},
		{"gitlab nested group", "https://gitlab.com/group/sub/project.git", "gitlab.com", "group/sub/project", ProviderGitLab},
		{"gitlab self hosted", "git@gitlab.example.com:group/project.git", "gitlab.example.com", "group/project", ProviderGitLab},
		{"credentials stripped", "https://user:token@github.com/octo/demo.git", "github.com", "octo/demo", ProviderGitHub},
		{"ssh with port", "ssh://git@gitlab.example.com:2222/group/project.git", "gitlab.example.com", "group/project", ProviderGitLab},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info, ok := ParseURL(test.raw)
			if !ok {
				t.Fatalf("ParseURL(%q) failed", test.raw)
			}
			if info.Host != test.wantHost {
				t.Fatalf("Host = %q, want %q", info.Host, test.wantHost)
			}
			if info.Slug != test.wantSlug {
				t.Fatalf("Slug = %q, want %q", info.Slug, test.wantSlug)
			}
			if info.Provider != test.wantForge {
				t.Fatalf("Provider = %q, want %q", info.Provider, test.wantForge)
			}
			if want := "https://" + test.wantHost + "/" + test.wantSlug; info.WebURL != want {
				t.Fatalf("WebURL = %q, want %q", info.WebURL, want)
			}
		})
	}
}

func TestParseURLRejectsUnusableInput(t *testing.T) {
	for _, raw := range []string{
		"",
		"not a url",
		"https://github.com/octo",
		"https://github.com/",
		"file:///srv/git/demo.git",
	} {
		if _, ok := ParseURL(raw); ok {
			t.Fatalf("ParseURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestURLBuilders(t *testing.T) {
	info := Info{
		Provider: ProviderGitHub,
		Host:     "github.com",
		Slug:     "octo/demo",
		WebURL:   "https://github.com/octo/demo",
	}
	if got := info.CommitURL("abc123"); got != "https://github.com/octo/demo/commit/abc123" {
		t.Fatalf("CommitURL = %q", got)
	}
	if got := info.CompareURL("v1.0.0", "v1.1.0"); got != "https://github.com/octo/demo/compare/v1.0.0...v1.1.0" {
		t.Fatalf("CompareURL = %q", got)
	}
	if got := info.CompareURL("", "v1.0.0"); got != "https://github.com/octo/demo/commits/v1.0.0" {
		t.Fatalf("CompareURL without from = %q", got)
	}
}

// GitLab project routes live under /-/; the legacy routes only redirect and are
// gone on recent versions, so release notes would link to 404 pages.
func TestGitLabLinksUseProjectRoutes(t *testing.T) {
	info := Info{
		Provider: ProviderGitLab,
		Host:     "gitlab.example.com",
		Slug:     "group/sub/app",
		WebURL:   "https://gitlab.example.com/group/sub/app",
	}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"commit", info.CommitURL("abc"), "https://gitlab.example.com/group/sub/app/-/commit/abc"},
		{"compare", info.CompareURL("2026.09.0", "2026.09.1"), "https://gitlab.example.com/group/sub/app/-/compare/2026.09.0...2026.09.1"},
		{"commits", info.CompareURL("", "2026.09.0"), "https://gitlab.example.com/group/sub/app/-/commits/2026.09.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("got %q, want %q", test.got, test.want)
			}
		})
	}
}

func TestZeroInfoProducesNoLinks(t *testing.T) {
	var info Info
	if info.Found() {
		t.Fatal("zero Info should not be Found")
	}
	if got := info.CommitURL("abc"); got != "" {
		t.Fatalf("CommitURL on zero Info = %q, want empty", got)
	}
	if got := info.CompareURL("a", "b"); got != "" {
		t.Fatalf("CompareURL on zero Info = %q, want empty", got)
	}
}

// newRepoWithRemote creates a working repository with a single remote.
func newRepoWithRemote(t *testing.T, name string, urls []string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: name, URLs: urls}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
	return dir
}

func TestDetectResolvesTheForge(t *testing.T) {
	dir := newRepoWithRemote(t, "origin", []string{"git@gitlab.com:group/subgroup/app.git"})

	info, err := Detect(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if !info.Found() {
		t.Fatal("Found() = false, want true")
	}
	if info.Slug != "group/subgroup/app" {
		t.Fatalf("Slug = %q", info.Slug)
	}
	if info.Provider != ProviderGitLab {
		t.Fatalf("Provider = %q, want gitlab", info.Provider)
	}
	if info.WebURL != "https://gitlab.com/group/subgroup/app" {
		t.Fatalf("WebURL = %q", info.WebURL)
	}
}

func TestDetectPrefersOrigin(t *testing.T) {
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "upstream", URLs: []string{"https://github.com/o/up.git"}}); err != nil {
		t.Fatalf("CreateRemote(upstream) error = %v", err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{"https://github.com/o/origin.git"}}); err != nil {
		t.Fatalf("CreateRemote(origin) error = %v", err)
	}

	info, err := Detect(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if info.Slug != "o/origin" {
		t.Fatalf("Slug = %q, want o/origin", info.Slug)
	}
}

// A remote that is not a known forge is a legitimate "cannot determine", not a
// failure: the release still works, only the links are missing.
func TestDetectOnRemoteThatIsNotAForge(t *testing.T) {
	dir := newRepoWithRemote(t, "origin", []string{"/srv/git/mirror.git"})

	info, err := Detect(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Detect() error = %v, want nil", err)
	}
	if info.Found() {
		t.Fatalf("Found() = true, want false for a local path remote: %+v", info)
	}
}

func TestDetectOnRepositoryWithoutRemotes(t *testing.T) {
	dir := t.TempDir()
	if _, err := gogit.PlainInit(dir, false); err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	info, err := Detect(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if info.Found() {
		t.Fatalf("Found() = true, want false for a repository without remotes")
	}
}

func TestPickRemote(t *testing.T) {
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "upstream", URLs: []string{"https://github.com/o/up.git"}}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
	remotes, err := Remotes(context.Background(), dir)
	if err != nil {
		t.Fatalf("Remotes() error = %v", err)
	}

	remote, err := PickRemote(remotes, "upstream")
	if err != nil {
		t.Fatalf("PickRemote() error = %v", err)
	}
	if remote.Config().Name != "upstream" {
		t.Fatalf("name = %q", remote.Config().Name)
	}

	// A single remote is used as the fallback when origin is absent.
	remote, err = PickRemote(remotes, "")
	if err != nil {
		t.Fatalf("PickRemote() error = %v", err)
	}
	if remote.Config().Name != "upstream" {
		t.Fatalf("fallback name = %q, want upstream", remote.Config().Name)
	}

	if _, err := PickRemote(remotes, "missing"); err == nil {
		t.Fatal("PickRemote() error = nil, want an error about the unknown remote")
	}
}

func clearTokenEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", JobTokenEnv} {
		t.Setenv(name, "")
	}
}

func TestResolveTokenPrefersTheExplicitToken(t *testing.T) {
	clearTokenEnv(t)
	t.Setenv("GITLAB_TOKEN", "from-env")
	if got := ResolveToken(ProviderGitLab, " explicit "); got != (Token{Value: "explicit"}) {
		t.Fatalf("ResolveToken() = %+v, want the explicit token", got)
	}
}

func TestResolveTokenReadsProviderEnvInOrder(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		env      map[string]string
		want     Token
	}{
		{"gitlab access token wins over job token", ProviderGitLab,
			map[string]string{"GITLAB_TOKEN": "pat", JobTokenEnv: "job"}, Token{Value: "pat"}},
		{"gitlab job token is marked", ProviderGitLab,
			map[string]string{JobTokenEnv: "job"}, Token{Value: "job", Job: true}},
		{"github token", ProviderGitHub,
			map[string]string{"GH_TOKEN": "gh"}, Token{Value: "gh"}},
		{"blank values are skipped", ProviderGitHub,
			map[string]string{"GITHUB_TOKEN": "  ", "GH_TOKEN": "gh"}, Token{Value: "gh"}},
		{"unknown provider has no token", ProviderAuto,
			map[string]string{"GITHUB_TOKEN": "gh"}, Token{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearTokenEnv(t)
			for name, value := range test.env {
				t.Setenv(name, value)
			}
			if got := ResolveToken(test.provider, ""); got != test.want {
				t.Fatalf("ResolveToken() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestDetectProviderFallsBackToTheCIHost(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		info Info
		want Provider
	}{
		{"host name wins", map[string]string{"CI_SERVER_HOST": "git.example.com"},
			Info{Host: "github.com", Provider: ProviderGitHub}, ProviderGitHub},
		{"gitlab ci on the same host", map[string]string{"CI_SERVER_HOST": "git.example.com"},
			Info{Host: "git.example.com"}, ProviderGitLab},
		{"github enterprise on the same host", map[string]string{"GITHUB_SERVER_URL": "https://git.example.com"},
			Info{Host: "git.example.com"}, ProviderGitHub},
		{"ci on another host", map[string]string{"CI_SERVER_HOST": "other.example.com"},
			Info{Host: "git.example.com"}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CI_SERVER_HOST", "")
			t.Setenv("GITHUB_SERVER_URL", "")
			for name, value := range test.env {
				t.Setenv(name, value)
			}
			if got := DetectProvider(test.info); got != test.want {
				t.Fatalf("DetectProvider() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCIAPIURLOnlyAppliesToTheCIHost(t *testing.T) {
	t.Setenv("CI_SERVER_HOST", "git.example.com")
	t.Setenv("CI_API_V4_URL", "https://git.example.com/gitlab/api/v4")
	t.Setenv("GITHUB_SERVER_URL", "")
	if got := CIAPIURL(Info{Provider: ProviderGitLab, Host: "git.example.com"}); got != "https://git.example.com/gitlab/api/v4" {
		t.Fatalf("CIAPIURL(same host) = %q", got)
	}
	if got := CIAPIURL(Info{Provider: ProviderGitLab, Host: "gitlab.com"}); got != "" {
		t.Fatalf("CIAPIURL(other host) = %q, want empty", got)
	}
}

func TestDefaultTokenEnvPerProvider(t *testing.T) {
	if got := DefaultTokenEnv(ProviderGitHub); got[0] != "GITHUB_TOKEN" {
		t.Fatalf("github = %v", got)
	}
	if got := DefaultTokenEnv(ProviderGitLab); got[0] != "GITLAB_TOKEN" {
		t.Fatalf("gitlab = %v", got)
	}
	if got := DefaultTokenEnv(ProviderAuto); got != nil {
		t.Fatalf("auto = %v, want nil", got)
	}
}
