package release

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
)

func TestRunPullRequestModeFromConfig(t *testing.T) {
	dir := t.TempDir()
	repository, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	writeFile(t, dir, "package.json", `{"name":"demo","version":"0.1.0"}`)
	writeFile(t, dir, ".magic-releaser.yaml", `mode: pull-request
versioning: calver
changelog: CHANGELOG.md
manifest: .magic-releaser-manifest.json
packages:
  - name: root
    path: .
    files:
      - type: package-json
        path: package.json
`)
	if _, err := worktree.Add("package.json"); err != nil {
		t.Fatalf("Add(package.json) error = %v", err)
	}
	if _, err := worktree.Add(".magic-releaser.yaml"); err != nil {
		t.Fatalf("Add(config) error = %v", err)
	}
	if _, err := worktree.Commit("feat: add release automation", &gogit.CommitOptions{Author: testSignature()}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	var output bytes.Buffer
	result, err := Run(context.Background(), Options{
		RepoDir:   dir,
		Now:       time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC),
		CreateTag: true,
		Output:    &output,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Released {
		t.Fatal("Released = false, want true")
	}
	if result.NextVersion != "2026.08.0" {
		t.Fatalf("NextVersion = %q, want 2026.08.0", result.NextVersion)
	}
	if result.PullRequestTitle != "chore: release 2026.08.0" {
		t.Fatalf("PullRequestTitle = %q", result.PullRequestTitle)
	}
	if !contains(result.BumpedFiles, "CHANGELOG.md") || !contains(result.BumpedFiles, "package.json") {
		t.Fatalf("BumpedFiles = %v, want changelog and package.json", result.BumpedFiles)
	}
	if !strings.Contains(output.String(), "Pull request mode: git tag was not created.") {
		t.Fatalf("output does not mention pull-request mode:\n%s", output.String())
	}

	changelog, err := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("ReadFile(CHANGELOG.md) error = %v", err)
	}
	if !strings.Contains(string(changelog), "## [2026.08.0] - 2026-08-11") {
		t.Fatalf("CHANGELOG.md was not updated:\n%s", string(changelog))
	}
}
