package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/yuki-nemurenai/magic-releaser/internal/publisher"
	"github.com/yuki-nemurenai/magic-releaser/internal/pusher"
	"github.com/yuki-nemurenai/magic-releaser/internal/repository"
)

// versionLikeTagPattern matches tags that look like version numbers regardless
// of the configured strategy, including a name prefix such as app-, app/ or
// app@ and a pre-release suffix. They are used to detect a strategy switch,
// which would otherwise silently move the release boundary to the first commit.
var versionLikeTagPattern = regexp.MustCompile(`^(?:[A-Za-z][\w.-]*[-_/@])?v?\d+(?:\.\d+)+(?:[-+][0-9A-Za-z.-]+)?$`)

func Run(ctx context.Context, options Options) (Result, error) {
	config, _, err := LoadConfig(options.RepoDir, options.ConfigPath)
	if err != nil {
		return Result{}, err
	}
	options = MergeConfig(options, config)
	options = withDefaults(options)
	if err := validateOptions(options); err != nil {
		return Result{}, err
	}
	// A release shortly after midnight belongs to the new month where the team
	// lives, not where UTC is.
	location, err := time.LoadLocation(options.Timezone)
	if err != nil {
		return Result{}, fmt.Errorf("timezone %q: %w", options.Timezone, err)
	}
	options.Now = options.Now.In(location)
	warnDeprecatedFileTypes(options)

	git := Git{Dir: options.RepoDir}
	shallow, err := git.IsShallow(ctx)
	if err != nil {
		return Result{}, err
	}
	if shallow {
		return Result{}, errors.New("the repository is a shallow clone, so the release history and tags are incomplete; " +
			"fetch the full history (GitHub Actions: fetch-depth: 0, GitLab CI: GIT_DEPTH: 0, locally: git fetch --unshallow --tags)")
	}
	boundary, err := resolveBoundary(ctx, git, options)
	if err != nil {
		return Result{}, err
	}
	if err := checkStrategyConsistency(options, boundary); err != nil {
		return Result{}, err
	}
	if err := checkPreviousRelease(options, boundary); err != nil {
		return Result{}, err
	}
	lastVersion, lastTag := boundary.BoundaryVersion, boundary.Tag

	commits, err := git.Commits(ctx, lastTag)
	if err != nil {
		return Result{}, err
	}

	level := AnalyzeCommitsWithOptions(commits, options.SkipMergeCommits)
	if level == ReleaseNone {
		fmt.Fprintln(options.Output, "No release required: no feat, fix, perf, or breaking commits found.")
		return Result{Released: false, LastVersion: lastVersion, Commits: commits}, nil
	}

	// The version is advanced from the highest tag in the whole repository, not
	// from the last one on this branch, so a release never reuses a tag name
	// another branch already published.
	baseVersion := boundary.HighestVersion
	nextVersion, err := NextVersionWithCalVer(options.Versioning, options.CalVerFormat, baseVersion, level, options.Now)
	if err != nil {
		return Result{}, err
	}
	if err := ensureVersionAdvances(options, baseVersion, nextVersion); err != nil {
		return Result{}, err
	}
	tagName, err := TagName(options.TagFormat, nextVersion)
	if err != nil {
		return Result{}, err
	}
	repo, err := repository.Detect(ctx, options.RepoDir, options.Remote)
	if err != nil {
		return Result{}, err
	}
	provider := repo.Provider
	if options.Provider != "" {
		provider = repository.Provider(options.Provider)
	}
	options.Notes.Style = ResolveNotesStyle(options.Notes.Style, provider)
	notes, err := buildNotes(options, repo, nextVersion, tagName, lastTag, commits)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Released:    true,
		LastVersion: lastVersion,
		NextVersion: nextVersion,
		TagName:     tagName,
		Notes:       notes,
		Commits:     commits,
	}

	fmt.Fprintf(options.Output, "Release type: %s\n", level)
	if lastVersion != "" {
		fmt.Fprintf(options.Output, "Previous version: %s\n", lastVersion)
	}
	if boundary.HighestVersion != lastVersion {
		fmt.Fprintf(options.Output,
			"Highest version in repository: %s (the next version advances from it to avoid reusing a tag name)\n",
			boundary.HighestVersion)
	}
	fmt.Fprintf(options.Output, "Next version: %s\n", nextVersion)
	if options.Publish {
		fmt.Fprintf(options.Output, "Release name: %s\n", releaseName(options.ReleaseName, options.Notes.Style, tagName, nextVersion))
	}
	fmt.Fprintf(options.Output, "Tag: %s\n\n%s", tagName, notes)

	// The target branch is resolved before anything is written, so a CI job
	// that cannot deliver the release fails without leaving a local tag behind.
	pushBranch, err := resolvePushBranch(ctx, git, options)
	if err != nil {
		return Result{}, err
	}
	if err := checkBackMerge(ctx, git, options, repo, pushBranch); err != nil {
		return Result{}, err
	}

	if options.DryRun {
		if len(options.BackMerge) > 0 {
			fmt.Fprintf(options.Output, "\nBack-merge into: %s\n", strings.Join(options.BackMerge, ", "))
		}
		fmt.Fprintln(options.Output, "\nDry run: changelog, version files, and git tag were not written.")
		return result, nil
	}

	// Refuse to build on top of an existing tag. Two concurrent CI runs in the
	// same month would otherwise compute the same CalVer MICRO and race.
	if options.CreateTag {
		exists, err := git.HasTag(ctx, tagName)
		if err != nil {
			return Result{}, err
		}
		if exists {
			return Result{}, fmt.Errorf("tag %s already exists: refusing to release on top of it", tagName)
		}
	}

	bumpedFiles, err := applyReleaseFiles(options, notes, nextVersion)
	if err != nil {
		return Result{}, err
	}
	result.BumpedFiles = bumpedFiles

	if options.Mode == ModePullRequest {
		result.PullRequestTitle = fmt.Sprintf("chore: release %s", nextVersion)
		result.PullRequestBody = FormatPullRequestBody(result)
		fmt.Fprintf(options.Output, "\nPull request mode: git tag was not created.\n")
		fmt.Fprintf(options.Output, "PR title: %s\n\n%s", result.PullRequestTitle, result.PullRequestBody)
		return result, nil
	}

	tagTarget := plumbing.ZeroHash
	if options.CreateCommit {
		message := releaseCommitMessage(options.ReleaseCommitMessage, tagName, nextVersion)
		hash, committed, err := git.CommitFiles(ctx, message, bumpedFiles)
		if err != nil {
			return Result{}, err
		}
		if committed {
			result.ReleaseCommit = hash.String()
			tagTarget = hash
		}
	}

	if options.CreateTag {
		if err := git.CreateAnnotatedTagAt(ctx, tagTarget, tagName, tagMessage(tagName, notes)); err != nil {
			return Result{}, err
		}
	}
	if err := deliverRelease(ctx, options, result, pushBranch); err != nil {
		return Result{}, err
	}
	if err := publishRelease(ctx, options, repo, &result, nextVersion, tagName, notes); err != nil {
		return Result{}, err
	}
	if err := backMerge(ctx, git, options, repo, &result); err != nil {
		return result, fmt.Errorf("release %s is out, but the back-merge failed: %w", tagName, err)
	}
	return result, nil
}

