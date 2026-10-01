package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/yuki-nemurenai/magic-releaser/internal/pusher"
	"github.com/yuki-nemurenai/magic-releaser/internal/repository"
)

var releaseNow = time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)

// commitExisting commits a file that was written beforehand with writeFile.
// commitFile cannot be used here because it overwrites the file content.
func commitExisting(t *testing.T, worktree *gogit.Worktree, name, message string) {
	t.Helper()
	if _, err := worktree.Add(name); err != nil {
		t.Fatalf("Add(%s) error = %v", name, err)
	}
	if _, err := worktree.Commit(message, &gogit.CommitOptions{Author: testSignature()}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
}

func newTestRepo(t *testing.T) (string, *gogit.Worktree) {
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
	return dir, worktree
}

// The documented CHANGELOG.md default has to actually apply.
func TestRunWritesChangelogByDefault(t *testing.T) {
	dir, worktree := newTestRepo(t)
	writeFile(t, dir, "package.json", `{"name":"demo","version":"0.1.0"}`)
	commitExisting(t, worktree, "package.json", "feat: add release automation")

	var output bytes.Buffer
	if _, err := Run(context.Background(), Options{
		RepoDir:          dir,
		Versioning:       VersioningCalVer,
		Now:              releaseNow,
		SkipMergeCommits: true,
		Output:           &output,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "CHANGELOG.md")); err != nil {
		t.Fatalf("CHANGELOG.md was not created by default: %v", err)
	}
}

// An explicit empty changelog still disables the feature.
func TestRunHonoursExplicitlyDisabledChangelog(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something")

	var output bytes.Buffer
	result, err := Run(context.Background(), Options{
		RepoDir:          dir,
		Versioning:       VersioningCalVer,
		Changelog:        "",
		ChangelogSet:     true,
		Now:              releaseNow,
		SkipMergeCommits: true,
		Output:           &output,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "CHANGELOG.md")); !os.IsNotExist(err) {
		t.Fatalf("CHANGELOG.md exists but changelog was disabled")
	}
	if contains(result.BumpedFiles, "CHANGELOG.md") {
		t.Fatalf("BumpedFiles = %v, must not contain CHANGELOG.md", result.BumpedFiles)
	}
}

// The tag has to point at the release commit that carries the changelog.
func TestRunTagsTheReleaseCommit(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")

	var output bytes.Buffer
	result, err := Run(context.Background(), Options{
		RepoDir:          dir,
		Versioning:       VersioningCalVer,
		Now:              releaseNow,
		SkipMergeCommits: true,
		CreateTag:        true,
		CreateCommit:     true,
		Output:           &output,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ReleaseCommit == "" {
		t.Fatal("ReleaseCommit is empty, want the release commit hash")
	}

	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	tagRef, err := repository.Tag("v2026.08.0")
	if err != nil {
		t.Fatalf("Tag(v2026.08.0) error = %v", err)
	}
	tagObject, err := repository.TagObject(tagRef.Hash())
	if err != nil {
		t.Fatalf("TagObject() error = %v", err)
	}
	taggedCommit, err := tagObject.Commit()
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if taggedCommit.Hash.String() != result.ReleaseCommit {
		t.Fatalf("tag points at %s, want release commit %s", taggedCommit.Hash, result.ReleaseCommit)
	}

	tree, err := taggedCommit.Tree()
	if err != nil {
		t.Fatalf("Tree() error = %v", err)
	}
	if _, err := tree.File("CHANGELOG.md"); err != nil {
		t.Fatalf("tagged commit does not contain CHANGELOG.md: %v", err)
	}

	worktreeStatus, err := repository.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	status, err := worktreeStatus.Status()
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.IsClean() {
		t.Fatalf("working tree is dirty after the release, changes were not committed: %v", status)
	}
}

// A release must never move the version backwards.
func TestNextCalVerNeverRollsBack(t *testing.T) {
	tests := []struct {
		name string
		last string
		now  time.Time
		want string
	}{
		{"first release", "", time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), "2026.08.0"},
		{"same month increments micro", "2026.08.3", time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), "2026.08.4"},
		{"new month resets micro", "2026.08.7", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "2026.09.0"},
		{"future tag is not rolled back", "2026.11.3", time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), "2026.11.4"},
		{"same day as previous release", "2026.08.0", time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), "2026.08.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NextCalVer(DefaultCalVerFormat, test.last, test.now)
			if err != nil {
				t.Fatalf("NextCalVer() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("NextCalVer(%q) = %q, want %q", test.last, got, test.want)
			}
		})
	}
}

