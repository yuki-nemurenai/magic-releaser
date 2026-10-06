// Command magic-releaser creates releases from Conventional Commits.
package main

import (
	"fmt"
	"os"
	"time"
	// The timezone database is embedded so that --timezone works in minimal
	// CI images that ship without tzdata.
	_ "time/tzdata"

	"github.com/spf13/cobra"
	"github.com/yuki-nemurenai/magic-releaser/internal/pusher"
	"github.com/yuki-nemurenai/magic-releaser/internal/release"
)

// Stamped at link time:
//
//	go build -ldflags "-X main.version=1.2.3 -X main.commit=abc1234" ./cmd/magic-releaser
var (
	version = ""
	commit  = ""
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "magic-releaser",
		Short: "Release automation from Conventional Commits, with SemVer or CalVer",
		Long: `magic-releaser automates releases from Conventional Commits.

It supports two versioning strategies:
  semver  Semantic Versioning rules compatible with Semantic Release:
          feat -> minor, fix/perf/revert -> patch, breaking changes -> major.
  calver  Calendar Versioning in YYYY.0M.MICRO format:
          year/month (UTC unless --timezone is set) with MICRO incremented
          per release in the same month.`,
		Example: `  magic-releaser release --dry-run
  magic-releaser release --versioning semver
  magic-releaser release --versioning calver
  magic-releaser release --mode pull-request`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	command.AddCommand(newReleaseCommand(), newVersionCommand())
	return command
}