// DefaultReleaseCommitMessage is the message of the release commit. It carries
// no [skip ci]: GitLab and GitHub apply that marker to the tag pipeline of the
// same commit as well, which would silently skip the build of the release.
const DefaultReleaseCommitMessage = "chore(release): {{tag}}"

// The default title of the forge release follows the notes style: the bare
// tag on GitHub, as semantic-release does, and "Release <version>" in the Keep
// a Changelog layout.
const (
	DefaultReleaseName                      = "Release {{version}}"
	DefaultConventionalChangelogReleaseName = "{{tag}}"
)

func releaseCommitMessage(template, tagName, version string) string {
	if template == "" {
		template = DefaultReleaseCommitMessage
	}
	return expandTemplate(template, tagName, version)
}

func releaseName(template string, style NotesStyle, tagName, version string) string {
	switch {
	case template != "":
	case style == NotesStyleConventionalChangelog:
		template = DefaultConventionalChangelogReleaseName
	default:
		template = DefaultReleaseName
	}
	return expandTemplate(template, tagName, version)
}

// expandTemplate substitutes the {{tag}} and {{version}} placeholders.
func expandTemplate(template, tagName, version string) string {
	return strings.ReplaceAll(strings.ReplaceAll(template, "{{tag}}", tagName), "{{version}}", version)
}

