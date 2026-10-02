package release

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type Git struct {
	Dir string
}

type Tag struct {
	Name string
	Hash plumbing.Hash
}

// Tags returns the tags reachable from HEAD, which is what the commit boundary
// of a release is based on.
func (git Git) Tags(ctx context.Context) ([]Tag, error) {
	all, err := git.AllTags(ctx)
	if err != nil {
		return nil, err
	}
	repository, err := git.open()
	if err != nil {
		return nil, err
	}
	head, err := repository.Head()
	if err != nil {
		return nil, err
	}
	reachable, err := git.reachableCommits(ctx, repository, head.Hash())
	if err != nil {
		return nil, err
	}
	reached := make([]Tag, 0, len(all))
	for _, tag := range all {
		if reachable[tag.Hash] {
			reached = append(reached, tag)
		}
	}
	return reached, nil
}

// AllTags returns every tag in the repository, including tags that only exist
// on other branches. The next version has to be computed from the highest
// version in the whole repository, otherwise a release on one branch can end
// up reusing a tag name another branch already published.
func (git Git) AllTags(ctx context.Context) ([]Tag, error) {
	repository, err := git.open()
	if err != nil {
		return nil, err
	}

	iterator, err := repository.Tags()
	if err != nil {
		return nil, err
	}
	defer iterator.Close()

	var tags []Tag
	err = iterator.ForEach(func(reference *plumbing.Reference) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		commitHash, err := git.tagCommitHash(repository, reference.Hash())
		if err != nil {
			return nil
		}
		tags = append(tags, Tag{
			Name: reference.Name().Short(),
			Hash: commitHash,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tags, nil
}

func (git Git) Commits(ctx context.Context, sinceTag Tag) ([]Commit, error) {
	repository, err := git.open()
	if err != nil {
		return nil, err
	}
	head, err := repository.Head()
	if err != nil {
		return nil, err
	}

	excluded := map[plumbing.Hash]bool{}
	if !sinceTag.Hash.IsZero() {
		excluded, err = git.reachableCommits(ctx, repository, sinceTag.Hash)
		if err != nil {
			return nil, err
		}
	}

	iterator, err := repository.Log(&gogit.LogOptions{From: head.Hash()})
	if err != nil {
		return nil, err
	}
	defer iterator.Close()

	var commits []Commit
	err = iterator.ForEach(func(commit *object.Commit) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if excluded[commit.Hash] {
			return nil
		}
		parsed := ParseCommit(commit.Hash.String(), commit.Message)
		if commit.NumParents() > 1 {
			parsed.Merge = true
		}
		parsed.AuthorName = commit.Author.Name
		parsed.AuthorEmail = commit.Author.Email
		commits = append(commits, parsed)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return commits, nil
}

// HasTag reports whether a tag with the given name already exists.
func (git Git) HasTag(ctx context.Context, name string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	repository, err := git.open()
	if err != nil {
		return false, err
	}
	if _, err := repository.Tag(name); err == nil {
		return true, nil
	} else if !errors.Is(err, gogit.ErrTagNotFound) && !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return false, err
	}
	return false, nil
}

// IsShallow reports whether the clone has a truncated history. A shallow
// clone hides both the commits and the tags a release boundary is computed
// from, so it would quietly look like a repository that was never released.
func (git Git) IsShallow(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	repository, err := git.open()
	if err != nil {
		return false, err
	}
	shallow, err := repository.Storer.Shallow()
	if err != nil {
		return false, fmt.Errorf("read shallow commits: %w", err)
	}
	return len(shallow) > 0, nil
}

// CommitFiles stages exactly the given paths and commits them. The release
// commit must carry only what the release changed: a CI workspace often holds
// build output or caches that are not ignored, and staging everything would
// publish them to the branch.
//
// It reports false without an error when the paths carry no change, which is
// what a rerun over already bumped files looks like.
func (git Git) CommitFiles(ctx context.Context, message string, paths []string) (plumbing.Hash, bool, error) {
	if err := ctx.Err(); err != nil {
		return plumbing.ZeroHash, false, err
	}
	if len(paths) == 0 {
		return plumbing.ZeroHash, false, nil
	}
	repository, err := git.open()
	if err != nil {
		return plumbing.ZeroHash, false, err
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return plumbing.ZeroHash, false, err
	}
	for _, path := range paths {
		if _, err := worktree.Add(filepath.ToSlash(path)); err != nil {
			return plumbing.ZeroHash, false, fmt.Errorf("stage %s: %w", path, err)
		}
	}
	author, committer := git.identity(repository)
	hash, err := worktree.Commit(message, &gogit.CommitOptions{
		Author:    author,
		Committer: committer,
	})
	if errors.Is(err, gogit.ErrEmptyCommit) {
		return plumbing.ZeroHash, false, nil
	}
	if err != nil {
		return plumbing.ZeroHash, false, fmt.Errorf("create release commit: %w", err)
	}
	return hash, true, nil
}

// CurrentBranch returns the short name of the checked out branch, or an empty
// string for a detached HEAD, which is how CI runners check a commit out.
func (git Git) CurrentBranch(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	repository, err := git.open()
	if err != nil {
		return "", err
	}
	head, err := repository.Head()
	if err != nil {
		return "", fmt.Errorf("resolve head: %w", err)
	}
	if !head.Name().IsBranch() {
		return "", nil
	}
	return head.Name().Short(), nil
}

// CreateAnnotatedTagAt points an annotated tag at an explicit commit, which is
// what the release flow needs: the tag has to reference the release commit that
// carries the changelog and version bumps. A zero target means the current head.
func (git Git) CreateAnnotatedTagAt(ctx context.Context, target plumbing.Hash, tagName, message string) error {
	repo, err := git.open()
	if err != nil {
		return err
	}
	return git.createAnnotatedTagAt(ctx, repo, target, tagName, message)
}

func (git Git) createAnnotatedTagAt(ctx context.Context, repository *gogit.Repository, target plumbing.Hash, tagName, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// A zero target means "whatever the release produced nothing to commit",
	// in which case the tag belongs on the current head.
	if target.IsZero() {
		head, err := repository.Head()
		if err != nil {
			return fmt.Errorf("resolve head for tag %s: %w", tagName, err)
		}
		target = head.Hash()
	}
	if _, err := repository.Tag(tagName); err == nil {
		return fmt.Errorf("tag %s already exists", tagName)
	} else if !errors.Is(err, gogit.ErrTagNotFound) && !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("look up tag %s: %w", tagName, err)
	}

	// git tags with the committer identity.
	_, tagger := git.identity(repository)
	_, err := repository.CreateTag(tagName, target, &gogit.CreateTagOptions{
		Tagger:  tagger,
		Message: message,
	})
	if err != nil {
		// go-git has no typed "already exists" error, so the guard above is
		// the authoritative check and this only covers the write failing.
		return fmt.Errorf("create tag %s: %w", tagName, err)
	}
	return nil
}

// identity reads the author and the committer like git does: the GIT_AUTHOR_*
// and GIT_COMMITTER_* environment variables first, then user.name and
// user.email from the repository, global and system config. The variables let
// a CI job set the identity without a git executable to run git config.
//
// Unlike git, a committer left unset follows GIT_AUTHOR_*: a release has one
// identity, and a CI job sets it with two variables rather than four.
func (git Git) identity(repository *gogit.Repository) (author, committer *object.Signature) {
	name := "magic-releaser"
	email := "magic-releaser@example.invalid"
	if config, err := repository.ConfigScoped(gitconfig.SystemScope); err == nil && config != nil {
		if config.User.Name != "" {
			name = config.User.Name
		}
		if config.User.Email != "" {
			email = config.User.Email
		}
	}
	now := time.Now().UTC()
	author = &object.Signature{
		Name:  envOr("GIT_AUTHOR_NAME", name),
		Email: envOr("GIT_AUTHOR_EMAIL", email),
		When:  now,
	}
	committer = &object.Signature{
		Name:  envOr("GIT_COMMITTER_NAME", author.Name),
		Email: envOr("GIT_COMMITTER_EMAIL", author.Email),
		When:  now,
	}
	return author, committer
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func (git Git) open() (*gogit.Repository, error) {
	repository, err := gogit.PlainOpenWithOptions(git.Dir, &gogit.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, fmt.Errorf("open git repository %q: %w", git.Dir, err)
	}
	return repository, nil
}

func (git Git) tagCommitHash(repository *gogit.Repository, hash plumbing.Hash) (plumbing.Hash, error) {
	if commit, err := repository.CommitObject(hash); err == nil {
		return commit.Hash, nil
	}

	tag, err := repository.TagObject(hash)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	commit, err := tag.Commit()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return commit.Hash, nil
}

func (git Git) reachableCommits(ctx context.Context, repository *gogit.Repository, from plumbing.Hash) (map[plumbing.Hash]bool, error) {
	iterator, err := repository.Log(&gogit.LogOptions{From: from})
	if err != nil {
		if errors.Is(err, plumbing.ErrObjectNotFound) || strings.Contains(err.Error(), "object not found") {
			return map[plumbing.Hash]bool{}, nil
		}
		return nil, err
	}
	defer iterator.Close()

	reachable := map[plumbing.Hash]bool{}
	err = iterator.ForEach(func(commit *object.Commit) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		reachable[commit.Hash] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reachable, nil
}