func newReleaseCommand() *cobra.Command {
	var options release.Options
	var noTag bool
	var noCommit bool
	var includeMergeCommits bool
	var pushTagOnly bool
	var outputFile string

	command := &cobra.Command{
		Use:   "release",
		Short: "Analyze commits, generate release notes, and create a release tag",
		Long: `Analyze Conventional Commits since the latest release tag and create the next release.

Versioning strategies:
  semver  feat -> minor, fix/perf/revert -> patch, breaking changes -> major.
  calver  YYYY.0M.MICRO by default, dated in UTC unless --timezone is set,
          with MICRO incremented within a month.

Modes:
  direct        Update files and create a local annotated git tag.
  pull-request  Update files and print a PR title/body without creating a tag.`,
		Example: `  magic-releaser release --dry-run
  magic-releaser release --versioning semver
  magic-releaser release --versioning calver
  magic-releaser release --versioning calver --tag-format "{{version}}"
  magic-releaser release --mode pull-request`,
		RunE: func(command *cobra.Command, _ []string) error {
			options.CreateTag = !noTag
			// A release is only delivered when --push is given. The release
			// commit carries the bumped files, so it normally travels with the
			// tag; --push-tag-only narrows that for unusual workflows.
			options.PushBranch = options.Push && !pushTagOnly
			options.CreateCommit = !noCommit
			options.SkipMergeCommits = !includeMergeCommits
			options.Now = time.Now()
			options.Output = command.OutOrStdout()
			options.ErrorOutput = command.ErrOrStderr()
			// An explicit --changelog "" disables the changelog, so the flag
			// has to be distinguishable from "flag was never passed".
			options.ChangelogSet = command.Flags().Changed("changelog")
			ctx := command.Context()
			if options.Push || options.PushBranch {
				tagPusher, err := pusher.New(ctx, options.RepoDir, options.Remote, options.Provider, options.Token)
				if err != nil {
					return err
				}
				options.Pusher = tagPusher
			}
			result, err := release.Run(ctx, options)
			if err != nil {
				return err
			}
			return appendOutputs(outputFile, result, options.DryRun)
		},
	}

	command.Flags().StringVar(&options.ConfigPath, "config", "", "config path (default: .magic-releaser.yaml or .magic-releaser.yml)")
	command.Flags().StringVar((*string)(&options.Mode), "mode", "", "release mode: direct or pull-request (default: direct)")
	command.Flags().StringVar((*string)(&options.Versioning), "versioning", "", "versioning strategy: semver or calver (default: semver)")
	command.Flags().StringVar(&options.CalVerFormat, "calver-format", "", "calver layout, e.g. YYYY.0M.MICRO, YYYY.MM.DD, YY.MM.MICRO (default: YYYY.0M.MICRO)")
	command.Flags().StringVar(&options.Timezone, "timezone", "", "IANA timezone the calver date is read in, e.g. Europe/Moscow (default: UTC)")
	command.Flags().StringVar(&options.TagFormat, "tag-format", "", "tag format with {{version}} placeholder (default: v{{version}})")
	command.Flags().StringVar(&options.Changelog, "changelog", "", "changelog path, empty disables changelog updates (default: CHANGELOG.md)")
	command.Flags().StringVar(&options.Manifest, "manifest", "", "manifest JSON path for package versions")
	command.Flags().BoolVar(&options.DryRun, "dry-run", false, "print the next release and its notes without writing anything")
	command.Flags().BoolVar(&noTag, "no-tag", false, "do not create a git tag")
	command.Flags().BoolVar(&noCommit, "no-commit", false, "do not create the release commit, leave changed files in the working tree")
	command.Flags().BoolVar(&options.RequirePreviousRelease, "require-previous-release", false, "fail instead of making a first release when no previous release tag is reachable")
	command.Flags().BoolVar(&options.ForceFirstRelease, "force-first-release", false, "ignore existing version tags and treat the repository as never released")
	command.Flags().BoolVar(&includeMergeCommits, "include-merge-commits", false, "include merge commits in the release analysis and notes")
	command.Flags().BoolVar(&options.Publish, "publish", false, "create the release on GitHub or GitLab")
	command.Flags().BoolVar(&options.Push, "push", false, "push the release commit and the tag to the remote in one atomic push")
	command.Flags().BoolVar(&pushTagOnly, "push-tag-only", false, "with --push, push only the tag and leave the release commit local")
	command.Flags().StringSliceVar(&options.BackMerge, "back-merge", nil, "after the release, merge it into these branches through the forge, e.g. develop,candidate (default: backMerge of the config)")
	command.Flags().StringVar(&options.PushBranchName, "push-branch-name", "", "branch that receives the release commit, required for a detached HEAD (default: the current branch)")
	command.Flags().StringVar(&options.Provider, "provider", "", "release provider: github or gitlab (default: detected from the remote)")
	command.Flags().StringVar(&options.Token, "token", "", "API token (default: taken from the provider token environment variables)")
	command.Flags().StringVar(&options.APIURL, "api-url", "", "forge API base URL (default: detected from the remote and the CI environment)")
	command.Flags().StringVar(&options.ReleaseName, "release-name", "", "release title with {{version}} and {{tag}} placeholders (default: the tag on GitHub, Release {{version}} elsewhere)")
	command.Flags().BoolVar(&options.Draft, "draft", false, "create the release as a draft")
	command.Flags().BoolVar(&options.Prerelease, "prerelease", false, "mark the release as a prerelease")
	command.Flags().StringVar(&options.RepoDir, "repo", ".", "path to git repository")
	command.Flags().StringVar(&outputFile, "output-file", "", "append the result as KEY=value lines, e.g. $GITHUB_OUTPUT or a GitLab dotenv report")

	return command
}

// appendOutputs appends rather than overwrites: $GITHUB_OUTPUT is shared by
// every command of a step.
func appendOutputs(path string, result release.Result, dryRun bool) error {
	if path == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open output file: %w", err)
	}
	_, writeErr := file.WriteString(release.Outputs(result, dryRun))
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("write output file: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close output file: %w", closeErr)
	}
	return nil
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(command *cobra.Command, _ []string) {
			fmt.Fprintln(command.OutOrStdout(), buildVersion())
		},
	}
}

// buildVersion returns the version stamped at link time, falling back to the
// development placeholder for local builds.
func buildVersion() string {
	if version == "" {
		return "dev"
	}
	if commit == "" {
		return version
	}
	return version + "+" + commit
}