// resolvePushBranch returns the branch that receives the release commit, or an
// empty string when the branch is not pushed.
func resolvePushBranch(ctx context.Context, git Git, options Options) (string, error) {
	if !options.PushBranch {
		return "", nil
	}
	if options.PushBranchName != "" {
		return options.PushBranchName, nil
	}
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return "", err
	}
	if branch == "" {
		return "", errors.New("cannot push the release commit: HEAD is detached, which is how CI checks a commit out; " +
			"pass --push-branch-name (GitLab CI: $CI_COMMIT_BRANCH, GitHub Actions: $GITHUB_REF_NAME)")
	}
	return branch, nil
}

// deliverRelease puts the release commit on its branch and the tag next to it
// in a single push. The push is atomic where the server supports it, so a
// rejected branch update (a protected branch, a concurrent push) cannot leave
// behind a tag on a commit the branch never received. The commit is pushed by
// hash, which works for the detached HEAD of a CI runner.
func deliverRelease(ctx context.Context, options Options, result Result, branch string) error {
	request := pusher.Request{}
	if options.Push && options.CreateTag {
		request.Tag = result.TagName
	}
	if branch != "" && result.ReleaseCommit != "" {
		request.Branch = branch
		request.Commit = result.ReleaseCommit
	}
	if request.Tag == "" && request.Branch == "" {
		return nil
	}
	if options.Pusher == nil {
		return errors.New("cannot push: no pusher is configured")
	}
	switch {
	case request.Branch != "" && request.Tag != "":
		fmt.Fprintf(options.Output, "Pushing release commit %s to %s and tag %s...\n", shortHash(request.Commit), request.Branch, request.Tag)
	case request.Branch != "":
		fmt.Fprintf(options.Output, "Pushing release commit %s to %s...\n", shortHash(request.Commit), request.Branch)
	default:
		fmt.Fprintf(options.Output, "Pushing tag %s...\n", request.Tag)
	}
	return options.Pusher.Push(ctx, request)
}

// publishRelease creates the release on the forge. It is only reached after
// the dry run early return, so a dry run never touches the network.
func publishRelease(ctx context.Context, options Options, repo repository.Info, result *Result, version, tagName, notes string) error {
	if !options.Publish {
		return nil
	}
	release, repo, err := forge(options, repo, "publish")
	if err != nil {
		return err
	}
	published, err := release.Publish(ctx, publisher.Request{
		Repository: repo,
		TagName:    tagName,
		Version:    version,
		Name:       releaseName(options.ReleaseName, options.Notes.Style, tagName, version),
		Notes:      notes,
		Target:     result.ReleaseCommit,
		Draft:      options.Draft,
		Prerelease: options.Prerelease,
	})
	if err != nil {
		return err
	}
	result.Published = true
	result.ReleaseURL = published.URL
	action := "Updated"
	if published.Created {
		action = "Created"
	}
	fmt.Fprintf(options.Output, "%s release %s on %s: %s\n", action, tagName, published.Provider, published.URL)
	return nil
}

// forge connects to the API of the repository's forge; purpose names the
// operation in the error of a repository without a forge.
func forge(options Options, repo repository.Info, purpose string) (publisher.Publisher, repository.Info, error) {
	if !repo.Found() {
		return nil, repo, fmt.Errorf("cannot %s: the repository has no usable git remote, configure one or pass --provider", purpose)
	}
	if options.Provider != "" {
		repo.Provider = repository.Provider(options.Provider)
	}
	client, err := publisher.New(repo, publisher.Options{
		BaseURL: options.APIURL,
		Token:   repository.ResolveToken(repo.Provider, options.Token),
		Verbose: options.Output,
	})
	return client, repo, err
}

