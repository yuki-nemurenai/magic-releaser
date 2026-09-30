package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func testSignature() *object.Signature {
	return &object.Signature{
		Name:  "Test User",
		Email: "test@example.invalid",
		When:  time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC),
	}
}

func newRepoWithFeat(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repository, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := worktree.Add("file.txt"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := worktree.Commit("feat: add a feature", &gogit.CommitOptions{Author: testSignature()}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	return dir
}

// execute runs the root command with the given arguments and captures output.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := newRootCommand()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return out.String(), err
}

func TestVersionCommandReportsBuildVersion(t *testing.T) {
	out, err := execute(t, "version")
	if err != nil {
		t.Fatalf("version error = %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("version printed nothing")
	}
}

func TestBuildVersionFallsBackToDev(t *testing.T) {
	previousVersion, previousCommit := version, commit
	t.Cleanup(func() { version, commit = previousVersion, previousCommit })

	version, commit = "", ""
	if got := buildVersion(); got != "dev" {
		t.Fatalf("buildVersion() = %q, want dev", got)
	}

	version, commit = "1.2.3", "abc1234"
	if got := buildVersion(); got != "1.2.3+abc1234" {
		t.Fatalf("buildVersion() = %q", got)
	}

	version, commit = "1.2.3", ""
	if got := buildVersion(); got != "1.2.3" {
		t.Fatalf("buildVersion() = %q", got)
	}
}

// The default changelog is applied when the flag is absent.
func TestReleaseWritesDefaultChangelog(t *testing.T) {
	dir := newRepoWithFeat(t)
	if _, err := execute(t, "release", "--repo", dir, "--versioning", "calver"); err != nil {
		t.Fatalf("release error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "CHANGELOG.md")); err != nil {
		t.Fatalf("CHANGELOG.md was not created: %v", err)
	}
}

// An explicitly empty changelog flag disables the file.
func TestReleaseChangelogEmptyFlagDisablesIt(t *testing.T) {
	dir := newRepoWithFeat(t)
	if _, err := execute(t, "release", "--repo", dir, "--versioning", "calver", "--changelog", ""); err != nil {
		t.Fatalf("release error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "CHANGELOG.md")); !os.IsNotExist(err) {
		t.Fatal("CHANGELOG.md exists although --changelog \"\" was passed")
	}
}

func TestReleaseDryRunWritesNothing(t *testing.T) {
	dir := newRepoWithFeat(t)
	out, err := execute(t, "release", "--repo", dir, "--versioning", "calver", "--dry-run")
	if err != nil {
		t.Fatalf("release error = %v", err)
	}
	if !strings.Contains(out, "Dry run") {
		t.Fatalf("output does not mention the dry run:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "CHANGELOG.md")); !os.IsNotExist(err) {
		t.Fatal("dry run created a changelog")
	}
}

// The release commit and the tag are created by default.
func TestReleaseCommitsAndTags(t *testing.T) {
	dir := newRepoWithFeat(t)
	if _, err := execute(t, "release", "--repo", dir, "--versioning", "calver"); err != nil {
		t.Fatalf("release error = %v", err)
	}
	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	if _, err := repository.Tag("v" + time.Now().UTC().Format("2006.01") + ".0"); err != nil {
		t.Fatalf("expected tag for the current month, got: %v", err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	commit, err := repository.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("CommitObject() error = %v", err)
	}
	if !strings.HasPrefix(commit.Message, "chore(release): ") {
		t.Fatalf("head commit = %q, want a release commit", commit.Message)
	}
}

func TestReleaseNoTagFlag(t *testing.T) {
	dir := newRepoWithFeat(t)
	if _, err := execute(t, "release", "--repo", dir, "--versioning", "calver", "--no-tag"); err != nil {
		t.Fatalf("release error = %v", err)
	}
	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	iterator, err := repository.Tags()
	if err != nil {
		t.Fatalf("Tags() error = %v", err)
	}
	defer iterator.Close()
	count := 0
	if err := iterator.ForEach(func(_ *plumbing.Reference) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("ForEach() error = %v", err)
	}
	if count != 0 {
		t.Fatalf("tags = %d, want 0 with --no-tag", count)
	}
}

func TestReleaseRejectsUnknownVersioning(t *testing.T) {
	dir := newRepoWithFeat(t)
	_, err := execute(t, "release", "--repo", dir, "--versioning", "nonsense")
	if err == nil {
		t.Fatal("release error = nil, want a validation error")
	}
	if !strings.Contains(err.Error(), "nonsense") {
		t.Fatalf("error should name the bad strategy, got: %v", err)
	}
}

func TestReleaseRequiresVersionPlaceholderInTagFormat(t *testing.T) {
	dir := newRepoWithFeat(t)
	_, err := execute(t, "release", "--repo", dir, "--tag-format", "release")
	if err == nil {
		t.Fatal("release error = nil, want a tag format error")
	}
}

// Publishing without a remote must fail with an explanation, not a panic.
func TestPublishWithoutRemoteFails(t *testing.T) {
	dir := newRepoWithFeat(t)
	_, err := execute(t, "release", "--repo", dir, "--versioning", "calver", "--publish")
	if err == nil {
		t.Fatal("release error = nil, want a publishing error")
	}
	if !strings.Contains(err.Error(), "remote") {
		t.Fatalf("error should mention the missing remote, got: %v", err)
	}
}

func TestPublishDryRunDoesNotCallTheAPI(t *testing.T) {
	dir := newRepoWithFeat(t)
	// A dry run returns before the publishing step, so even an unreachable API
	// URL must not produce a request.
	out, err := execute(t, "release", "--repo", dir, "--versioning", "calver",
		"--dry-run", "--publish", "--api-url", "http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("release error = %v", err)
	}
	if !strings.Contains(out, "Dry run") {
		t.Fatalf("output does not mention the dry run:\n%s", out)
	}
}

func TestHelpListsTheMainFlags(t *testing.T) {
	out, err := execute(t, "release", "--help")
	if err != nil {
		t.Fatalf("help error = %v", err)
	}
	for _, flag := range []string{"--publish", "--push", "--versioning", "--calver-format", "--dry-run", "--no-tag"} {
		if !strings.Contains(out, flag) {
			t.Fatalf("help does not document %s", flag)
		}
	}
}

// CI reads the result from a file instead of parsing the log, and the file
// is appended to because $GITHUB_OUTPUT is shared by the whole step.
func TestReleaseAppendsOutputsToTheOutputFile(t *testing.T) {
	dir := newRepoWithFeat(t)
	outputFile := filepath.Join(t.TempDir(), "outputs.env")
	if err := os.WriteFile(outputFile, []byte("EARLIER=1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := execute(t, "release", "--repo", dir, "--versioning", "calver", "--tag-format", "{{version}}",
		"--output-file", outputFile); err != nil {
		t.Fatalf("release error = %v", err)
	}
	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(data)
	for _, want := range []string{"EARLIER=1\n", "RELEASE_CREATED=true\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output file lacks %q:\n%s", want, got)
		}
	}
	// The command reads the real clock, so only the shape of the tag is checked.
	if !regexp.MustCompile(`(?m)^RELEASE_TAG=\d{4}\.\d{2}\.0$`).MatchString(got) {
		t.Fatalf("output file lacks a calver RELEASE_TAG:\n%s", got)
	}
}

func TestReleaseRejectsAnUnwritableOutputFile(t *testing.T) {
	dir := newRepoWithFeat(t)
	if _, err := execute(t, "release", "--repo", dir, "--dry-run", "--output-file", filepath.Join(dir, "missing", "out.env")); err == nil {
		t.Fatal("release error = nil, want an error about the output file")
	}
}
