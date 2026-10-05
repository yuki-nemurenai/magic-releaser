package release

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
)

// commitAt writes a file, creating its directories, and commits it.
func commitAt(t *testing.T, worktree *gogit.Worktree, dir, name, message string) {
	t.Helper()
	writeFile(t, dir, name, message)
	commitExisting(t, worktree, name, message)
}

// newMonorepo creates a repository with the api, web and docs packages, each
// released once at its baseline tag.
func newMonorepo(t *testing.T) (string, *gogit.Worktree) {
	t.Helper()
	dir, worktree := newTestRepo(t)
	writeFile(t, dir, "services/api/package.json", "{\n  \"name\": \"api\",\n  \"version\": \"1.0.0\"\n}\n")
	writeFile(t, dir, "web/package.json", "{\n  \"name\": \"web\",\n  \"version\": \"0.3.0\"\n}\n")
	writeFile(t, dir, "docs/version.txt", "2.0.0\n")
	for _, name := range []string{"services/api/package.json", "web/package.json"} {
		if _, err := worktree.Add(name); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}
	commitExisting(t, worktree, "docs/version.txt", "chore: initial packages")
	head := plumbingHash(t, headHash(t, dir))
	for _, tag := range []string{"api-v1.0.0", "web-v0.3.0", "docs-v2.0.0"} {
		if err := (Git{Dir: dir}).CreateAnnotatedTagAt(context.Background(), head, tag, "baseline"); err != nil {
			t.Fatalf("CreateAnnotatedTagAt(%s) error = %v", tag, err)
		}
	}
	return dir, worktree
}

func monorepoPackages() []PackageConfig {
	return []PackageConfig{
		{Path: "services/api", Component: "api", Files: []BumpFileConfig{{Type: "node", Path: "."}}},
		{Path: "web", Component: "web", Files: []BumpFileConfig{{Type: "node", Path: "."}}},
		{Path: "docs", Component: "docs", Files: []BumpFileConfig{{Type: "simple", Path: "version.txt"}}},
	}
}

