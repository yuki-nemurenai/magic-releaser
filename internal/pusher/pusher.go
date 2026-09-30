// Package pusher delivers a release to a git remote.
//
// It is the only place in the project that combines a forge identity with
// credentials: the core decides what to deliver and never learns which
// provider it is talking to or which token authenticates it.
package pusher

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/yuki-nemurenai/magic-releaser/internal/repository"
)

// Request describes what a release puts on the remote. Either part may be
// empty: a release without a commit only pushes its tag, and a release without
// a tag only pushes its commit.
type Request struct {
	// Branch receives Commit. Both are set together or not at all.
	Branch string
	// Commit is the full hash of the release commit. It is pushed by hash
	// rather than by a local branch name, because a CI runner checks the
	// commit out on a detached HEAD and has no local branch to push from.
	Commit string
	// Tag is the name of an existing local tag.
	Tag string
}

// Pusher pushes to a single remote of a local repository.
type Pusher struct {
	repo     string
	remote   string
	provider repository.Provider
	token    repository.Token
}

// New prepares a pusher for the named remote of the repository in dir.
// An empty token is resolved from the provider token environment variables.
// An empty remoteName selects origin.
func New(ctx context.Context, dir, remoteName, providerName, token string) (*Pusher, error) {
	remotes, err := repository.Remotes(ctx, dir)
	if err != nil {
		return nil, err
	}
	if len(remotes) == 0 {
		return nil, fmt.Errorf("cannot push: the repository in %q has no remote", dir)
	}
	remote, err := repository.PickRemote(remotes, remoteName)
	if err != nil {
		return nil, err
	}
	if len(remote.Config().URLs) == 0 {
		return nil, fmt.Errorf("cannot push: remote %q has no URL configured", remote.Config().Name)
	}

	provider := providerFor(remote, providerName)
	return &Pusher{
		repo:     dir,
		remote:   remote.Config().Name,
		provider: provider,
		token:    repository.ResolveToken(provider, token),
	}, nil
}

func providerFor(remote *gogit.Remote, providerName string) repository.Provider {
	if providerName != "" {
		return repository.Provider(providerName)
	}
	info, ok := repository.ParseURL(remote.Config().URLs[0])
	if !ok {
		return repository.ProviderAuto
	}
	return repository.DetectProvider(info)
}

// Push delivers the release commit and the tag in a single push.
//
// The push is atomic when the server supports it, which GitHub, GitLab and
// git itself do: a rejected branch update, for example on a protected branch or
// after a concurrent push, then also rejects the tag. Otherwise the remote
// would carry a tag on a commit its branch never received, and the next run
// would release the same commits again.
func (p *Pusher) Push(ctx context.Context, request Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	refSpecs, err := request.refSpecs()
	if err != nil {
		return err
	}
	if len(refSpecs) == 0 {
		return nil
	}

	repo, err := gogit.PlainOpenWithOptions(p.repo, &gogit.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return fmt.Errorf("open git repository %q: %w", p.repo, err)
	}
	remotes, err := repo.Remotes()
	if err != nil {
		return fmt.Errorf("read remotes: %w", err)
	}
	remote, err := repository.PickRemote(remotes, p.remote)
	if err != nil {
		return err
	}
	auth, err := p.auth(remote.Config().URLs[0])
	if err != nil {
		return err
	}

	err = remote.PushContext(ctx, &gogit.PushOptions{
		RemoteName: remote.Config().Name,
		RefSpecs:   refSpecs,
		Auth:       auth,
		Atomic:     true,
	})
	// The remote already carries everything, which is what a rerun looks like
	// and not a failure: every ref is named explicitly, so up to date cannot
	// mean that nothing matched.
	if errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("push %s to %s: %w", request.describe(), remote.Config().Name, err)
	}
	return nil
}

func (request Request) refSpecs() ([]config.RefSpec, error) {
	var specs []config.RefSpec
	if request.Branch != "" || request.Commit != "" {
		if !isValidBranchName(request.Branch) {
			return nil, fmt.Errorf("%q is not a valid branch name", request.Branch)
		}
		if !plumbing.IsHash(request.Commit) {
			return nil, fmt.Errorf("%q is not a full commit hash", request.Commit)
		}
		specs = append(specs, config.RefSpec(request.Commit+":refs/heads/"+request.Branch))
	}
	if tag := strings.TrimSpace(request.Tag); tag != "" {
		specs = append(specs, config.RefSpec("refs/tags/"+tag+":refs/tags/"+tag))
	}
	for _, spec := range specs {
		if err := spec.Validate(); err != nil {
			return nil, fmt.Errorf("refspec %s: %w", spec, err)
		}
	}
	return specs, nil
}

func (request Request) describe() string {
	switch {
	case request.Branch != "" && request.Tag != "":
		return fmt.Sprintf("branch %s and tag %s", request.Branch, request.Tag)
	case request.Branch != "":
		return "branch " + request.Branch
	default:
		return "tag " + request.Tag
	}
}

func isValidBranchName(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") {
		return false
	}
	return !strings.ContainsAny(name, " ~^:?*[\\")
}

// auth builds the credentials for a remote URL. Only http remotes can carry a
// token: ssh remotes rely on the ambient agent and are pushed without it.
func (p *Pusher) auth(remoteURL string) (transport.AuthMethod, error) {
	if p.token.Value == "" || remoteURL == "" {
		return nil, nil
	}
	if !strings.HasPrefix(remoteURL, "http://") && !strings.HasPrefix(remoteURL, "https://") {
		return nil, nil
	}
	var username string
	switch {
	case p.provider == repository.ProviderGitHub:
		username = "x-access-token"
	case p.provider == repository.ProviderGitLab && p.token.Job:
		// A job token is only accepted for this user name, and only when the
		// project allows job tokens to push.
		username = "gitlab-ci-token"
	case p.provider == repository.ProviderGitLab:
		username = "oauth2"
	default:
		return nil, fmt.Errorf("cannot authenticate for %s: pass --provider github|gitlab", p.provider)
	}
	return &githttp.BasicAuth{Username: username, Password: p.token.Value}, nil
}
