package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/yuki-nemurenai/magic-releaser/internal/publisher"
	"github.com/yuki-nemurenai/magic-releaser/internal/repository"
)

// DefaultMonorepoTagFormat names the tags of a monorepo component, as
// release-please does: api-v1.2.0.
const DefaultMonorepoTagFormat = "{{component}}-v{{version}}"

// validComponent keeps a component usable in a tag and an output name.
var validComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// isMonorepo reports whether the packages are released as components.
func isMonorepo(packages []PackageConfig) bool {
	for _, pkg := range packages {
		if pkg.Component != "" {
			return true
		}
	}
	return false
}

// componentTagFormat is the tag format of one component.
func componentTagFormat(format, component string) string {
	return strings.ReplaceAll(format, "{{component}}", component)
}

// validateMonorepo checks the components before anything is read.
func validateMonorepo(options Options) error {
	if !isMonorepo(options.Packages) {
		if strings.Contains(options.TagFormat, "{{component}}") {
			return errors.New("tag format contains {{component}}, but no package names a component")
		}
		if len(options.LinkedVersions) > 0 {
			return errors.New("linkedVersions needs packages with a component")
		}
		return nil
	}
	if options.Mode == ModePullRequest {
		return errors.New("pull-request mode does not support components")
	}
	if !strings.Contains(options.TagFormat, "{{component}}") {
		return fmt.Errorf("tag format %q of a monorepo must contain {{component}}, e.g. %s", options.TagFormat, DefaultMonorepoTagFormat)
	}
	components := map[string]bool{}
	paths := map[string]bool{}
	for _, pkg := range options.Packages {
		switch {
		case pkg.Component == "":
			return fmt.Errorf("package %q has no component: in a monorepo every package names one", pkg.Path)
		case !validComponent.MatchString(pkg.Component):
			return fmt.Errorf("component %q may contain only letters, digits, '.', '_' and '-'", pkg.Component)
		case components[pkg.Component]:
			return fmt.Errorf("component %q is configured twice", pkg.Component)
		case paths[packagePath(pkg)]:
			return fmt.Errorf("path %q belongs to two components", packagePath(pkg))
		}
		components[pkg.Component] = true
		paths[packagePath(pkg)] = true
	}
	grouped := map[string]bool{}
	for _, group := range options.LinkedVersions {
		for _, component := range group {
			if !components[component] {
				return fmt.Errorf("linkedVersions names %q, which is not a component", component)
			}
			if grouped[component] {
				return fmt.Errorf("component %q is in two linkedVersions groups", component)
			}
			grouped[component] = true
		}
	}
	return nil
}

func packagePath(pkg PackageConfig) string {
	return path.Clean(filepath.ToSlash(pkg.Path))
}

// touches reports whether a commit changes a file of the package.
func touches(commit Commit, pkg PackageConfig) bool {
	root := packagePath(pkg)
	for _, file := range commit.Files {
		if !within(file, root) {
			continue
		}
		excluded := false
		for _, exclude := range pkg.ExcludePaths {
			if within(file, path.Join(root, path.Clean(filepath.ToSlash(exclude)))) {
				excluded = true
				break
			}
		}
		if !excluded {
			return true
		}
	}
	return false
}

func within(file, dir string) bool {
	return dir == "." || file == dir || strings.HasPrefix(file, dir+"/")
}

// componentPlan is the release of one component as it is computed.
type componentPlan struct {
	pkg      PackageConfig
	options  Options
	boundary releaseBoundary
	commits  []Commit
	level    Level
	next     string
	tag      string
	notes    string
}

func (plan componentPlan) releasing() bool {
	return plan.next != ""
}

