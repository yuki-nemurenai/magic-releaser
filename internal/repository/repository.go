// Package repository resolves the forge a release belongs to from a local git
// checkout. The same resolution feeds the links in the release notes, the
// choice of publishing provider and the credentials used to push a tag, so the
// forge knowledge lives in exactly one place.
package repository

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	gogit "github.com/go-git/go-git/v5"
)

type Provider string

const (
	ProviderAuto   Provider = "auto"
	ProviderGitHub Provider = "github"
	ProviderGitLab Provider = "gitlab"
)

// Info describes the remote repository a release is published to.
// The zero value means "could not be determined", which callers must handle:
// release notes stay valid without it, only the links disappear.
type Info struct {
	Provider Provider
	Host     string
	Slug     string
	WebURL   string
}

func (info Info) Found() bool { return info.Slug != "" && info.Host != "" }

// CommitURL returns the web URL of a commit.
func (info Info) CommitURL(hash string) string {
	if !info.Found() {
		return ""
	}
	return info.WebURL + info.routePrefix() + "/commit/" + hash
}

// CompareURL returns the web URL comparing two revisions.
func (info Info) CompareURL(from, to string) string {
	if !info.Found() {
		return ""
	}
	if from == "" {
		return info.CompareToURL(to)
	}
	return info.WebURL + info.routePrefix() + "/compare/" + from + "..." + to
}

// CompareToURL returns the web URL listing the commits of a single revision.
func (info Info) CompareToURL(to string) string {
	if !info.Found() {
		return ""
	}
	return info.WebURL + info.routePrefix() + "/commits/" + to
}

// routePrefix separates project routes from the project path on GitLab. The
// legacy routes without it only redirect, and are gone on recent versions.
func (info Info) routePrefix() string {
	if info.Provider == ProviderGitLab {
		return "/-"
	}
	return ""
}

// JobTokenEnv is the GitLab CI job token. It is scoped to the running job: it
// authenticates with a JOB-TOKEN header and the gitlab-ci-token user rather
// than as a bearer token, and by default it cannot push.
const JobTokenEnv = "CI_JOB_TOKEN"

// DefaultTokenEnv lists the environment variables checked for a token, in
// priority order, per provider. The list is forge knowledge, so it lives here
// rather than being duplicated by every caller.
func DefaultTokenEnv(provider Provider) []string {
	switch provider {
	case ProviderGitHub:
		return []string{"GITHUB_TOKEN", "GH_TOKEN"}
	case ProviderGitLab:
		return []string{"GITLAB_TOKEN", JobTokenEnv}
	default:
		return nil
	}
}

// Token is a forge credential together with the way the forge expects it.
// The zero value means no token.
type Token struct {
	Value string
	// Job marks a GitLab CI job token, which is sent differently from a
	// personal, project or group access token.
	Job bool
}

// ResolveToken picks the credential for a provider: an explicit token wins,
// otherwise the provider environment variables are read in priority order.
// Publishing and pushing both resolve through here, so they can never end up
// authenticating with different tokens.
func ResolveToken(provider Provider, explicit string) Token {
	if value := strings.TrimSpace(explicit); value != "" {
		return Token{Value: value}
	}
	for _, name := range DefaultTokenEnv(provider) {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return Token{Value: value, Job: name == JobTokenEnv}
		}
	}
	return Token{}
}

var scpLikeURL = regexp.MustCompile(`^(?:([^@/]+)@)?([^:/]+):(.+)$`)

// ParseURL extracts the forge coordinates from a git remote URL. It accepts
// scp-like syntax, ssh://, git:// and http(s)://, with or without credentials.
func ParseURL(raw string) (Info, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Info{}, false
	}

	var host, path string
	if match := scpLikeURL.FindStringSubmatch(raw); match != nil && !strings.Contains(raw, "://") {
		host, path = match[2], match[3]
	} else {
		parsed, err := url.Parse(raw)
		if err != nil {
			return Info{}, false
		}
		host = parsed.Hostname()
		path = strings.TrimPrefix(parsed.Path, "/")
	}
	if host == "" {
		return Info{}, false
	}

	// GitLab allows nested groups, so everything but the last segment is the
	// namespace. GitHub only ever has a single owner segment.
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		return Info{}, false
	}
	name := segments[len(segments)-1]
	namespace := strings.Join(segments[:len(segments)-1], "/")
	if name == "" || namespace == "" {
		return Info{}, false
	}

	return Info{
		Host:     host,
		Slug:     namespace + "/" + name,
		WebURL:   "https://" + host + "/" + namespace + "/" + name,
		Provider: guessProvider(host),
	}, true
}