// Clean calver ignores the release level by design.
func TestCleanCalVerIgnoresReleaseLevel(t *testing.T) {
	now := releaseNow
	for _, level := range []Level{ReleasePatch, ReleaseMinor, ReleaseMajor} {
		got, err := NextVersionWithCalVer(VersioningCalVer, DefaultCalVerFormat, "2026.08.0", level, now)
		if err != nil {
			t.Fatalf("NextVersion(%s) error = %v", level, err)
		}
		if got != "2026.08.1" {
			t.Fatalf("NextVersion(level=%s) = %q, want 2026.08.1 for every level", level, got)
		}
	}
}

// A strategy switch must fail instead of silently releasing all history.
func TestRunFailsWhenVersionTagsDoNotMatchStrategy(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: old work")
	commitFile(t, worktree, dir, "two.txt", "fix: newer work")

	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	if _, err := repository.CreateTag("v1.2.3", head.Hash(), &gogit.CreateTagOptions{
		Tagger:  testSignature(),
		Message: "semver release",
	}); err != nil {
		t.Fatalf("CreateTag() error = %v", err)
	}

	var output bytes.Buffer
	_, err = Run(context.Background(), Options{
		RepoDir:          dir,
		Versioning:       VersioningCalVer,
		Now:              releaseNow,
		SkipMergeCommits: true,
		Output:           &output,
	})
	if err == nil {
		t.Fatal("Run() error = nil, want an error about mismatched version tags")
	}
	if !strings.Contains(err.Error(), "v1.2.3") || !strings.Contains(err.Error(), "force-first-release") {
		t.Fatalf("error should name the tag and the escape hatch, got: %v", err)
	}
}

