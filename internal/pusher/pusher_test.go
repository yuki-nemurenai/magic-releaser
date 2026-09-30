package pusher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/yuki-nemurenai/magic-releaser/internal/repository"
)

// newRepoWithBareRemote creates a working repository whose origin is a bare
// repository on disk, which is a real push target without a network.
func newRepoWithBareRemote(t *testing.T) (work, bare string) {
	t.Helper()
	bare = filepath.Join(t.TempDir(), "remote.git")
	if _, err := gogit.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit(bare) error = %v", err)
	}
	work = t.TempDir()
	repo, err := gogit.PlainInit(work, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}
	signature := &object.Signature{Name: "Test", Email: "test@example.invalid", When: time.Now().UTC()}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := worktree.Add("file.txt"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := worktree.Commit("feat: first", &gogit.CommitOptions{Author: signature}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	return work, bare
}

func tagRefs(t *testing.T, dir string) map[string]bool {
	t.Helper()
	repo, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("PlainOpen(%s) error = %v", dir, err)
	}
	tags := map[string]bool{}
	iter, err := repo.Tags()
	if err != nil {
		t.Fatalf("Tags() error = %v", err)
	}
	defer iter.Close()
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		tags[ref.Name().Short()] = true
		return nil
	}); err != nil {
		t.Fatalf("ForEach() error = %v", err)
	}
	return tags
}

