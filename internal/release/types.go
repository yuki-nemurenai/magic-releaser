package release

import (
	"context"
	"io"
	"time"

	"github.com/yuki-nemurenai/magic-releaser/internal/pusher"
)

type Versioning string

const (
	VersioningSemVer Versioning = "semver"
	VersioningCalVer Versioning = "calver"
)

type Mode string

const (
	ModeDirect      Mode = "direct"
	ModePullRequest Mode = "pull-request"
)

// Pusher delivers a release to a remote: the release commit to its branch and
// the tag, in one push. The core decides what has to be delivered and stays
// unaware of the provider, the remote and the token used to authenticate.
type Pusher interface {
	Push(ctx context.Context, request pusher.Request) error
}

type Options struct {
	RepoDir      string
	ConfigPath   string
	Mode         Mode
	Versioning   Versioning
	CalVerFormat string
	// Timezone is the IANA zone the CalVer date is read in, UTC when empty.
	Timezone          string
	TagFormat         string
	Changelog         string
	ChangelogSet      bool
	Manifest          string
	Packages          []PackageConfig
	Remote            string
	Notes             NotesConfig
	DryRun            bool
	CreateTag         bool
	CreateCommit      bool
	ForceFirstRelease bool
	SkipMergeCommits  bool
	// Publish controls whether the release is created on the forge.
	Pusher     Pusher
	PushBranch bool
	// PushBranchName targets a specific branch, required for a detached HEAD.
	PushBranchName string
	// ReleaseCommitMessage is the template of the release commit message.
	ReleaseCommitMessage string
	Publish              bool
	Provider             string
	Token                string
	APIURL               string
	Push                 bool
	Draft                bool
	Prerelease           bool
	ReleaseName          string
	Now                  time.Time
	Output               io.Writer
	ErrorOutput          io.Writer
}

type Result struct {
	Released         bool
	LastVersion      string
	NextVersion      string
	TagName          string
	Notes            string
	Commits          []Commit
	BumpedFiles      []string
	ReleaseCommit    string
	ReleaseURL       string
	Published        bool
	PullRequestTitle string
	PullRequestBody  string
}

type Config struct {
	Mode         Mode       `yaml:"mode"`
	Versioning   Versioning `yaml:"versioning"`
	CalVerFormat string     `yaml:"calverFormat"`
	Timezone     string     `yaml:"timezone"`
	TagFormat    string     `yaml:"tagFormat"`
	// Changelog is a pointer so that "key absent" and "key present but empty"
	// are told apart: only the latter disables the changelog.
	Changelog *string `yaml:"changelog"`
	// ReleaseCommitMessage accepts the {{tag}} and {{version}} placeholders.
	ReleaseCommitMessage string          `yaml:"releaseCommitMessage"`
	Manifest             string          `yaml:"manifest"`
	Remote               string          `yaml:"remote"`
	Notes                NotesConfig     `yaml:"notes"`
	Packages             []PackageConfig `yaml:"packages"`
}

// NotesConfig configures the release notes layout.
type NotesConfig struct {
	// Preset selects a built-in category layout.
	Preset Preset `yaml:"preset"`
	// Categories overrides the preset entirely when set.
	Categories []Category `yaml:"categories"`
	// ShowContributors appends the list of commit authors.
	ShowContributors bool `yaml:"showContributors"`
}

// PackageConfig describes one package of the repository. The whole release
// shares a single version, so per package versioning is deliberately absent
// rather than silently ignored.
type PackageConfig struct {
	Name      string           `yaml:"name"`
	Path      string           `yaml:"path"`
	Changelog string           `yaml:"changelog"`
	Files     []BumpFileConfig `yaml:"files"`
}

type BumpFileConfig struct {
	Type  string `yaml:"type"`
	Path  string `yaml:"path"`
	Image string `yaml:"image"`
	// Marker is the annotation prefix of the generic type. It defaults to
	// x-release-please, which keeps existing release-please annotated files
	// working unchanged.
	Marker string `yaml:"marker"`
	// Pattern overrides the regexp used to recognise a version in the file.
	Pattern string `yaml:"pattern"`
}

type Commit struct {
	Hash         string
	Header       string
	Type         string
	Scope        string
	Description  string
	Breaking     bool
	BreakingBody string
	Merge        bool
	SkipRelease  bool
	AuthorName   string
	AuthorEmail  string
}

type Level int

const (
	ReleaseNone Level = iota
	ReleasePatch
	ReleaseMinor
	ReleaseMajor
)

func (level Level) String() string {
	switch level {
	case ReleaseMajor:
		return "major"
	case ReleaseMinor:
		return "minor"
	case ReleasePatch:
		return "patch"
	default:
		return "none"
	}
}