// The escape hatch releases the full history on purpose.
func TestRunForceFirstReleaseOverridesStrategyMismatch(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: old work")

	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	if _, err := repository.CreateTag("v1.2.3", head.Hash(), &gogit.CreateTagOptions{
		Tagger:  testSignature(),
		Message: "semver release",
	}); err != nil {
		t.Fatalf("CreateTag() error = %v", err)
	}

	var output, diagnostics bytes.Buffer
	result, err := Run(context.Background(), Options{
		RepoDir:           dir,
		Versioning:        VersioningCalVer,
		Now:               releaseNow,
		SkipMergeCommits:  true,
		ForceFirstRelease: true,
		Output:            &output,
		ErrorOutput:       &diagnostics,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.NextVersion != "2026.08.0" {
		t.Fatalf("NextVersion = %q, want 2026.08.0", result.NextVersion)
	}
	// Diagnostics must not pollute stdout, which carries the release notes.
	if strings.Contains(output.String(), "warning:") {
		t.Fatalf("warning leaked into stdout:\n%s", output.String())
	}
	if !strings.Contains(diagnostics.String(), "warning:") {
		t.Fatalf("expected a warning on the error output, got:\n%s", diagnostics.String())
	}
}

// Merge commits are noise and must not reach the notes.
func TestGenerateNotesExcludesMergeCommits(t *testing.T) {
	commits := []Commit{
		ParseCommit("aaaaaaa", "feat: real feature"),
		ParseCommit("bbbbbbb", "Merge pull request #42 from feature/x"),
	}
	commits[1].Merge = true

	excluded := GenerateNotes(NotesOptions{Version: "1.1.0", Date: releaseNow, Commits: commits, SkipMergeCommits: true})
	if strings.Contains(excluded, "Merge pull request") {
		t.Fatalf("merge commit leaked into the notes:\n%s", excluded)
	}
	if !strings.Contains(excluded, "real feature") {
		t.Fatalf("real feature missing from the notes:\n%s", excluded)
	}

	included := GenerateNotes(NotesOptions{Version: "1.1.0", Date: releaseNow, Commits: commits})
	if !strings.Contains(included, "Merge pull request") {
		t.Fatalf("merge commit should be included when the filter is off:\n%s", included)
	}
}

// Merge commits must not influence the release level either.
func TestAnalyzeCommitsIgnoresMergeCommits(t *testing.T) {
	merge := ParseCommit("bbbbbbb", "Merge pull request #42 from feat/thing")
	merge.Merge = true

	commits := []Commit{merge, ParseCommit("aaaaaaa", "fix: small repair")}

	if level := AnalyzeCommitsWithOptions(commits, true); level != ReleasePatch {
		t.Fatalf("level = %s, want patch", level)
	}
}

// The BREAKING CHANGE footer text has to survive into the notes.
func TestGenerateNotesRendersBreakingBody(t *testing.T) {
	commits := []Commit{
		ParseCommit("aaaaaaa", "feat!: drop legacy endpoint\n\nBREAKING CHANGE: /v1 was removed, use /v2 instead"),
	}
	notes := GenerateNotes(NotesOptions{Version: "2.0.0", Date: releaseNow, Commits: commits, SkipMergeCommits: true})
	if !strings.Contains(notes, "### ⚠ BREAKING CHANGES") {
		t.Fatalf("missing breaking group:\n%s", notes)
	}
	if !strings.Contains(notes, "/v1 was removed, use /v2 instead") {
		t.Fatalf("breaking change body missing from the notes:\n%s", notes)
	}
}

func TestParseCommitExtractsBreakingBody(t *testing.T) {
	commit := ParseCommit("aaaaaaa", "feat!: drop legacy\n\nBREAKING CHANGE: first line\nsecond line\n\nfooter text")
	if !commit.Breaking {
		t.Fatal("Breaking = false, want true")
	}
	if commit.BreakingBody != "first line\nsecond line" {
		t.Fatalf("BreakingBody = %q, want %q", commit.BreakingBody, "first line\nsecond line")
	}
}

// Tagging over an existing tag is refused.
func TestCreateAnnotatedTagRefusesExistingTag(t *testing.T) {
	dir, worktree := newTestRepo(t)
	first := commitFile(t, worktree, dir, "one.txt", "feat: first")

	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	if _, err := repository.CreateTag("v1.0.0", first, &gogit.CreateTagOptions{
		Tagger:  testSignature(),
		Message: "first",
	}); err != nil {
		t.Fatalf("CreateTag() error = %v", err)
	}

	git := Git{Dir: dir}
	exists, err := git.HasTag(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("HasTag() error = %v", err)
	}
	if !exists {
		t.Fatal("HasTag(v1.0.0) = false, want true")
	}

	exists, err = git.HasTag(context.Background(), "v9.9.9")
	if err != nil {
		t.Fatalf("HasTag() error = %v", err)
	}
	if exists {
		t.Fatal("HasTag(v9.9.9) = true, want false")
	}

	head, err := repository.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	if err := git.CreateAnnotatedTagAt(context.Background(), head.Hash(), "v1.0.0", "again"); err == nil {
		t.Fatal("CreateAnnotatedTagAt() on an existing tag returned nil, want an error")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error should say the tag already exists, got: %v", err)
	}
}

// recordingPusher captures what the pipeline asked to deliver.
type recordingPusher struct {
	tag    string
	branch string
	commit string
	calls  int
	err    error
}

func (p *recordingPusher) Push(ctx context.Context, request pusher.Request) error {
	p.calls++
	p.tag, p.branch, p.commit = request.Tag, request.Branch, request.Commit
	return p.err
}

// The pipeline must deliver the tag through the port, without knowing how.
func TestRunPushesTagThroughThePusherPort(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	addRemote(t, dir)

	tagPusher := &recordingPusher{}
	var output bytes.Buffer
	if _, err := Run(context.Background(), Options{
		RepoDir:          dir,
		Versioning:       VersioningCalVer,
		Now:              releaseNow,
		SkipMergeCommits: true,
		CreateTag:        true,
		CreateCommit:     true,
		Push:             true,
		Pusher:           tagPusher,
		Output:           &output,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if tagPusher.tag != "v2026.08.0" {
		t.Fatalf("pushed tag = %q, want v2026.08.0", tagPusher.tag)
	}
	if !strings.Contains(output.String(), "Pushing tag v2026.08.0") {
		t.Fatalf("output does not mention the push:\n%s", output.String())
	}
}

// Requesting a push without a configured port must fail explicitly.
func TestRunFailsWhenPushHasNoPusher(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	addRemote(t, dir)

	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		RepoDir:          dir,
		Versioning:       VersioningCalVer,
		Now:              releaseNow,
		SkipMergeCommits: true,
		CreateTag:        true,
		Push:             true,
		Output:           &output,
	})
	if err == nil {
		t.Fatal("Run() error = nil, want an error about the missing pusher")
	}
	if !strings.Contains(err.Error(), "pusher") {
		t.Fatalf("error should mention the pusher, got: %v", err)
	}
}

// A failing push must surface the transport error instead of being swallowed.
func TestRunPropagatesPushFailure(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	addRemote(t, dir)

	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		RepoDir:          dir,
		Versioning:       VersioningCalVer,
		Now:              releaseNow,
		SkipMergeCommits: true,
		CreateTag:        true,
		Push:             true,
		Pusher:           &recordingPusher{err: errors.New("remote rejected the push")},
		Output:           &output,
	})
	if err == nil {
		t.Fatal("Run() error = nil, want the push failure")
	}
	if !strings.Contains(err.Error(), "remote rejected the push") {
		t.Fatalf("error should carry the transport failure, got: %v", err)
	}
}