// runMonorepo releases each component that has changes since its own last
// release: one release commit for all of them, one tag, changelog and forge
// release per component.
func runMonorepo(ctx context.Context, git Git, options Options) (Result, error) {
	plans, err := planComponents(ctx, git, options)
	if err != nil {
		return Result{}, err
	}
	if err := linkVersions(options, plans); err != nil {
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
	for index := range plans {
		plans[index].options.Notes.Style = options.Notes.Style
	}

	result := Result{}
	var tags []string
	for index := range plans {
		plan := &plans[index]
		component := ComponentResult{
			Component:   plan.pkg.Component,
			Path:        packagePath(plan.pkg),
			LastVersion: plan.boundary.BoundaryVersion,
		}
		if plan.releasing() {
			plan.notes, err = buildNotes(plan.options, repo, plan.next, plan.tag, plan.boundary.Tag, plan.commits)
			if err != nil {
				return Result{}, err
			}
			component.Released = true
			component.NextVersion = plan.next
			component.TagName = plan.tag
			component.Notes = plan.notes
			tags = append(tags, plan.tag)
			result.Commits = append(result.Commits, plan.commits...)
		}
		result.Components = append(result.Components, component)
	}
	printComponents(options, plans)
	if len(tags) == 0 {
		fmt.Fprintln(options.Output, "No release required: no component has feat, fix, perf, or breaking commits.")
		return result, nil
	}
	result.Released = true

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
		fmt.Fprintln(options.Output, "\nDry run: changelogs, version files, and git tags were not written.")
		return result, nil
	}

	if options.CreateTag {
		for _, tag := range tags {
			exists, err := git.HasTag(ctx, tag)
			if err != nil {
				return Result{}, err
			}
			if exists {
				return Result{}, fmt.Errorf("tag %s already exists: refusing to release on top of it", tag)
			}
		}
	}
	files, err := writeComponentFiles(options, plans)
	if err != nil {
		return Result{}, err
	}
	result.BumpedFiles = files

	tagTarget := plumbing.ZeroHash
	if options.CreateCommit {
		message := releaseCommitMessage(options.ReleaseCommitMessage, strings.Join(tags, ", "), "")
		hash, committed, err := git.CommitFiles(ctx, message, files)
		if err != nil {
			return Result{}, err
		}
		if committed {
			result.ReleaseCommit = hash.String()
			tagTarget = hash
		}
	}
	if options.CreateTag {
		for _, plan := range plans {
			if !plan.releasing() {
				continue
			}
			if err := git.CreateAnnotatedTagAt(ctx, tagTarget, plan.tag, tagMessage(plan.tag, plan.notes)); err != nil {
				return Result{}, err
			}
		}
	}
	if err := deliverRelease(ctx, options, result.ReleaseCommit, tags, pushBranch); err != nil {
		return Result{}, err
	}
	if err := publishComponents(ctx, options, repo, &result, plans); err != nil {
		return Result{}, err
	}

	// The back-merge carries all the tags of this release under one label.
	merge := Result{TagName: strings.Join(tags, "+"), ReleaseCommit: result.ReleaseCommit}
	err = backMerge(ctx, git, options, repo, &merge)
	result.BackMerges = merge.BackMerges
	if err != nil {
		return result, fmt.Errorf("release %s is out, but the back-merge failed: %w", strings.Join(tags, ", "), err)
	}
	return result, nil
}