// commitRelease adds a release commit on top of HEAD and returns its hash.
func commitRelease(t *testing.T, work string) plumbing.Hash {
	t.Helper()
	repo, err := gogit.PlainOpen(work)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "file.txt"), []byte("released"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := worktree.Add("file.txt"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	signature := &object.Signature{Name: "Test", Email: "t@e.invalid", When: time.Now().UTC()}
	hash, err := worktree.Commit("chore(release): 2026.09.1", &gogit.CommitOptions{Author: signature})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	return hash
}

func createTag(t *testing.T, work, name string, target plumbing.Hash) {
	t.Helper()
	repo, err := gogit.PlainOpen(work)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	if _, err := repo.CreateTag(name, target, &gogit.CreateTagOptions{
		Tagger:  &object.Signature{Name: "Test", Email: "t@e.invalid", When: time.Now().UTC()},
		Message: "release",
	}); err != nil {
		t.Fatalf("CreateTag() error = %v", err)
	}
}

// pushInitialBranch publishes the first commit as main, like a real remote
// that already carries the branch being released.
func pushInitialBranch(t *testing.T, work string) {
	t.Helper()
	repo, err := gogit.PlainOpen(work)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	if err := repo.Push(&gogit.PushOptions{RefSpecs: []gitconfig.RefSpec{
		gitconfig.RefSpec(head.Hash().String() + ":refs/heads/main"),
	}}); err != nil {
		t.Fatalf("Push(initial) error = %v", err)
	}
}

// detach checks HEAD out as a bare commit and deletes every local branch,
// which is exactly the state a GitLab runner leaves the clone in.
func detach(t *testing.T, work string) {
	t.Helper()
	repo, err := gogit.PlainOpen(work)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if err := worktree.Checkout(&gogit.CheckoutOptions{Hash: head.Hash(), Force: true}); err != nil {
		t.Fatalf("Checkout(detached) error = %v", err)
	}
	branches, err := repo.Branches()
	if err != nil {
		t.Fatalf("Branches() error = %v", err)
	}
	var names []plumbing.ReferenceName
	_ = branches.ForEach(func(ref *plumbing.Reference) error {
		names = append(names, ref.Name())
		return nil
	})
	for _, name := range names {
		if err := repo.Storer.RemoveReference(name); err != nil {
			t.Fatalf("RemoveReference(%s) error = %v", name, err)
		}
	}
}

func remoteRef(t *testing.T, bare string, name plumbing.ReferenceName) (plumbing.Hash, bool) {
	t.Helper()
	repo, err := gogit.PlainOpen(bare)
	if err != nil {
		t.Fatalf("PlainOpen(bare) error = %v", err)
	}
	ref, err := repo.Reference(name, true)
	if err != nil {
		return plumbing.ZeroHash, false
	}
	return ref.Hash(), true
}

func TestPushDeliversATag(t *testing.T) {
	work, bare := newRepoWithBareRemote(t)
	repo, err := gogit.PlainOpen(work)
	if err != nil {
		t.Fatalf("PlainOpen() error = %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	createTag(t, work, "v1.0.0", head.Hash())

	tagPusher, err := New(context.Background(), work, "", "", "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := tagPusher.Push(context.Background(), Request{Tag: "v1.0.0"}); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if !tagRefs(t, bare)["v1.0.0"] {
		t.Fatalf("tag v1.0.0 is missing on the remote, got %v", tagRefs(t, bare))
	}
}

// A CI runner has a detached HEAD and no local branch. The release commit
// still has to land on the branch, and a push that delivers nothing must never
// be reported as a success.
func TestPushDeliversTheReleaseCommitFromADetachedHead(t *testing.T) {
	work, bare := newRepoWithBareRemote(t)
	pushInitialBranch(t, work)
	detach(t, work)
	release := commitRelease(t, work)
	createTag(t, work, "2026.09.1", release)

	tagPusher, err := New(context.Background(), work, "", "", "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := tagPusher.Push(context.Background(), Request{
		Branch: "main",
		Commit: release.String(),
		Tag:    "2026.09.1",
	}); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if got, _ := remoteRef(t, bare, plumbing.NewBranchReferenceName("main")); got != release {
		t.Fatalf("remote main = %s, want the release commit %s", got, release)
	}
	if !tagRefs(t, bare)["2026.09.1"] {
		t.Fatal("the tag was not delivered")
	}
}

// When the branch update is rejected, as a protected branch does, the tag
// must not reach the remote either, otherwise it points at a commit that the
// branch never received and the next run releases the same commits again.
func TestPushIsAtomicWhenTheBranchIsRejected(t *testing.T) {
	work, bare := newRepoWithBareRemote(t)
	pushInitialBranch(t, work)
	hook := "#!/bin/sh\n[ \"$1\" = refs/heads/main ] && { echo protected >&2; exit 1; }\nexit 0\n"
	if err := os.MkdirAll(filepath.Join(bare, "hooks"), 0o755); err != nil {
		t.Fatalf("MkdirAll(hooks) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(bare, "hooks", "update"), []byte(hook), 0o755); err != nil {
		t.Fatalf("WriteFile(hook) error = %v", err)
	}
	release := commitRelease(t, work)
	createTag(t, work, "2026.09.1", release)

	tagPusher, err := New(context.Background(), work, "", "", "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = tagPusher.Push(context.Background(), Request{Branch: "main", Commit: release.String(), Tag: "2026.09.1"})
	if err == nil {
		t.Fatal("Push() error = nil, want the rejected branch update")
	}
	if tagRefs(t, bare)["2026.09.1"] {
		t.Fatal("the tag reached the remote although the branch was rejected")
	}
}

func TestPushRejectsAnInvalidRequest(t *testing.T) {
	work, _ := newRepoWithBareRemote(t)
	tagPusher, err := New(context.Background(), work, "", "", "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	hash := strings.Repeat("a", 40)
	tests := []struct {
		name    string
		request Request
	}{
		{"branch starts with a dash", Request{Branch: "-bad", Commit: hash}},
		{"branch starts with a slash", Request{Branch: "/bad", Commit: hash}},
		{"branch with a space", Request{Branch: "bad name", Commit: hash}},
		{"branch with a tilde", Request{Branch: "bad~name", Commit: hash}},
		{"branch without a commit", Request{Branch: "main"}},
		{"commit without a branch", Request{Commit: hash}},
		{"abbreviated commit", Request{Branch: "main", Commit: "abc1234"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := tagPusher.Push(context.Background(), test.request); err == nil {
				t.Fatalf("Push(%+v) error = nil, want a validation error", test.request)
			}
		})
	}
}

func TestPushWithAnEmptyRequestDoesNothing(t *testing.T) {
	work, _ := newRepoWithBareRemote(t)
	tagPusher, err := New(context.Background(), work, "", "", "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := tagPusher.Push(context.Background(), Request{Tag: "  "}); err != nil {
		t.Fatalf("Push() error = %v, want nil for nothing to deliver", err)
	}
}

func TestNewFailsWithoutRemote(t *testing.T) {
	dir := t.TempDir()
	if _, err := gogit.PlainInit(dir, false); err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	_, err := New(context.Background(), dir, "", "", "")
	if err == nil {
		t.Fatal("New() error = nil, want an error about the missing remote")
	}
	if !strings.Contains(err.Error(), "remote") {
		t.Fatalf("error should mention the remote, got: %v", err)
	}
}

func TestNewFailsForUnknownRemoteName(t *testing.T) {
	work, _ := newRepoWithBareRemote(t)
	_, err := New(context.Background(), work, "upstream", "", "")
	if err == nil {
		t.Fatal("New() error = nil, want an error about the unknown remote")
	}
	if !strings.Contains(err.Error(), "upstream") {
		t.Fatalf("error should name the remote, got: %v", err)
	}
}

// A remote section without a url key must produce an error, not a panic.
// go-git refuses to create such a remote, so the config is written by hand,
// which is exactly how it looks after a manual edit.
func TestNewFailsWhenRemoteHasNoURL(t *testing.T) {
	work := t.TempDir()
	if _, err := gogit.PlainInit(work, false); err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	dotGit := filepath.Join(work, ".git")
	config := "[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	if err := os.WriteFile(filepath.Join(dotGit, "config"), []byte(config), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := New(context.Background(), work, "", "", ""); err == nil {
		t.Fatal("New() error = nil, want an error about the missing URL")
	}
}

// A GitLab job token is only accepted for the gitlab-ci-token user.
func TestAuthUsesTheJobTokenUser(t *testing.T) {
	tagPusher := &Pusher{provider: repository.ProviderGitLab, token: repository.Token{Value: "job", Job: true}}
	auth, err := tagPusher.auth("https://gitlab.example.com/g/p.git")
	if err != nil {
		t.Fatalf("auth() error = %v", err)
	}
	basic, ok := auth.(*githttp.BasicAuth)
	if !ok || basic.Username != "gitlab-ci-token" || basic.Password != "job" {
		t.Fatalf("auth = %+v, want gitlab-ci-token with the job token", auth)
	}
}

func TestAuthUsesForgeCredentials(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		url      string
		token    string
		wantAuth bool
		wantUser string
		wantErr  bool
	}{
		{"gitlab", "gitlab", "https://gitlab.com/g/p.git", "tok", true, "oauth2", false},
		{"github", "github", "https://github.com/o/d.git", "tok", true, "x-access-token", false},
		{"unknown provider", "", "https://git.example.com/o/d.git", "tok", false, "", true},
		{"no token", "github", "https://github.com/o/d.git", "", false, "", false},
		{"ssh remote", "github", "git@github.com:o/d.git", "tok", false, "", false},
		{"empty url", "github", "", "tok", false, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tagPusher := &Pusher{provider: repository.Provider(test.provider), token: repository.Token{Value: test.token}}
			auth, err := tagPusher.auth(test.url)
			if test.wantErr {
				if err == nil {
					t.Fatal("auth() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("auth() error = %v", err)
			}
			if !test.wantAuth {
				if auth != nil {
					t.Fatalf("auth = %v, want nil", auth)
				}
				return
			}
			basic, ok := auth.(*githttp.BasicAuth)
			if !ok {
				t.Fatalf("auth type = %T, want *githttp.BasicAuth", auth)
			}
			if basic.Username != test.wantUser {
				t.Fatalf("username = %q, want %q", basic.Username, test.wantUser)
			}
			if basic.Password != "tok" {
				t.Fatalf("password = %q, want tok", basic.Password)
			}
		})
	}
}
