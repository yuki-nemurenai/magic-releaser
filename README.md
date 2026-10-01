# magic-releaser

[![ci](https://github.com/yuki-nemurenai/magic-releaser/actions/workflows/ci.yml/badge.svg)](https://github.com/yuki-nemurenai/magic-releaser/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/yuki-nemurenai/magic-releaser)](https://github.com/yuki-nemurenai/magic-releaser/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/yuki-nemurenai/magic-releaser.svg)](https://pkg.go.dev/github.com/yuki-nemurenai/magic-releaser)
[![license](https://img.shields.io/github/license/yuki-nemurenai/magic-releaser)](LICENSE)

**Fully automated releases from [Conventional Commits](https://www.conventionalcommits.org/), with
[SemVer](https://semver.org/) or [CalVer](https://calver.org/), for GitHub and GitLab.**

magic-releaser reads the commits since the last release, decides whether a release is needed,
computes the next version, writes the release notes and the changelog, updates the version in
your files, commits, tags, pushes, and publishes a GitHub or GitLab release. It is a single
static binary with no runtime dependencies: no Node.js, no plugins, and no `git` executable
for HTTPS and SSH remotes.

It combines what usually takes two tools:

- the commit analysis and release notes of [semantic-release](https://github.com/semantic-release/semantic-release);
- the version bumps in arbitrary files of [release-please](https://github.com/googleapis/release-please),
  driven by annotations placed in the files themselves.

## Highlights

- **SemVer and CalVer.** `feat` → minor, `fix`/`perf` → patch, breaking → major; or a calendar
  version such as `2026.09.3` in any layout you choose.
- **Release notes** grouped by type, with commit links, a compare link, breaking change
  descriptions and contributors. Section titles, order and visibility are configurable.
- **Version bumps in any file**: `package.json`, Helm charts, container image tags, and any text
  file through an `x-magic-release-version` annotation. Existing release-please annotations work
  unchanged.
- **Safe delivery.** The release commit and the tag reach the remote in one atomic push: a
  rejected branch update never leaves an orphaned tag behind. Works on the detached HEAD of a CI
  runner.
- **GitHub and GitLab**, including GitHub Enterprise, self-hosted GitLab and nested GitLab groups,
  detected from the remote and the CI environment.
- **Idempotent and CI friendly.** Reruns update the existing release, API calls are retried with
  backoff, results are exposed as step outputs and dotenv reports, `--dry-run` never writes.

## Contents

- [How it works](#how-it-works)
- [Commit message format](#commit-message-format)
- [Versioning](#versioning)
- [Installation](#installation)
- [Quick start](#quick-start)
- [GitHub Actions](#github-actions)
- [GitLab CI](#gitlab-ci)
- [Configuration](#configuration)
- [Release notes](#release-notes)
- [Updating versions in files](#updating-versions-in-files)
- [Command line reference](#command-line-reference)
- [Migrating](#migrating)
- [Troubleshooting](#troubleshooting)
- [Limitations](#limitations)
- [Development](#development)
- [License](#license)

## How it works

On every run, typically on each push to the default branch, magic-releaser:

1. **Finds the last release**: the highest version tag reachable from `HEAD` that matches the
   configured versioning and tag format.
2. **Analyzes the commits** since that tag and derives the release type. Without a `feat`, `fix`,
   `perf` or breaking commit there is nothing to release, and the run ends successfully.
3. **Computes the next version.** The version always advances past the highest version in the
   whole repository, so a release never reuses a tag published from another branch.
4. **Writes the release notes**, prepends them to `CHANGELOG.md` and updates the configured
   version files.
5. **Commits** exactly those files as `chore(release): <tag>` and creates an annotated tag on the
   release commit.
6. **Pushes** the release commit and the tag together (`--push`).
7. **Publishes** the release with its notes on GitHub or GitLab (`--publish`).

```text
feat: add export ───┐
fix: rounding error ├─► minor release ─► 1.4.0 ─► notes + CHANGELOG.md + files
docs: typo          ┘                            ─► commit ─► tag ─► push ─► GitHub/GitLab release
```

## Commit message format

magic-releaser follows the [Conventional Commits](https://www.conventionalcommits.org/)
specification:

```text
<type>(<optional scope>)!: <description>

<optional body>

<optional footers, e.g. BREAKING CHANGE: ...>
```

| Commit message | Release type |
|----------------|--------------|
| `fix(api): handle empty pages` | Patch: `1.4.0` → `1.4.1` |
| `perf: cache the lookup table` | Patch |
| `feat(ui): add dark mode` | Minor: `1.4.0` → `1.5.0` |
| `feat!: drop Node 18 support` | Major: `1.4.0` → `2.0.0` |
| `refactor: split the parser`<br><br>`BREAKING CHANGE: Parser.parse is removed` | Major |
| `docs`, `chore`, `ci`, `test`, `style`, `refactor`, `build` | No release, still listed in the notes of the next one |

- The text after `BREAKING CHANGE:` (or `BREAKING-CHANGE:`) is rendered under the entry in the
  breaking changes section.
- A commit containing `[skip release]` or `[release skip]` is ignored entirely.
- Merge commits are ignored unless `--include-merge-commits` is passed.

## Versioning

### SemVer

`versioning: semver` (the default) follows semantic-release: `feat` gives a minor release, `fix`
and `perf` a patch release, and a `!` or a `BREAKING CHANGE` footer a major release. The first
release is `1.0.0`. Tags are `v1.2.3` by default (`tagFormat: "v{{version}}"`).

### CalVer

`versioning: calver` produces a calendar version. The version encodes *when* a release happened,
not the shape of the change: a patch and a breaking release in the same month both advance the
counter, and a breaking change is announced by the release notes instead.

The layout is configurable with `calverFormat`:

| Token | Meaning | Example |
|-------|---------|---------|
| `YYYY` | full year | `2026` |
| `YY` | year, last two digits | `26` |
| `0M`, `MM` | month, zero padded | `09` |
| `M` | month | `9` |
| `0D`, `DD` | day, zero padded | `05` |
| `D` | day | `5` |
| `MICRO`, `PATCH` | release counter, reset in every new period | `0`, `1`, `2` |

| Layout | Versions |
|--------|----------|
| `YYYY.0M.MICRO` (default) | `2026.09.0`, `2026.09.1`, `2026.10.0` |
| `YY.MM.MICRO` | `26.09.0`, `26.09.1` |
| `YYYY.MM.DD` | `2026.09.30`, one release per day |
| `YYYY.MM.DD.MICRO` | `2026.09.30.0`, `2026.09.30.1` |
| `YYYYMMDD` | `20260930` |
| `YYYY.PATCH` | `2026.73`, `2026.74`: a year and a counter |

The date is read in UTC. Set `timezone` (an IANA name such as `Europe/Moscow`) so that a release
shortly after midnight on the 1st belongs to the new month where your team works. The version
never moves backwards, even if the newest tag carries a future date. A daily layout without
`MICRO` refuses a second release on the same day instead of colliding with the existing tag.

> **Note:** a zero padded CalVer such as `2026.09.0` is not a valid SemVer (leading zero). npm and
> Helm validate SemVer, so for `package.json` or `Chart.yaml` choose a layout with `MM` → `M`, for
> example `YYYY.M.MICRO`.

## Installation

| Method | Command |
|--------|---------|
| Binary | Download an archive for Linux, macOS or Windows from [Releases](https://github.com/yuki-nemurenai/magic-releaser/releases) |
| Container image | `docker run --rm -v "$PWD:/repo" -w /repo ghcr.io/yuki-nemurenai/magic-releaser:1 release --dry-run` |
| Go | `go install github.com/yuki-nemurenai/magic-releaser/cmd/magic-releaser@latest` |
| GitHub Actions | [`uses: yuki-nemurenai/magic-releaser@v1`](#github-actions) |
| GitLab CI | [include the template](#gitlab-ci) |

Every release publishes `checksums.txt` next to the archives. The container image is based on
Alpine and includes `git` for CI scripts.

## Quick start

1. Add a `.magic-releaser.yaml` to the repository root (optional, every key has a default):

   ```yaml
   versioning: calver
   calverFormat: YYYY.0M.MICRO
   timezone: Europe/Moscow
   tagFormat: "{{version}}"
   ```

2. Preview the next release locally. Nothing is written and no network call is made:

   ```sh
   magic-releaser release --dry-run
   ```

   ```text
   Release type: minor
   Previous version: 2026.09.0
   Next version: 2026.09.1
   Tag: 2026.09.1

   ## [2026.09.1] - 2026-09-30

   ### Features

   - **footer:** show the build number (0315a7b)

   ### Bug Fixes

   - typo in the footer (891bf93)
   ```

3. Release from CI with [GitHub Actions](#github-actions) or [GitLab CI](#gitlab-ci).

## GitHub Actions

```yaml
name: release

on:
  push:
    branches: [main]

permissions:
  contents: write # push the release commit and the tag, create the release

concurrency:
  group: release
  cancel-in-progress: false

jobs:
  release:
    # The release commit itself carries nothing to release.
    if: ${{ !startsWith(github.event.head_commit.message, 'chore(release):') }}
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
        with:
          fetch-depth: 0 # the full history and all tags are required

      - id: release
        uses: yuki-nemurenai/magic-releaser@v1
        with:
          versioning: calver
          timezone: Europe/Moscow

      - if: steps.release.outputs.released == 'true'
        run: echo "Released ${{ steps.release.outputs.tag }}: ${{ steps.release.outputs.release-url }}"
```

| Reference | Follows | Binary |
|-----------|---------|--------|
| `@v1` | the latest `1.x.y` release: the `v1` branch moves with every release | downloaded, checksum verified |
| `@v1.1.0` | exactly this release | downloaded, checksum verified |
| `@main`, a commit SHA | unreleased code | built from source with `actions/setup-go` |

`@v1` picks up fixes and features automatically and never a breaking change. Pin an exact
release, or a commit SHA, when every update has to be reviewed.

### Inputs

| Input | Default | Description |
|-------|---------|-------------|
| `versioning` | from config | `semver` or `calver` |
| `calver-format` | from config | CalVer layout, e.g. `YYYY.0M.MICRO` |
| `timezone` | from config | IANA timezone of the CalVer date |
| `tag-format` | from config | Tag format with a `{{version}}` placeholder |
| `config` | `.magic-releaser.yaml` | Path to the config file |
| `token` | `${{ github.token }}` | Token used to push and to create the release |
| `push` | `true` | Push the release commit and the tag |
| `publish` | `true` | Create the GitHub release |
| `draft` | `false` | Create the release as a draft |
| `prerelease` | `false` | Mark the release as a prerelease |
| `dry-run` | `false` | Only compute and print the release |
| `args` | | Extra command line arguments |

### Outputs

| Output | Description |
|--------|-------------|
| `released` | `true` when a release was written; `false` for a dry run or nothing to release |
| `version` | The released version, e.g. `2026.09.1` |
| `previous-version` | The version of the previous release |
| `tag` | The release tag |
| `release-url` | URL of the GitHub release |
| `release-commit` | Hash of the release commit |

### Protected branches and downstream workflows

- With branch protection on `main`, pass a token allowed to push to it (a GitHub App token or a
  fine-grained personal access token) through the `token` input.
- A tag pushed with `GITHUB_TOKEN` does not trigger other workflows. Continue in the same job on
  `steps.release.outputs.released == 'true'`, or pass a different token.

## GitLab CI

Include the template and extend its hidden jobs:

```yaml
include:
  - remote: https://raw.githubusercontent.com/yuki-nemurenai/magic-releaser/v1.0.1/templates/gitlab-ci.yml

stages: [release, build]

release:
  extends: .magic-releaser # releases the default branch on push
  stage: release
  variables:
    MAGIC_RELEASER_ARGS: "--timezone Europe/Moscow"

release:preview:
  extends: .magic-releaser-preview # dry run in merge requests
  stage: release

notify:
  stage: build
  needs: [release]
  script:
    # Variables of the dotenv report are available in scripts, not in rules.
    - if [ "$RELEASE_CREATED" = "true" ]; then echo "Released $RELEASE_TAG"; fi
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH && $CI_PIPELINE_SOURCE == "push"
```

The tag pushed by the release job starts its own pipeline, which is where the release is
usually built and deployed (`rules: - if: $CI_COMMIT_TAG`).

The template runs the container image with the full history (`GIT_DEPTH: 0`), serializes
releases with a `resource_group`, skips the pipeline of its own release commit, and exposes
`RELEASE_CREATED`, `RELEASE_VERSION`, `RELEASE_PREVIOUS_VERSION`, `RELEASE_TAG`, `RELEASE_COMMIT`
and `RELEASE_URL` as a dotenv report.

**Credentials.** Create a project access token with the `api` and `write_repository` scopes and
the right to push to the default branch, and store it as a masked, protected CI/CD variable named
`GITLAB_TOKEN`. On GitLab 17.2 or later, `CI_JOB_TOKEN` is used instead when the project allows
job tokens to push.

Self-hosted GitLab needs no extra setup: the API URL comes from `CI_API_V4_URL`.

> Keep `[skip ci]` out of the release commit message. GitLab applies it to the tag pipeline of the
> same commit as well, which would skip the build of the release itself.

## Configuration

magic-releaser reads `.magic-releaser.yaml` (or `.yml`) from the repository root. A command line
flag wins over the file, and the file wins over the defaults. An unknown key is an error, so a
typo never passes as a working setting.

```yaml
versioning: calver            # semver | calver
calverFormat: YYYY.0M.MICRO   # calver only
timezone: Europe/Moscow       # calver only, UTC by default
tagFormat: "{{version}}"      # default: v{{version}}
changelog: CHANGELOG.md       # "" disables the changelog file
releaseCommitMessage: "chore(release): {{tag}}"
remote: origin
mode: direct                  # direct | pull-request

notes:
  preset: conventionalcommits # conventionalcommits | angular | none
  style: auto                 # auto | conventional-changelog | keep-a-changelog
  showContributors: true

packages:
  - path: .
    files:
      - type: package-json
        path: package.json
      - type: generic
        path: src/app/footer.component.html
```

| Key | Default | Description |
|-----|---------|-------------|
| `versioning` | `semver` | Versioning strategy |
| `calverFormat` | `YYYY.0M.MICRO` | CalVer layout |
| `timezone` | `UTC` | IANA timezone the CalVer date is read in |
| `tagFormat` | `v{{version}}` | Tag name; must contain `{{version}}` |
| `changelog` | `CHANGELOG.md` | File the notes are prepended to; `""` disables it |
| `releaseCommitMessage` | `chore(release): {{tag}}` | Supports `{{tag}}` and `{{version}}` |
| `releaseName` | by `notes.style` | Title of the GitHub or GitLab release; supports `{{tag}}` and `{{version}}` |
| `remote` | `origin` | Remote used for detection, links and pushing |
| `mode` | `direct` | `pull-request` updates the files and prints a PR title and body, without committing or tagging |
| `manifest` | | JSON file receiving the version of every package |
| `notes` | | [Release notes](#release-notes) layout |
| `packages` | | [Version files](#updating-versions-in-files) to update |

The complete reference is in [docs/configuration.md](docs/configuration.md).

## Release notes

The notes group commits into sections. A preset gives the usual layout:

| Preset | Sections |
|--------|----------|
| `conventionalcommits` (default) | ⚠ BREAKING CHANGES, Features, Bug Fixes, Performance Improvements, Reverts, Documentation, Continuous Integration, Miscellaneous, Other Changes |
| `angular` | ⚠ BREAKING CHANGES, Features, Bug Fixes, Performance Improvements, Reverts, Documentation, Other Changes |
| `none` | Changes |

`notes.categories` replaces the preset with your own sections: titles (emoji welcome), their
order, the types and scopes they collect, and whether they are shown. This is the equivalent of
`presetConfig.types` of `@semantic-release/release-notes-generator`:

```yaml
notes:
  categories:
    - title: "✨ Features"
      types: [feat]
    - title: "🐛 Bug Fixes"
      types: [fix]
    - title: "⚡ Performance Improvements"
      types: [perf]
    - title: "⏪ Reverts"
      types: [revert]
    - title: "📝 Documentation"
      types: [docs]
    - title: "🎨 Styles"
      types: [style]
    - title: "♻️ Code Refactoring"
      types: [refactor]
    - title: "✅ Tests"
      types: [test]
    - title: "🔧 Build Systems"
      types: [build]
    - title: "👷 Continuous Integration"
      types: [ci]
    - title: "🔒 Security"
      types: [security]
    - title: "🧹 Chores"
      types: [chore]
      hidden: true
```

| Field | Description |
|-------|-------------|
| `title` | Section heading |
| `types` | Commit types collected by the section; `*` matches any type |
| `scopes` | Optional: only commits with one of these scopes |
| `breaking` | `true` makes this the section of breaking changes |
| `hidden` | `true` keeps the section out of the notes |

A commit lands in the first section that matches it, and a type listed in no section is left out.
A breaking change always goes to the breaking section: when your layout defines none, a
`⚠ BREAKING CHANGES` section is added in front, so the change behind a major release is never
lost. To style that heading, add your own section with `breaking: true`.

### Release title and heading

The title and the heading of a release follow the convention of the forge. `notes.style` picks
one explicitly, and `releaseName` overrides the title:

| `notes.style` | Default for | Title | Heading |
|---------------|-------------|-------|---------|
| `conventional-changelog` | GitHub | the tag: `v2.0.0` | `## [2.0.0](<compare link>) (2026-09-30)`, as semantic-release |
| `keep-a-changelog` | GitLab, other remotes | `Release 2.0.0` | `## [2.0.0] - 2026-09-30` and a compare link below, as git-cliff |

`auto`, the default, picks by the forge. The same heading starts the entry in `CHANGELOG.md`.
A rendered GitHub release:

```markdown
## [2.0.0](https://github.com/acme/app/compare/v1.4.0...v2.0.0) (2026-09-30)

### ⚠ BREAKING CHANGES

- **api:** remove the v1 endpoints ([a1b2c3d](https://github.com/acme/app/commit/a1b2c3d...))
  Clients must migrate to /v2 before upgrading.

### ✨ Features

- **ui:** add dark mode ([e4f5a6b](https://github.com/acme/app/commit/e4f5a6b...))
```

## Updating versions in files

List the files to update under `packages`. Only listed files are touched, and each one is edited
in place: formatting, key order and comments survive, and the release commit shows a one line
change.

| Type | Updates |
|------|---------|
| `package-json` | the top level `version` |
| `helm-chart` | the top level `version` and `appVersion` |
| `docker` | the tag of `image` (required), e.g. `ghcr.io/acme/app:1.4.0` |
| `plain` | the whole file becomes the version |
| `generic` | the versions marked with an annotation |

A Go module needs no file: it is versioned by its `v`-prefixed tags.

### Any file: annotations

The `generic` type updates versions wherever you mark them, in any language or format:

```html
<span>Acme v2026.09.0</span> <!-- x-magic-release-version -->
```

```yaml
image: ghcr.io/acme/app:1.4.0 # x-magic-release-version
```

```text
# x-magic-release-start-version
docker pull ghcr.io/acme/app:1.4.0
helm install app ./chart --version 1.4.0
# x-magic-release-end
```

| Annotation | Updates |
|------------|---------|
| `x-magic-release-version` | the version on the same line |
| `x-magic-release-major` / `-minor` / `-patch` | one component of the version on the same line |
| `x-magic-release-start-version` … `x-magic-release-end` | every version between the markers |
| `x-magic-release-start-major` / `-minor` / `-patch` | one component of every version in the block |

A file listed as `generic` without any annotation fails the release, so a misconfigured file is
never silently skipped. `marker` changes the annotation prefix and `pattern` the regular
expression recognising a version:

```yaml
packages:
  - path: .
    files:
      - type: generic
        path: docs/install.md
        marker: x-release-please   # keep existing release-please annotations
      - type: generic
        path: build/descriptor.txt
        pattern: '[0-9]{4}\.[0-9]{2}\.[0-9]+'
```

## Command line reference

```text
magic-releaser release [flags]
magic-releaser version
```

| Flag | Description |
|------|-------------|
| `--dry-run` | Print the next release and its notes without writing anything |
| `--versioning` | `semver` or `calver` |
| `--calver-format` | CalVer layout |
| `--timezone` | IANA timezone of the CalVer date |
| `--tag-format` | Tag format with `{{version}}` |
| `--changelog` | Changelog path; `""` disables it |
| `--config` | Config file path |
| `--mode` | `direct` or `pull-request` |
| `--manifest` | JSON file receiving the package versions |
| `--no-commit` | Leave the changed files uncommitted |
| `--no-tag` | Do not create a tag |
| `--push` | Push the release commit and the tag in one atomic push |
| `--push-branch-name` | Branch receiving the release commit; required on a detached HEAD |
| `--push-tag-only` | With `--push`, push only the tag |
| `--publish` | Create the GitHub or GitLab release |
| `--provider` | `github` or `gitlab`, when it cannot be detected |
| `--api-url` | Forge API base URL, when it cannot be detected |
| `--token` | API token; defaults to the environment |
| `--release-name` | Release title template; defaults to the [convention of the forge](#release-title-and-heading) |
| `--draft`, `--prerelease` | Passed through to the forge |
| `--output-file` | Append the result as `KEY=value` lines, e.g. `$GITHUB_OUTPUT` |
| `--include-merge-commits` | Analyze merge commits too |
| `--force-first-release` | Ignore version tags that do not match the configuration |
| `--repo` | Repository path; defaults to the current directory |

**Tokens** are read from `--token`, then `GITHUB_TOKEN` or `GH_TOKEN` for GitHub, and
`GITLAB_TOKEN` or `CI_JOB_TOKEN` for GitLab. A token is used for HTTPS remotes; an SSH remote is
pushed through the SSH agent.

## Migrating

### From semantic-release

| semantic-release | magic-releaser |
|------------------|----------------|
| `tagFormat: "v${version}"` | `tagFormat: "v{{version}}"` |
| `@semantic-release/commit-analyzer` | built in, Conventional Commits rules |
| `@semantic-release/release-notes-generator` with `presetConfig.types` | `notes.categories` |
| `@semantic-release/changelog` | `changelog: CHANGELOG.md` |
| `@semantic-release/git` | built in: the release commit |
| `@semantic-release/github`, `@semantic-release/gitlab` | `--publish` |
| `@semantic-release/npm` (version only) | `type: package-json` |
| `@semantic-release/exec` | a following CI step reading the outputs |
| `branches` | the CI trigger of the release job |

Existing `v1.2.3` tags are recognised as they are, so the next release continues from them.

### From release-please

Keep your annotations with `marker: x-release-please` on each `generic` file, and move the
version files of `release-please-config.json` to `packages`. magic-releaser releases directly on
the branch instead of through a release pull request.

### Changing the version layout

When the existing tags do not match the new configuration (for example switching from `2026.73`
to `YYYY.0M.MICRO`), the last release cannot be determined and the run stops, naming the tags.
Create one tag in the new format on the current release commit, e.g.
`git tag -a 2026.09.0 -m "calver" && git push origin 2026.09.0`, and the next release continues
from it. `--force-first-release` releases the whole history instead.

## Troubleshooting

| Message | Cause and fix |
|---------|---------------|
| `the repository is a shallow clone` | Fetch the full history: `fetch-depth: 0` on GitHub Actions, `GIT_DEPTH: 0` on GitLab CI |
| `HEAD is detached` | CI checks out a commit, not a branch: pass `--push-branch-name` (the action and the template do) |
| `found N version-like tag(s) ... that match neither` | The tags do not match the configured versioning; see [Changing the version layout](#changing-the-version-layout) |
| `push ... rejected` | The token may not push to the branch, or the branch moved; nothing was published, rerun |
| `no token found` | Set `GITHUB_TOKEN` / `GITLAB_TOKEN`, or pass `--token` |
| `tag ... already exists` | Another run released concurrently; serialize releases with `concurrency` / `resource_group` |

## Limitations

- One version per repository; independently versioned packages in a monorepo are not supported.
- No plugin system: steps after the release run as ordinary CI steps using the outputs.
- Outside CI, a self-hosted forge whose host name contains neither `github` nor `gitlab` needs
  `--provider` and `--api-url`.
- Pushing over SSH requires a running SSH agent.

## Development

```sh
go build ./...
go test -race ./...
go vet ./...
golangci-lint run ./...
./scripts/check-layering.sh
```

The code is organised in layers: `internal/release` holds the domain logic and never talks to
the network directly; `internal/publisher` (GitHub and GitLab APIs), `internal/pusher` (git
transport) and `internal/repository` (forge detection and credentials) are its ports, wired
together in `cmd/magic-releaser`. `scripts/check-layering.sh` enforces this in CI.

magic-releaser releases itself: the release workflow runs the action on `main`, and GoReleaser
attaches the binaries and publishes the container image to the release it created.

## License

[MIT](LICENSE)
