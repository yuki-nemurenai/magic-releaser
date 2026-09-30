package release

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestGitCommitsSinceTag(t *testing.T) {
	dir := t.TempDir()
	repository, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}

	first := commitFile(t, worktree, dir, "one.txt", "feat: initial release")
	_, err = repository.CreateTag("v1.0.0", first, &gogit.CreateTagOptions{
		Tagger:  testSignature(),
		Message: "chore(release): v1.0.0",
	})
	if err != nil {
		t.Fatalf("CreateTag() error = %v", err)
	}
	commitFile(t, worktree, dir, "two.txt", "fix: repair parser")

	git := Git{Dir: dir}
	boundary, err := resolveBoundary(context.Background(), git, Options{
		RepoDir:      dir,
		Versioning:   VersioningSemVer,
		TagFormat:    "v{{version}}",
		CalVerFormat: DefaultCalVerFormat,
	})
	if err != nil {
		t.Fatalf("resolveBoundary() error = %v", err)
	}
	tag := boundary.Tag
	if tag.Name != "v1.0.0" {
		t.Fatalf("tag.Name = %q, want v1.0.0", tag.Name)
	}

	commits, err := git.Commits(context.Background(), tag)
	if err != nil {
		t.Fatalf("Commits() error = %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("len(commits) = %d, want 1", len(commits))
	}
	if commits[0].Description != "repair parser" {
		t.Fatalf("Description = %q, want repair parser", commits[0].Description)
	}
}

func TestLatestVersionTagSkipsInvalidVersionsForStrategy(t *testing.T) {
	dir := t.TempDir()
	repository, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	first := commitFile(t, worktree, dir, "one.txt", "feat: initial release")

	for _, tagName := range []string{"v2026.1", "v2026.08.0"} {
		_, err = repository.CreateTag(tagName, first, &gogit.CreateTagOptions{
			Tagger:  testSignature(),
			Message: "chore(release): " + tagName,
		})
		if err != nil {
			t.Fatalf("CreateTag(%q) error = %v", tagName, err)
		}
	}

	boundary, err := resolveBoundary(context.Background(), Git{Dir: dir}, Options{
		RepoDir:      dir,
		Versioning:   VersioningCalVer,
		TagFormat:    "v{{version}}",
		CalVerFormat: DefaultCalVerFormat,
	})
	if err != nil {
		t.Fatalf("resolveBoundary() error = %v", err)
	}
	version, tag := boundary.BoundaryVersion, boundary.Tag
	if version != "2026.08.0" {
		t.Fatalf("version = %q, want 2026.08.0", version)
	}
	if tag.Name != "v2026.08.0" {
		t.Fatalf("tag.Name = %q, want v2026.08.0", tag.Name)
	}
}

func TestGitDetectDotGitFromSubdirectory(t *testing.T) {
	dir := t.TempDir()
	repository, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	commitFile(t, worktree, dir, "one.txt", "feat: initial release")

	subdir := filepath.Join(dir, "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	git := Git{Dir: subdir}
	commits, err := git.Commits(context.Background(), Tag{})
	if err != nil {
		t.Fatalf("Commits() error = %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("len(commits) = %d, want 1", len(commits))
	}
}

func commitFile(t *testing.T, worktree *gogit.Worktree, dir, name, message string) plumbing.Hash {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(message), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := worktree.Add(name); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	hash, err := worktree.Commit(message, &gogit.CommitOptions{Author: testSignature()})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	return hash
}

func testSignature() *object.Signature {
	return &object.Signature{
		Name:  "Test User",
		Email: "test@example.invalid",
		When:  time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC),
	}
}