// planComponents computes, for each component, the commits since its last
// release that touch its path and the version they produce.
func planComponents(ctx context.Context, git Git, options Options) ([]componentPlan, error) {
	formats := make([]string, 0, len(options.Packages))
	for _, pkg := range options.Packages {
		formats = append(formats, componentTagFormat(options.TagFormat, pkg.Component))
	}
	plans := make([]componentPlan, 0, len(options.Packages))
	for _, pkg := range options.Packages {
		componentOptions := options
		componentOptions.TagFormat = componentTagFormat(options.TagFormat, pkg.Component)
		boundary, err := resolveBoundary(ctx, git, componentOptions)
		if err != nil {
			return nil, err
		}
		// The tags of the other components are not a sign of a strategy switch.
		boundary.ForeignTags = withoutComponentTags(boundary.ForeignTags, formats, options)
		if err := checkStrategyConsistency(componentOptions, boundary); err != nil {
			return nil, fmt.Errorf("component %s: %w", pkg.Component, err)
		}
		if err := checkPreviousRelease(componentOptions, boundary); err != nil {
			return nil, fmt.Errorf("component %s: %w", pkg.Component, err)
		}
		all, err := git.Commits(ctx, boundary.Tag, true)
		if err != nil {
			return nil, err
		}
		commits := make([]Commit, 0, len(all))
		for _, commit := range all {
			if touches(commit, pkg) {
				commits = append(commits, commit)
			}
		}
		plan := componentPlan{
			pkg:      pkg,
			options:  componentOptions,
			boundary: boundary,
			commits:  commits,
			level:    AnalyzeCommitsWithOptions(commits, options.SkipMergeCommits),
		}
		if plan.level != ReleaseNone {
			if plan.next, err = nextComponentVersion(plan, plan.level); err != nil {
				return nil, err
			}
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func withoutComponentTags(tags, formats []string, options Options) []string {
	kept := tags[:0:0]
	for _, tag := range tags {
		component := false
		for _, format := range formats {
			if version, ok := VersionFromTag(format, tag); ok && ValidVersionWithCalVer(options.Versioning, options.CalVerFormat, version) {
				component = true
				break
			}
		}
		if !component {
			kept = append(kept, tag)
		}
	}
	return kept
}

func nextComponentVersion(plan componentPlan, level Level) (string, error) {
	options := plan.options
	base := plan.boundary.HighestVersion
	next, err := NextVersionWithCalVer(options.Versioning, options.CalVerFormat, base, level, options.Now)
	if err != nil {
		return "", fmt.Errorf("component %s: %w", plan.pkg.Component, err)
	}
	if err := ensureVersionAdvances(options, base, next); err != nil {
		return "", fmt.Errorf("component %s: %w", plan.pkg.Component, err)
	}
	return next, nil
}

// linkVersions gives every component of a linkedVersions group the highest
// version of the group as soon as one of them is released, and assigns the
// tags.
func linkVersions(options Options, plans []componentPlan) error {
	byComponent := map[string]*componentPlan{}
	for index := range plans {
		byComponent[plans[index].pkg.Component] = &plans[index]
	}
	for _, group := range options.LinkedVersions {
		released := false
		for _, component := range group {
			released = released || byComponent[component].releasing()
		}
		if !released {
			continue
		}
		version := ""
		for _, component := range group {
			plan := byComponent[component]
			level := plan.level
			if level == ReleaseNone {
				level = ReleasePatch
			}
			candidate, err := nextComponentVersion(*plan, level)
			if err != nil {
				return err
			}
			if version == "" || isNewerVersion(options, candidate, version) {
				version = candidate
			}
		}
		for _, component := range group {
			byComponent[component].next = version
		}
	}
	for index := range plans {
		if !plans[index].releasing() {
			continue
		}
		tag, err := TagName(plans[index].options.TagFormat, plans[index].next)
		if err != nil {
			return err
		}
		plans[index].tag = tag
	}
	return nil
}

func printComponents(options Options, plans []componentPlan) {
	for _, plan := range plans {
		if !plan.releasing() {
			last := plan.boundary.BoundaryVersion
			if last == "" {
				last = "never released"
			}
			fmt.Fprintf(options.Output, "Component %s (%s): no release, at %s\n\n", plan.pkg.Component, packagePath(plan.pkg), last)
			continue
		}
		fmt.Fprintf(options.Output, "Component %s (%s): %s -> %s, tag %s\n",
			plan.pkg.Component, packagePath(plan.pkg), orNone(plan.boundary.BoundaryVersion), plan.next, plan.tag)
		if options.Publish {
			fmt.Fprintf(options.Output, "Release name: %s\n",
				releaseName(options.ReleaseName, options.Notes.Style, plan.tag, plan.next, plan.pkg.Component))
		}
		fmt.Fprintf(options.Output, "\n%s\n", plan.notes)
	}
}

func orNone(version string) string {
	if version == "" {
		return "none"
	}
	return version
}

// writeComponentFiles writes the changelog and the version files of each
// released component, and the manifest of all versions.
func writeComponentFiles(options Options, plans []componentPlan) ([]string, error) {
	changed := map[string]bool{}
	versions := map[string]string{}
	for _, plan := range plans {
		if !plan.releasing() {
			continue
		}
		versions[plan.pkg.Component] = plan.next
		name := plan.pkg.Changelog
		if name == "" {
			name = options.Changelog
		}
		if name != "" {
			file := filepath.Join(plan.pkg.Path, name)
			if err := PrependChangelog(options.RepoDir, file, plan.notes); err != nil {
				return nil, err
			}
			changed[relativePath(options.RepoDir, file)] = true
		}
		files, err := BumpVersionFiles(options.RepoDir, []PackageConfig{plan.pkg}, "", plan.next)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			changed[file] = true
		}
	}
	if options.Manifest != "" {
		file, err := writeComponentManifest(options.RepoDir, options.Manifest, versions)
		if err != nil {
			return nil, err
		}
		changed[file] = true
	}
	paths := make([]string, 0, len(changed))
	for file := range changed {
		paths = append(paths, file)
	}
	sort.Strings(paths)
	return paths, nil
}

// writeComponentManifest records the released versions by component, keeping
// the entries of the components this release leaves alone.
func writeComponentManifest(repoDir, manifest string, versions map[string]string) (string, error) {
	fullPath := manifest
	if !filepath.IsAbs(manifest) {
		fullPath = filepath.Join(repoDir, manifest)
	}
	entries := map[string]string{}
	data, err := os.ReadFile(fullPath)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &entries); err != nil {
			return "", fmt.Errorf("read manifest %s: %w", manifest, err)
		}
	}
	for component, version := range versions {
		entries[component] = version
	}
	output, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(fullPath, append(output, '\n'), 0o644); err != nil {
		return "", err
	}
	return relativePath(repoDir, fullPath), nil
}

// publishComponents creates one forge release per released component.
func publishComponents(ctx context.Context, options Options, repo repository.Info, result *Result, plans []componentPlan) error {
	if !options.Publish {
		return nil
	}
	client, repo, err := forge(options, repo, "publish")
	if err != nil {
		return err
	}
	for index, plan := range plans {
		if !plan.releasing() {
			continue
		}
		published, err := client.Publish(ctx, publisher.Request{
			Repository: repo,
			TagName:    plan.tag,
			Version:    plan.next,
			Name:       releaseName(options.ReleaseName, options.Notes.Style, plan.tag, plan.next, plan.pkg.Component),
			Notes:      plan.notes,
			Target:     result.ReleaseCommit,
			Draft:      options.Draft,
			Prerelease: options.Prerelease,
		})
		if err != nil {
			return fmt.Errorf("publish %s: %w", plan.tag, err)
		}
		result.Published = true
		result.Components[index].ReleaseURL = published.URL
		action := "Updated"
		if published.Created {
			action = "Created"
		}
		fmt.Fprintf(options.Output, "%s release %s on %s: %s\n", action, plan.tag, published.Provider, published.URL)
	}
	return nil
}