// Each component is released from the commits that touch its path, with its
// own version, tag, changelog and files, in one release commit.
func TestMonorepoReleasesTheTouchedComponents(t *testing.T) {
	dir, worktree := newMonorepo(t)
	commitAt(t, worktree, dir, "services/api/handler.go", "feat(api): add orders endpoint")
	commitAt(t, worktree, dir, "web/app.js", "fix(web): keep the session")
	commitAt(t, worktree, dir, "docs/guide.md", "docs: explain releases")
	commitAt(t, worktree, dir, "README.md", "feat: a change outside every package")

	pusher := &recordingPusher{}
	var output bytes.Buffer
	result, err := Run(context.Background(), Options{
		RepoDir:        dir,
		Versioning:     VersioningSemVer,
		Now:            releaseNow,
		CreateTag:      true,
		CreateCommit:   true,
		Push:           true,
		PushBranch:     true,
		PushBranchName: "main",
		Pusher:         pusher,
		Manifest:       ".release-manifest.json",
		Packages:       monorepoPackages(),
		Output:         &output,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := map[string]ComponentResult{
		"api":  {Component: "api", Path: "services/api", Released: true, LastVersion: "1.0.0", NextVersion: "1.1.0", TagName: "api-v1.1.0"},
		"web":  {Component: "web", Path: "web", Released: true, LastVersion: "0.3.0", NextVersion: "0.3.1", TagName: "web-v0.3.1"},
		"docs": {Component: "docs", Path: "docs", LastVersion: "2.0.0"},
	}
	for _, component := range result.Components {
		expected := want[component.Component]
		component.Notes = ""
		if component != expected {
			t.Fatalf("component %s = %+v, want %+v", component.Component, component, expected)
		}
	}
	if pusher.tag != "api-v1.1.0,web-v0.3.1" || pusher.branch != "main" || pusher.commit != result.ReleaseCommit {
		t.Fatalf("push = %+v, want both tags with the release commit", pusher)
	}

	if got := readFile(t, dir, "services/api/package.json"); !strings.Contains(got, `"version": "1.1.0"`) {
		t.Fatalf("api package.json = %s", got)
	}
	if got := readFile(t, dir, "web/package.json"); !strings.Contains(got, `"version": "0.3.1"`) {
		t.Fatalf("web package.json = %s", got)
	}
	if got := readFile(t, dir, "docs/version.txt"); got != "2.0.0\n" {
		t.Fatalf("docs was bumped: %s", got)
	}
	apiChangelog := readFile(t, dir, "services/api/CHANGELOG.md")
	if !strings.Contains(apiChangelog, "add orders endpoint") || strings.Contains(apiChangelog, "keep the session") ||
		strings.Contains(apiChangelog, "outside every package") {
		t.Fatalf("api changelog holds other commits:\n%s", apiChangelog)
	}
	if webChangelog := readFile(t, dir, "web/CHANGELOG.md"); !strings.Contains(webChangelog, "keep the session") ||
		strings.Contains(webChangelog, "orders endpoint") {
		t.Fatalf("web changelog:\n%s", webChangelog)
	}
	var manifest map[string]string
	if err := json.Unmarshal([]byte(readFile(t, dir, ".release-manifest.json")), &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if manifest["api"] != "1.1.0" || manifest["web"] != "0.3.1" || len(manifest) != 2 {
		t.Fatalf("manifest = %v", manifest)
	}

	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	commit, err := repository.CommitObject(plumbingHash(t, result.ReleaseCommit))
	if err != nil {
		t.Fatalf("CommitObject() error = %v", err)
	}
	if commit.Message != "chore(release): api-v1.1.0, web-v0.3.1" {
		t.Fatalf("release commit message = %q", commit.Message)
	}
	for _, tag := range []string{"api-v1.1.0", "web-v0.3.1"} {
		ref, err := repository.Tag(tag)
		if err != nil {
			t.Fatalf("Tag(%s) error = %v", tag, err)
		}
		object, err := repository.TagObject(ref.Hash())
		if err != nil || object.Target != commit.Hash {
			t.Fatalf("tag %s does not point at the release commit", tag)
		}
	}
	if !strings.Contains(output.String(), "Component docs (docs): no release, at 2.0.0") {
		t.Fatalf("output does not report docs:\n%s", output.String())
	}
}

// A linked group shares a version: the release of one member releases all.
func TestMonorepoLinkedVersions(t *testing.T) {
	dir, worktree := newMonorepo(t)
	commitAt(t, worktree, dir, "services/api/handler.go", "feat(api): add orders endpoint")
	result, err := Run(context.Background(), Options{
		RepoDir:        dir,
		Versioning:     VersioningSemVer,
		Now:            releaseNow,
		DryRun:         true,
		Packages:       monorepoPackages(),
		LinkedVersions: [][]string{{"api", "web"}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	versions := map[string]string{}
	for _, component := range result.Components {
		versions[component.Component] = component.TagName
	}
	if versions["api"] != "api-v1.1.0" || versions["web"] != "web-v1.1.0" || versions["docs"] != "" {
		t.Fatalf("tags = %v, want api and web at 1.1.0", versions)
	}
}

// A root package collects every commit except the nested packages it excludes.
func TestMonorepoRootPackageExcludesNestedPackages(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitAt(t, worktree, dir, "packages/lib/index.js", "feat: initial lib")
	head := plumbingHash(t, headHash(t, dir))
	for _, tag := range []string{"app-v1.0.0", "lib-v1.0.0"} {
		if err := (Git{Dir: dir}).CreateAnnotatedTagAt(context.Background(), head, tag, "baseline"); err != nil {
			t.Fatalf("CreateAnnotatedTagAt() error = %v", err)
		}
	}
	commitAt(t, worktree, dir, "packages/lib/util.js", "fix(lib): trim input")

	result, err := Run(context.Background(), Options{
		RepoDir:    dir,
		Versioning: VersioningSemVer,
		Now:        releaseNow,
		DryRun:     true,
		Packages: []PackageConfig{
			{Path: ".", Component: "app", ExcludePaths: []string{"packages"}},
			{Path: "packages/lib", Component: "lib"},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Components[0].Released || !result.Components[1].Released || result.Components[1].TagName != "lib-v1.0.1" {
		t.Fatalf("components = %+v, want only lib released", result.Components)
	}
}

// A new component starts from the commits of its path; requirePreviousRelease
// asks for a baseline tag instead.
func TestMonorepoFirstReleaseOfANewComponent(t *testing.T) {
	dir, worktree := newMonorepo(t)
	commitAt(t, worktree, dir, "worker/main.go", "feat(worker): process jobs")
	packages := append(monorepoPackages(), PackageConfig{Path: "worker", Component: "worker"})

	result, err := Run(context.Background(), Options{
		RepoDir: dir, Versioning: VersioningSemVer, Now: releaseNow, DryRun: true, Packages: packages,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Components[3].TagName != "worker-v1.0.0" {
		t.Fatalf("worker = %+v, want a first release 1.0.0", result.Components[3])
	}

	_, err = Run(context.Background(), Options{
		RepoDir: dir, Versioning: VersioningSemVer, Now: releaseNow, DryRun: true, Packages: packages,
		RequirePreviousRelease: true,
	})
	if err == nil || !strings.Contains(err.Error(), "component worker") || !strings.Contains(err.Error(), "worker-v1.0.0") {
		t.Fatalf("Run() error = %v, want a baseline hint for worker", err)
	}
}

func TestMonorepoValidatesTheComponents(t *testing.T) {
	tests := []struct {
		name      string
		packages  []PackageConfig
		tagFormat string
		linked    [][]string
		wantError string
	}{
		{"package without a component", []PackageConfig{{Path: "a", Component: "a"}, {Path: "b"}}, "", nil, "has no component"},
		{"component twice", []PackageConfig{{Path: "a", Component: "a"}, {Path: "b", Component: "a"}}, "", nil, "configured twice"},
		{"path twice", []PackageConfig{{Path: "a", Component: "a"}, {Path: "a/", Component: "b"}}, "", nil, "belongs to two"},
		{"invalid component", []PackageConfig{{Path: "a", Component: "a b"}}, "", nil, "may contain only"},
		{"tag format without the component", []PackageConfig{{Path: "a", Component: "a"}}, "v{{version}}", nil, "must contain {{component}}"},
		{"unknown linked component", []PackageConfig{{Path: "a", Component: "a"}}, "", [][]string{{"a", "z"}}, `"z", which is not a component`},
		{"component placeholder without components", []PackageConfig{{Path: "."}}, "{{component}}-v{{version}}", nil, "no package names a component"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, worktree := newTestRepo(t)
			commitFile(t, worktree, dir, "one.txt", "feat: something")
			_, err := Run(context.Background(), Options{
				RepoDir: dir, DryRun: true, Packages: test.packages, TagFormat: test.tagFormat, LinkedVersions: test.linked,
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Run() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

// Each released component gets its own forge release, and the outputs list
// them for a CI matrix.
func TestMonorepoPublishesAndReportsEachComponent(t *testing.T) {
	var titles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			titles = append(titles, body["name"].(string))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"_links":{"self":"https://gitlab.com/g/d/-/releases/` + body["tag_name"].(string) + `"}}`))
		}
	}))
	t.Cleanup(server.Close)

	dir, worktree := newMonorepo(t)
	addPlainRemote(t, dir, "git@gitlab.com:group/demo.git")
	commitAt(t, worktree, dir, "services/api/handler.go", "feat(api): add orders endpoint")
	commitAt(t, worktree, dir, "web/app.js", "fix(web): keep the session")

	result, err := Run(context.Background(), Options{
		RepoDir: dir, Versioning: VersioningSemVer, Now: releaseNow, CreateTag: true, CreateCommit: true,
		Publish: true, Token: "t", APIURL: server.URL, Packages: monorepoPackages(),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Join(titles, ",") != "Release api 1.1.0,Release web 0.3.1" {
		t.Fatalf("release titles = %v", titles)
	}

	outputs := Outputs(result, false)
	for _, want := range []string{
		`RELEASE_CREATED=true`,
		`RELEASE_COMPONENTS=["api","web"]`,
		`"api":{"version":"1.1.0","previous-version":"1.0.0","tag":"api-v1.1.0","release-url":"https://gitlab.com/g/d/-/releases/api-v1.1.0"}`,
		"RELEASE_WEB_TAG=web-v0.3.1",
		"RELEASE_DOCS_CREATED=false",
	} {
		if !strings.Contains(outputs, want) {
			t.Fatalf("outputs lack %q:\n%s", want, outputs)
		}
	}
}

// The notes of a component follow the forge, like a single release: the
// semantic-release heading on GitHub, comparing the tags of the component.
func TestMonorepoNotesFollowTheForge(t *testing.T) {
	dir, worktree := newMonorepo(t)
	addRemote(t, dir)
	commitAt(t, worktree, dir, "services/api/handler.go", "feat(api): add orders endpoint")

	result, err := Run(context.Background(), Options{
		RepoDir: dir, Versioning: VersioningSemVer, Now: releaseNow, DryRun: true, Packages: monorepoPackages(),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := "## [1.1.0](https://github.com/octo/demo/compare/api-v1.0.0...api-v1.1.0) (2026-08-11)"
	if !strings.HasPrefix(result.Components[0].Notes, want) {
		t.Fatalf("api notes =\n%s\nwant the heading %s", result.Components[0].Notes, want)
	}
}