// addRemote attaches a remote that only has to parse as a forge URL: the
// pipeline tests never reach the network, they use a recording port.
func addRemote(t *testing.T, dir string) {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{
		Name: "origin",
		URLs: []string{"git@github.com:octo/demo.git"},
	}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
}

// Delivering a tag must not require a known forge: a mirror or a path remote
// is a perfectly valid push target and produces notes without links.
func TestRunPushesWithoutAForgeRemote(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	addPlainRemote(t, dir, "/srv/git/mirror.git")

	tagPusher := &recordingPusher{}
	var output bytes.Buffer
	result, err := Run(context.Background(), Options{
		RepoDir:          dir,
		Versioning:       VersioningCalVer,
		Now:              releaseNow,
		SkipMergeCommits: true,
		CreateTag:        true,
		CreateCommit:     true,
		Push:             true,
		Pusher:           tagPusher,
		Output:           &output,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if tagPusher.tag != "v2026.08.0" {
		t.Fatalf("pushed tag = %q, want v2026.08.0", tagPusher.tag)
	}
	// Without a forge there are no links, but the notes stay valid.
	if strings.Contains(result.Notes, "http") {
		t.Fatalf("notes should carry no links without a forge:\n%s", result.Notes)
	}
	if !strings.Contains(result.Notes, "### Features") {
		t.Fatalf("notes are missing the feature group:\n%s", result.Notes)
	}
}

// Publishing does need a forge, and must say so.
func TestRunFailsToPublishWithoutAForge(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	addPlainRemote(t, dir, "/srv/git/mirror.git")

	var output bytes.Buffer
	_, err := Run(context.Background(), Options{
		RepoDir:          dir,
		Versioning:       VersioningCalVer,
		Now:              releaseNow,
		SkipMergeCommits: true,
		CreateTag:        true,
		Publish:          true,
		Token:            "t",
		Output:           &output,
	})
	if err == nil {
		t.Fatal("Run() error = nil, want an error about the missing forge")
	}
	if !strings.Contains(err.Error(), "publish") {
		t.Fatalf("error should mention publishing, got: %v", err)
	}
}

func addPlainRemote(t *testing.T, dir, url string) {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{url}}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
}

// A tag published on another branch must still be respected when computing the
// next version, otherwise a release reuses a tag name that already exists.
func TestNextVersionSkipsTagsFromOtherBranches(t *testing.T) {
	dir := t.TempDir()
	repository, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	first := commitFile(t, worktree, dir, "one.txt", "chore: init")
	if _, err := repository.CreateTag("2026.56", first, &gogit.CreateTagOptions{
		Tagger:  testSignature(),
		Message: "release on this branch",
	}); err != nil {
		t.Fatalf("CreateTag(2026.56) error = %v", err)
	}

	// A newer tag on a commit that HEAD cannot reach stands in for a tag that
	// only exists on another branch.
	offHistory := commitFile(t, worktree, dir, "branch.txt", "feat: work on another branch")
	if _, err := repository.CreateTag("2026.73", offHistory, &gogit.CreateTagOptions{
		Tagger:  testSignature(),
		Message: "released elsewhere",
	}); err != nil {
		t.Fatalf("CreateTag(2026.73) error = %v", err)
	}
	if err := worktree.Reset(&gogit.ResetOptions{Commit: first, Mode: gogit.HardReset}); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}

	commitFile(t, worktree, dir, "two.txt", "feat: new work")

	var output bytes.Buffer
	result, err := Run(context.Background(), Options{
		RepoDir:          dir,
		Versioning:       VersioningCalVer,
		CalVerFormat:     "YYYY.PATCH",
		TagFormat:        "{{version}}",
		Changelog:        "",
		ChangelogSet:     true,
		Now:              releaseNow,
		SkipMergeCommits: true,
		Output:           &output,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// The commit range still starts after the tag reachable from HEAD.
	if result.LastVersion != "2026.56" {
		t.Fatalf("LastVersion = %q, want 2026.56", result.LastVersion)
	}
	// The version advances past the highest tag in the whole repository.
	if result.NextVersion != "2026.74" {
		t.Fatalf("NextVersion = %q, want 2026.74", result.NextVersion)
	}
	if len(result.Commits) != 1 {
		t.Fatalf("len(Commits) = %d, want 1", len(result.Commits))
	}
}

// The release commit carries only what the release wrote. A CI workspace
// holds build output that is not ignored, and it must not reach the branch.
func TestRunCommitsOnlyTheReleaseFiles(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	writeFile(t, dir, "build.log", "junk")

	result, err := Run(context.Background(), Options{
		RepoDir:      dir,
		Versioning:   VersioningCalVer,
		Now:          releaseNow,
		CreateTag:    true,
		CreateCommit: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	commit, err := repository.CommitObject(plumbingHash(t, result.ReleaseCommit))
	if err != nil {
		t.Fatalf("CommitObject() error = %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Tree() error = %v", err)
	}
	if _, err := tree.File("build.log"); err == nil {
		t.Fatal("build.log was committed with the release")
	}
	if _, err := tree.File("CHANGELOG.md"); err != nil {
		t.Fatalf("CHANGELOG.md is missing from the release commit: %v", err)
	}
}

// In CI the HEAD is detached. Without a branch name the release cannot be
// delivered, and that has to fail before any tag is written locally.
func TestRunRefusesADetachedPushWithoutABranchBeforeWriting(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	addRemote(t, dir)
	detachHead(t, dir)

	tagPusher := &recordingPusher{}
	_, err := Run(context.Background(), Options{
		RepoDir:      dir,
		Versioning:   VersioningCalVer,
		Now:          releaseNow,
		CreateTag:    true,
		CreateCommit: true,
		Push:         true,
		PushBranch:   true,
		Pusher:       tagPusher,
	})
	if err == nil || !strings.Contains(err.Error(), "--push-branch-name") {
		t.Fatalf("Run() error = %v, want a hint about --push-branch-name", err)
	}
	if exists, _ := (Git{Dir: dir}).HasTag(context.Background(), "v2026.08.0"); exists {
		t.Fatal("a local tag was created although the release could not be delivered")
	}
	if tagPusher.calls != 0 {
		t.Fatal("the pusher was called for an undeliverable release")
	}
}

// The release commit travels by hash, together with the tag, in one
// push request.
func TestRunDeliversTheReleaseCommitAndTagTogether(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	addRemote(t, dir)
	detachHead(t, dir)

	tagPusher := &recordingPusher{}
	var output bytes.Buffer
	result, err := Run(context.Background(), Options{
		RepoDir:        dir,
		Versioning:     VersioningCalVer,
		Now:            releaseNow,
		CreateTag:      true,
		CreateCommit:   true,
		Push:           true,
		PushBranch:     true,
		PushBranchName: "main",
		Pusher:         tagPusher,
		Output:         &output,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if tagPusher.calls != 1 {
		t.Fatalf("pusher calls = %d, want a single push", tagPusher.calls)
	}
	if tagPusher.branch != "main" || tagPusher.commit != result.ReleaseCommit || tagPusher.tag != "v2026.08.0" {
		t.Fatalf("push = %+v, want main <- %s with tag v2026.08.0", tagPusher, result.ReleaseCommit)
	}
	if !strings.Contains(output.String(), "to main and tag v2026.08.0") {
		t.Fatalf("output does not name the branch:\n%s", output.String())
	}
}

// A shallow clone hides tags and history; it must be an explicit error.
func TestRunRefusesAShallowClone(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	head := headHash(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".git", "shallow"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(shallow) error = %v", err)
	}

	_, err := Run(context.Background(), Options{RepoDir: dir, Versioning: VersioningCalVer, Now: releaseNow, DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "shallow") {
		t.Fatalf("Run() error = %v, want a shallow clone error", err)
	}
}

// The release commit message is configurable, with placeholders.
func TestRunUsesTheConfiguredReleaseCommitMessage(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")

	result, err := Run(context.Background(), Options{
		RepoDir:              dir,
		Versioning:           VersioningCalVer,
		Now:                  releaseNow,
		CreateCommit:         true,
		ReleaseCommitMessage: "chore(release): {{version}} [skip ci]",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	commit, err := repository.CommitObject(plumbingHash(t, result.ReleaseCommit))
	if err != nil {
		t.Fatalf("CommitObject() error = %v", err)
	}
	if commit.Message != "chore(release): 2026.08.0 [skip ci]" {
		t.Fatalf("message = %q", commit.Message)
	}
}

func headHash(t *testing.T, dir string) string {
	t.Helper()
	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	return head.Hash().String()
}

func detachHead(t *testing.T, dir string) {
	t.Helper()
	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if err := worktree.Checkout(&gogit.CheckoutOptions{Hash: plumbingHash(t, headHash(t, dir)), Force: true}); err != nil {
		t.Fatalf("Checkout(detached) error = %v", err)
	}
}

func plumbingHash(t *testing.T, hash string) plumbing.Hash {
	t.Helper()
	if !plumbing.IsHash(hash) {
		t.Fatalf("%q is not a commit hash", hash)
	}
	return plumbing.NewHash(hash)
}

// 00:30 on the 1st of October in Moscow is still September in UTC. With
// the team's timezone the release belongs to October.
func TestRunReadsTheCalVerDateInTheConfiguredTimezone(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	now := time.Date(2026, 9, 30, 21, 30, 0, 0, time.UTC)

	tests := []struct {
		timezone string
		want     string
	}{
		{"", "2026.09.0"},
		{"Europe/Moscow", "2026.10.0"},
	}
	for _, test := range tests {
		t.Run("timezone "+test.timezone, func(t *testing.T) {
			result, err := Run(context.Background(), Options{
				RepoDir:    dir,
				Versioning: VersioningCalVer,
				Timezone:   test.timezone,
				Now:        now,
				DryRun:     true,
			})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if result.NextVersion != test.want {
				t.Fatalf("NextVersion = %q, want %q", result.NextVersion, test.want)
			}
		})
	}
}

func TestRunRejectsAnUnknownTimezone(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	_, err := Run(context.Background(), Options{RepoDir: dir, Versioning: VersioningCalVer, Timezone: "Mars/Olympus", DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "Mars/Olympus") {
		t.Fatalf("Run() error = %v, want an unknown timezone error", err)
	}
}

// A switch away from prefixed or pre-release tags must still be noticed.
func TestVersionLikeTagPattern(t *testing.T) {
	tests := []struct {
		tag  string
		want bool
	}{
		{"2026.73", true},
		{"v1.2.3", true},
		{"v1.2.3-rc.1", true},
		{"app-2026.73", true},
		{"app/v1.2.3", true},
		{"pkg@1.2.3", true},
		{"release-candidate", false},
		{"v1", false},
		{"latest", false},
	}
	for _, test := range tests {
		t.Run(test.tag, func(t *testing.T) {
			if got := versionLikeTagPattern.MatchString(test.tag); got != test.want {
				t.Fatalf("match(%q) = %v, want %v", test.tag, got, test.want)
			}
		})
	}
}

// CI sets the identity with git config --global; the release commit has
// to carry it.
func TestRunSignsWithTheGlobalGitIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	writeFile(t, home, ".gitconfig", "[user]\n\tname = Global CI\n\temail = ci@example.invalid\n")
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")

	result, err := Run(context.Background(), Options{RepoDir: dir, Versioning: VersioningCalVer, Now: releaseNow, CreateCommit: true})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	repository, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	commit, err := repository.CommitObject(plumbingHash(t, result.ReleaseCommit))
	if err != nil {
		t.Fatalf("CommitObject() error = %v", err)
	}
	if commit.Author.Name != "Global CI" || commit.Author.Email != "ci@example.invalid" {
		t.Fatalf("author = %s <%s>, want the global identity", commit.Author.Name, commit.Author.Email)
	}
}

// Each forge gets its own convention: the semantic-release layout on GitHub,
// titled by the tag, and the Keep a Changelog layout on GitLab, titled
// "Release <version>". A configured style or title wins over the forge.
func TestRunFollowsTheReleaseConventionOfTheForge(t *testing.T) {
	tests := []struct {
		name        string
		remote      string
		style       NotesStyle
		template    string
		wantTitle   string
		wantHeading string
	}{
		{"github", "git@github.com:octo/demo.git", "", "",
			"v2026.08.1", "## [2026.08.1](https://github.com/octo/demo/compare/v2026.08.0...v2026.08.1) (2026-08-11)\n"},
		{"gitlab", "git@gitlab.com:group/demo.git", "", "",
			"Release 2026.08.1", "## [2026.08.1] - 2026-08-11\n\nFull changelog: https://gitlab.com/group/demo/-/compare/v2026.08.0...v2026.08.1\n"},
		{"style from the config", "git@github.com:octo/demo.git", NotesStyleKeepAChangelog, "",
			"Release 2026.08.1", "## [2026.08.1] - 2026-08-11\n"},
		{"title template", "git@gitlab.com:group/demo.git", "", "{{tag}} ({{version}})",
			"v2026.08.1 (2026.08.1)", "## [2026.08.1] - 2026-08-11\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var title string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/releases"):
					_, _ = w.Write([]byte(`[]`))
				case r.Method == http.MethodGet:
					w.WriteHeader(http.StatusNotFound)
				case r.Method == http.MethodPost:
					var body struct {
						Name string `json:"name"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					title = body.Name
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"id":1}`))
				}
			}))
			t.Cleanup(server.Close)

			dir, worktree := newTestRepo(t)
			commitFile(t, worktree, dir, "one.txt", "feat: first")
			if err := (Git{Dir: dir}).CreateAnnotatedTagAt(context.Background(), plumbingHash(t, headHash(t, dir)), "v2026.08.0", "first"); err != nil {
				t.Fatalf("CreateAnnotatedTagAt() error = %v", err)
			}
			commitFile(t, worktree, dir, "two.txt", "fix: second")
			addPlainRemote(t, dir, test.remote)
			var output bytes.Buffer
			result, err := Run(context.Background(), Options{
				RepoDir:      dir,
				Versioning:   VersioningCalVer,
				Now:          releaseNow,
				CreateTag:    true,
				CreateCommit: true,
				Publish:      true,
				Token:        "t",
				APIURL:       server.URL,
				ReleaseName:  test.template,
				Notes:        NotesConfig{Style: test.style},
				Output:       &output,
			})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if title != test.wantTitle {
				t.Fatalf("release title = %q, want %q", title, test.wantTitle)
			}
			if !strings.HasPrefix(result.Notes, test.wantHeading) {
				t.Fatalf("notes start with\n%s\nwant\n%s", result.Notes, test.wantHeading)
			}
			if !strings.Contains(output.String(), "Release name: "+test.wantTitle) {
				t.Fatalf("output does not show the release name:\n%s", output.String())
			}
		})
	}
}

// The first release has nothing to compare against, so the GitHub heading
// carries no link, as semantic-release renders it.
func TestConventionalChangelogHeadingOfTheFirstRelease(t *testing.T) {
	notes := GenerateNotes(NotesOptions{
		Version:    "1.0.0",
		Date:       releaseNow,
		Style:      NotesStyleConventionalChangelog,
		CompareTo:  "v1.0.0",
		Repository: repository.Info{Provider: repository.ProviderGitHub, Host: "github.com", Slug: "o/d", WebURL: "https://github.com/o/d"},
		Commits:    []Commit{ParseCommit("a1", "feat: first")},
	})
	if !strings.HasPrefix(notes, "## 1.0.0 (2026-08-11)\n\n### Features") {
		t.Fatalf("notes =\n%s", notes)
	}
}

func TestRunRejectsAnUnknownNotesStyle(t *testing.T) {
	dir, worktree := newTestRepo(t)
	commitFile(t, worktree, dir, "one.txt", "feat: something new")
	_, err := Run(context.Background(), Options{RepoDir: dir, DryRun: true, Notes: NotesConfig{Style: "fancy"}})
	if err == nil || !strings.Contains(err.Error(), "fancy") {
		t.Fatalf("Run() error = %v, want an unknown style error", err)
	}
}

func TestConfigSetsTheReleaseName(t *testing.T) {
	options := MergeConfig(Options{}, Config{ReleaseName: "Build {{version}}"})
	if options.ReleaseName != "Build {{version}}" {
		t.Fatalf("ReleaseName = %q", options.ReleaseName)
	}
	if options := MergeConfig(Options{ReleaseName: "flag"}, Config{ReleaseName: "config"}); options.ReleaseName != "flag" {
		t.Fatalf("ReleaseName = %q, want the flag to win", options.ReleaseName)
	}
}