func guessProvider(host string) Provider {
	host = strings.ToLower(host)
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		return ProviderGitHub
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com") || strings.Contains(host, "gitlab"):
		return ProviderGitLab
	default:
		return ""
	}
}

// Detect reads the remote of a local repository. The named remote is preferred,
// otherwise the first configured remote is used, preferring "origin".
//
// A remote that cannot be mapped to a known forge is not an error: it yields a
// zero Info, and callers that need a forge (publishing) check Found() and report
// the missing coordinates themselves. Release notes stay valid without links.
func Detect(ctx context.Context, dir, remoteName string) (Info, error) {
	remotes, err := Remotes(ctx, dir)
	if err != nil {
		return Info{}, err
	}
	if len(remotes) == 0 {
		return Info{}, nil
	}
	remote, err := PickRemote(remotes, remoteName)
	if err != nil {
		return Info{}, err
	}
	info, ok := infoFromRemote(remote)
	if !ok {
		return Info{}, nil
	}
	info.Provider = DetectProvider(info)
	return info, nil
}

// DetectProvider returns the provider of a repository. A host name tells it
// for the public forges and for most self-hosted GitLab instances; otherwise a
// CI job running on that very host says which forge it is.
func DetectProvider(info Info) Provider {
	if info.Provider != "" && info.Provider != ProviderAuto {
		return info.Provider
	}
	switch {
	case sameHost(os.Getenv("CI_SERVER_HOST"), info.Host):
		return ProviderGitLab
	case sameHost(hostOf(os.Getenv("GITHUB_SERVER_URL")), info.Host):
		return ProviderGitHub
	default:
		return info.Provider
	}
}

// CIAPIURL returns the API root that a CI job running on the repository host
// advertises, or an empty string outside such a job. It is exact where a guess
// from the host name is not: a GitLab installed under a relative URL, or a
// GitHub Enterprise Server.
func CIAPIURL(info Info) string {
	switch info.Provider {
	case ProviderGitLab:
		if sameHost(os.Getenv("CI_SERVER_HOST"), info.Host) {
			return os.Getenv("CI_API_V4_URL")
		}
	case ProviderGitHub:
		if sameHost(hostOf(os.Getenv("GITHUB_SERVER_URL")), info.Host) {
			return os.Getenv("GITHUB_API_URL")
		}
	}
	return ""
}

func sameHost(a, b string) bool {
	return a != "" && strings.EqualFold(a, b)
}

func hostOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// Remotes opens a repository and returns its configured remotes.
func Remotes(ctx context.Context, dir string) ([]*gogit.Remote, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, err := gogit.PlainOpenWithOptions(dir, &gogit.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, fmt.Errorf("open git repository %q: %w", dir, err)
	}
	remotes, err := repo.Remotes()
	if err != nil {
		return nil, fmt.Errorf("read remotes: %w", err)
	}
	return remotes, nil
}

// PickRemote selects a remote by name, falling back to origin and then to the
// first configured remote.
func PickRemote(remotes []*gogit.Remote, remoteName string) (*gogit.Remote, error) {
	if remoteName != "" {
		for _, remote := range remotes {
			if remote.Config().Name == remoteName {
				return remote, nil
			}
		}
		return nil, fmt.Errorf("remote %q not found", remoteName)
	}
	for _, remote := range remotes {
		if remote.Config().Name == "origin" {
			return remote, nil
		}
	}
	return remotes[0], nil
}

func infoFromRemote(remote *gogit.Remote) (Info, bool) {
	urls := remote.Config().URLs
	if len(urls) == 0 {
		return Info{}, false
	}
	info, ok := ParseURL(urls[0])
	if !ok {
		return Info{}, false
	}
	return info, true
}