// checkBackMerge validates the back-merge before anything is written.
func checkBackMerge(ctx context.Context, git Git, options Options, repo repository.Info, pushBranch string) error {
	if len(options.BackMerge) == 0 || options.Mode == ModePullRequest {
		return nil
	}
	if !options.Push && !options.DryRun {
		return errors.New("backMerge needs --push: the forge merges the release commit and its tag, which have to be on the remote")
	}
	if !repo.Found() && options.Provider == "" {
		return errors.New("backMerge needs a GitHub or GitLab remote: the forge API makes the merge")
	}
	release := pushBranch
	if release == "" {
		branch, err := git.CurrentBranch(ctx)
		if err != nil {
			return err
		}
		release = branch
	}
	seen := map[string]bool{}
	for _, target := range options.BackMerge {
		switch {
		case !validBranchName.MatchString(target):
			return fmt.Errorf("backMerge: %q is not a branch name", target)
		case target == release:
			return fmt.Errorf("backMerge: %q is the release branch itself", target)
		case seen[target]:
			return fmt.Errorf("backMerge: %q is listed twice", target)
		}
		seen[target] = true
	}
	return nil
}

// validBranchName accepts the branch names a back-merge can target.
var validBranchName = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]*$`)

// backMerge merges the published release into the configured branches. A
// branch the forge cannot merge into automatically gets a pull or merge
// request, reported as a warning: the release itself is complete.
func backMerge(ctx context.Context, git Git, options Options, repo repository.Info, result *Result) error {
	if len(options.BackMerge) == 0 || options.Mode == ModePullRequest {
		return nil
	}
	commit := result.ReleaseCommit
	if commit == "" {
		head, err := git.HeadHash(ctx)
		if err != nil {
			return err
		}
		commit = head.String()
	}
	client, repo, err := forge(options, repo, "back-merge")
	if err != nil {
		return err
	}
	results, err := client.BackMerge(ctx, publisher.BackMergeRequest{
		Repository: repo,
		TagName:    result.TagName,
		Commit:     commit,
		Targets:    options.BackMerge,
	})
	result.BackMerges = results
	for _, merge := range results {
		switch merge.Outcome {
		case publisher.BackMerged:
			fmt.Fprintf(options.Output, "Back-merged %s into %s\n", result.TagName, merge.Target)
		case publisher.BackMergeUpToDate:
			fmt.Fprintf(options.Output, "%s already contains %s\n", merge.Target, result.TagName)
		case publisher.BackMergePending:
			fmt.Fprintf(options.ErrorOutput, "warning: back-merge of %s into %s needs a person (%s): %s\n",
				result.TagName, merge.Target, merge.Reason, merge.URL)
		}
	}
	return err
}

func tagMessage(tagName, notes string) string {
	return fmt.Sprintf("%s\n\n%s", tagName, notes)
}

// buildNotes renders the release notes. The remote is resolved for the forge
// links; a repository without a usable remote still gets valid notes, just
// without links.
func buildNotes(options Options, repo repository.Info, version, tagName string, lastTag Tag, commits []Commit) (string, error) {
	if options.Notes.Preset == "" {
		options.Notes.Preset = PresetConventionalCommits
	}
	categories := options.Notes.Categories
	if len(categories) == 0 {
		categories = DefaultCategories(options.Notes.Preset)
	}
	return GenerateNotes(NotesOptions{
		Version:          version,
		Date:             options.Now,
		Commits:          commits,
		SkipMergeCommits: options.SkipMergeCommits,
		Categories:       categories,
		Repository:       repo,
		CompareFrom:      lastTag.Name,
		CompareTo:        tagName,
		ShowContributors: options.Notes.ShowContributors,
		Style:            options.Notes.Style,
	}), nil
}

// checkStrategyConsistency turns a silent history replay into a hard error.
func checkStrategyConsistency(options Options, boundary releaseBoundary) error {
	// A repository that carries version tags but none of them is readable
	// under this strategy and format: the boundary is unknown.
	if len(boundary.ForeignTags) == 0 || boundary.BoundaryVersion != "" || boundary.HighestVersion != "" {
		return nil
	}
	foreignTags := boundary.ForeignTags
	if options.ForceFirstRelease {
		fmt.Fprintf(options.ErrorOutput,
			"warning: ignoring %d existing version tag(s) that do not match versioning %q with tag format %q; the whole history will be released\n",
			len(foreignTags), options.Versioning, options.TagFormat)
		return nil
	}
	preview := foreignTags
	if len(preview) > 5 {
		preview = preview[:5]
	}
	return fmt.Errorf(
		"found %d version-like tag(s) (%s) that match neither versioning %q nor tag format %q, so the last release boundary is unknown and the entire history would be released; either switch back to the previous strategy, or pass --force-first-release to accept a first release",
		len(foreignTags), joinLimited(preview, ", "), options.Versioning, options.TagFormat,
	)
}

// warnDeprecatedFileTypes names the former file types still in use. They keep
// working, so a configuration is migrated when convenient rather than at once.
func warnDeprecatedFileTypes(options Options) {
	for _, pkg := range options.Packages {
		for _, file := range pkg.Files {
			if replacement, ok := deprecatedFileTypes[file.Type]; ok {
				fmt.Fprintf(options.ErrorOutput, "warning: file type %q of %s is deprecated, use %q\n",
					file.Type, file.Path, replacement)
			}
		}
	}
}

// checkPreviousRelease refuses a first release when the configuration asks
// for an earlier one. A repository adopting the tool after years of history
// would otherwise publish all of it as the notes of its first release.
func checkPreviousRelease(options Options, boundary releaseBoundary) error {
	if !options.RequirePreviousRelease || options.ForceFirstRelease || boundary.BoundaryVersion != "" {
		return nil
	}
	example := "1.0.0"
	if options.Versioning == VersioningCalVer {
		if version, err := NextCalVer(options.CalVerFormat, "", options.Now); err == nil {
			example = version
		}
	}
	tag, err := TagName(options.TagFormat, example)
	if err != nil {
		tag = example
	}
	return fmt.Errorf("no previous release is reachable from HEAD and requirePreviousRelease is set, "+
		"so the first release would list the whole history; tag the last released commit once, e.g. "+
		"git tag -a %s -m \"Release baseline\" <commit> && git push origin %s, or pass --force-first-release",
		tag, tag)
}

func joinLimited(values []string, separator string) string {
	out := ""
	for index, value := range values {
		if index > 0 {
			out += separator
		}
		out += value
	}
	return out
}

func ensureVersionAdvances(options Options, lastVersion, nextVersion string) error {
	if lastVersion == "" {
		return nil
	}
	compare, err := CompareVersionsWithCalVer(options.Versioning, options.CalVerFormat, nextVersion, lastVersion)
	if err != nil {
		return err
	}
	if compare <= 0 {
		return fmt.Errorf("computed version %s does not advance past the current release %s", nextVersion, lastVersion)
	}
	return nil
}

func applyReleaseFiles(options Options, notes, version string) ([]string, error) {
	changed := map[string]bool{}
	if options.Changelog != "" {
		if err := PrependChangelog(options.RepoDir, options.Changelog, notes); err != nil {
			return nil, err
		}
		changed[relativePath(options.RepoDir, options.Changelog)] = true
	}
	for _, pkg := range options.Packages {
		if pkg.Changelog == "" {
			continue
		}
		path := filepath.Join(pkg.Path, pkg.Changelog)
		if err := PrependChangelog(options.RepoDir, path, notes); err != nil {
			return nil, err
		}
		changed[relativePath(options.RepoDir, path)] = true
	}
	bumpedFiles, err := BumpVersionFiles(options.RepoDir, options.Packages, options.Manifest, version)
	if err != nil {
		return nil, err
	}
	for _, path := range bumpedFiles {
		changed[path] = true
	}

	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func withDefaults(options Options) Options {
	if options.RepoDir == "" {
		options.RepoDir = "."
	}
	if options.Mode == "" {
		options.Mode = ModeDirect
	}
	if options.Versioning == "" {
		options.Versioning = VersioningSemVer
	}
	if options.CalVerFormat == "" {
		options.CalVerFormat = DefaultCalVerFormat
	}
	if options.TagFormat == "" {
		options.TagFormat = "v{{version}}"
	}
	// An empty changelog is only meaningful when the user asked for it
	// explicitly (--changelog ""), otherwise it is the documented default.
	if options.Changelog == "" && !options.ChangelogSet {
		options.Changelog = "CHANGELOG.md"
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	if options.Timezone == "" {
		options.Timezone = "UTC"
	}
	if options.Output == nil {
		options.Output = io.Discard
	}
	if options.ErrorOutput == nil {
		options.ErrorOutput = io.Discard
	}
	return options
}

func validateOptions(options Options) error {
	switch options.Mode {
	case ModeDirect, ModePullRequest:
	default:
		return fmt.Errorf("unsupported mode %q", options.Mode)
	}
	switch options.Versioning {
	case VersioningSemVer, VersioningCalVer:
	default:
		return fmt.Errorf("unsupported versioning strategy %q", options.Versioning)
	}
	switch options.Notes.Style {
	case "", NotesStyleAuto, NotesStyleConventionalChangelog, NotesStyleKeepAChangelog:
	default:
		return fmt.Errorf("unsupported notes style %q: use auto, conventional-changelog or keep-a-changelog", options.Notes.Style)
	}
	if _, _, ok := tagFormatParts(options.TagFormat); !ok {
		return fmt.Errorf("tag format must contain {{version}}")
	}
	return nil
}

func relativePath(repoDir, path string) string {
	fullPath := path
	if !filepath.IsAbs(path) {
		fullPath = filepath.Join(repoDir, path)
	}
	relative, err := filepath.Rel(repoDir, fullPath)
	if err != nil {
		return path
	}
	return relative
}

// releaseBoundary separates the two questions about existing tags.
type releaseBoundary struct {
	// Tag is the last release tag reachable from HEAD, which ends the commit
	// range of this release.
	Tag Tag
	// BoundaryVersion is the version of that tag, empty when this branch has
	// never been released.
	BoundaryVersion string
	// HighestVersion is the highest version in the whole repository, including
	// tags that live on other branches.
	HighestVersion string
	// ForeignTags are version-like tags that match neither the strategy nor the
	// tag format.
	ForeignTags []string
}

func resolveBoundary(ctx context.Context, git Git, options Options) (releaseBoundary, error) {
	all, err := git.AllTags(ctx)
	if err != nil {
		return releaseBoundary{}, err
	}
	reached, err := git.Tags(ctx)
	if err != nil {
		return releaseBoundary{}, err
	}

	result := releaseBoundary{}
	for _, tag := range all {
		version, ok := VersionFromTag(options.TagFormat, tag.Name)
		if !ok || !ValidVersionWithCalVer(options.Versioning, options.CalVerFormat, version) {
			if versionLikeTagPattern.MatchString(tag.Name) {
				result.ForeignTags = append(result.ForeignTags, tag.Name)
			}
			continue
		}
		if isNewerVersion(options, version, result.HighestVersion) {
			result.HighestVersion = version
		}
	}
	for _, tag := range reached {
		version, ok := VersionFromTag(options.TagFormat, tag.Name)
		if !ok || !ValidVersionWithCalVer(options.Versioning, options.CalVerFormat, version) {
			continue
		}
		if isNewerVersion(options, version, result.BoundaryVersion) {
			result.BoundaryVersion = version
			result.Tag = tag
		}
	}
	return result, nil
}

func isNewerVersion(options Options, candidate, current string) bool {
	if current == "" {
		return true
	}
	compare, err := CompareVersionsWithCalVer(options.Versioning, options.CalVerFormat, candidate, current)
	if err != nil {
		return false
	}
	return compare > 0
}
