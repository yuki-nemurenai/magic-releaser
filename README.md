# magic-releaser

[![Release](https://img.shields.io/github/v/release/yuki-nemurenai/magic-releaser?style=for-the-badge&logo=github&label=release)](https://github.com/yuki-nemurenai/magic-releaser/releases)
[![CI](https://img.shields.io/github/actions/workflow/status/yuki-nemurenai/magic-releaser/ci.yml?branch=main&style=for-the-badge&logo=githubactions&logoColor=white&label=CI)](https://github.com/yuki-nemurenai/magic-releaser/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/yuki-nemurenai/magic-releaser?style=for-the-badge&logo=go&logoColor=white)](go.mod)
[![Conventional Commits](https://img.shields.io/badge/Conventional_Commits-1.0.0-FE5196?style=for-the-badge&logo=conventionalcommits&logoColor=white)](https://www.conventionalcommits.org/)
[![GitLab CI](https://img.shields.io/badge/GitLab_CI-template-FC6D26?style=for-the-badge&logo=gitlab&logoColor=white)](templates/gitlab-ci.yml)
[![Container](https://img.shields.io/badge/ghcr.io-image-2496ED?style=for-the-badge&logo=docker&logoColor=white)](https://github.com/yuki-nemurenai/magic-releaser/pkgs/container/magic-releaser)
[![License](https://img.shields.io/github/license/yuki-nemurenai/magic-releaser?style=for-the-badge)](LICENSE)

Automated releases from [Conventional Commits](https://www.conventionalcommits.org/), with
[SemVer](https://semver.org/) or [CalVer](https://calver.org/), for GitHub and GitLab.

magic-releaser computes the next version from the commits since the last release, writes the
release notes and the changelog, updates the version in your files, commits, tags, pushes and
publishes the release. It releases on the branch directly, without a release pull request, and
ships as a single static binary.

## Features

- **SemVer and CalVer**, with configurable CalVer layouts such as `YYYY.0M.MICRO`.
- **Release notes** grouped by commit type, with links, breaking change details and configurable
  sections.
- **Version bumps in any file**: `package.json`, npm lock files, Helm charts, image tags, and any
  text file through an `x-magic-release-version` annotation.
- **Atomic delivery**: the release commit and the tag are pushed together, also from the detached
  HEAD of a CI runner.
- **GitHub and GitLab**, including GitHub Enterprise and self-hosted GitLab.

## Usage

### GitHub Actions

```yaml
on:
  push:
    branches: [main]

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0

      - uses: yuki-nemurenai/magic-releaser@v1
        id: release

      - if: steps.release.outputs.released == 'true'
        run: echo "Released ${{ steps.release.outputs.tag }}"
```

`@v1` follows the latest `1.x.y` release; pin `@v1.5.0` to review every update. Both download a
prebuilt binary and verify its checksum.

A release pushed with `GITHUB_TOKEN` does not trigger other workflows: build and deploy in the
same workflow, on `released == 'true'`, from the tag in `outputs.tag`. If `main` is protected,
pass a token allowed to push to it through the `token` input.

### GitLab CI

```yaml
include:
  - remote: https://raw.githubusercontent.com/yuki-nemurenai/magic-releaser/v1/templates/gitlab-ci.yml

release:
  extends: .magic-releaser # releases the default branch on push
  stage: release
```

Store a project or group access token with the `api` and `write_repository` scopes as the masked
CI/CD variable `GITLAB_TOKEN`. The release tag starts its own pipeline, which builds and deploys
the release. `.magic-releaser-preview` shows the next release in merge requests.

### Command line

```sh
magic-releaser release --dry-run          # preview the next release, write nothing
magic-releaser release --push --publish   # release, push and publish
```

Binaries are available from [Releases](https://github.com/yuki-nemurenai/magic-releaser/releases),
the image as `ghcr.io/yuki-nemurenai/magic-releaser`, and the source with
`go install github.com/yuki-nemurenai/magic-releaser/cmd/magic-releaser@latest`. See the
[command line reference](docs/configuration.md#command-line).

## Inputs

| Input | Default | Description |
| --- | --- | --- |
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

## Outputs

| Output | GitLab dotenv | Description |
| --- | --- | --- |
| `released` | `RELEASE_CREATED` | `true` if a release was created |
| `version` | `RELEASE_VERSION` | Released version, e.g. `1.5.0` |
| `previous-version` | `RELEASE_PREVIOUS_VERSION` | Version of the previous release |
| `tag` | `RELEASE_TAG` | Release tag, e.g. `v1.5.0` |
| `release-url` | `RELEASE_URL` | URL of the GitHub or GitLab release |
| `release-commit` | `RELEASE_COMMIT` | SHA of the release commit |

## Configuration

`.magic-releaser.yaml` in the repository root. Every key is optional; command line flags take
precedence, and unknown keys are an error.

```yaml
versioning: calver
calverFormat: YYYY.0M.MICRO
timezone: Europe/Moscow
tagFormat: "{{version}}"
changelog: CHANGELOG.md

packages:
  - path: .
    files:
      - type: package-json
        path: package.json
```

| Key | Default | Description |
| --- | --- | --- |
| `versioning` | `semver` | `semver` or `calver` |
| `calverFormat` | `YYYY.0M.MICRO` | CalVer layout |
| `timezone` | `UTC` | IANA timezone the CalVer date is read in |
| `tagFormat` | `v{{version}}` | Tag name, with a `{{version}}` placeholder |
| `changelog` | `CHANGELOG.md` | Changelog file; `""` disables it |
| `releaseCommitMessage` | `chore(release): {{tag}}` | Message of the release commit |
| `releaseName` | by forge | Release title: the tag on GitHub, `Release {{version}}` on GitLab |
| `requirePreviousRelease` | `false` | Fail instead of making a first release from the whole history |
| `notes` | | [Release notes](#release-notes) layout |
| `packages` | | [Files](#version-files) to update |

The full reference is in [docs/configuration.md](docs/configuration.md).

## Versioning

| Commit | SemVer | CalVer `YYYY.0M.MICRO` |
| --- | --- | --- |
| `fix:`, `perf:` | `1.4.0` → `1.4.1` | `2026.10.0` → `2026.10.1` |
| `feat:` | `1.4.0` → `1.5.0` | `2026.10.0` → `2026.10.1` |
| `feat!:`, `BREAKING CHANGE:` footer | `1.4.0` → `2.0.0` | `2026.10.0` → `2026.10.1` |
| `docs:`, `chore:`, `ci:`, … | no release | no release |

CalVer encodes the release date; the counter resets every period. Layouts are built from `YYYY`,
`YY`, `0M`, `MM`, `M`, `0D`, `DD`, `D` and `MICRO`, for example `YY.MM.MICRO` or `YYYY.MM.DD`.

## Version files

| Type | Updates |
| --- | --- |
| `package-json` | `version` in `package.json` |
| `package-lock` | the root package version in `package-lock.json` |
| `helm-chart` | `version` and `appVersion` in `Chart.yaml` |
| `docker` | the tag of a given `image` |
| `plain` | the whole file, e.g. `version.txt` |
| `generic` | versions marked with an annotation, in any file |

```html
<span>v1.4.0</span> <!-- x-magic-release-version -->
```

Files are edited in place: formatting and key order are kept. Existing release-please annotations
work with `marker: x-release-please`.

## Release notes

Commits are grouped into sections, by default the
[Conventional Commits](https://www.conventionalcommits.org/) preset. `notes.categories` defines
your own sections, titles and visibility:

```yaml
notes:
  categories:
    - title: "✨ Features"
      types: [feat]
    - title: "🐛 Bug Fixes"
      types: [fix]
    - title: "🧹 Chores"
      types: [chore]
      hidden: true
```

Headings follow the forge: `## [1.5.0](compare) (2026-10-02)` on GitHub, as semantic-release, and
`## [1.5.0] - 2026-10-02` on GitLab, as git-cliff. Breaking changes always get their own section.

## How it works

1. Finds the last release tag reachable from `HEAD`.
2. Reads the Conventional Commits since that tag. Without `feat`, `fix`, `perf` or breaking
   commits there is nothing to release.
3. Computes the next version, never reusing a tag that already exists.
4. Writes the notes and the changelog, and updates the version files.
5. Commits them as `chore(release): <tag>`, tags the commit, and pushes both in one atomic push.
6. Publishes the release on GitHub or GitLab.

## Migrating

| From | To |
| --- | --- |
| release-please `release-type: simple` | `type: plain` for `version.txt` |
| release-please `release-type: node` | `type: package-json` and `type: package-lock` |
| semantic-release `presetConfig.types` | `notes.categories` |
| semantic-release `@semantic-release/changelog` | `changelog: CHANGELOG.md` |
| semantic-release `@semantic-release/exec` | a CI step reading the outputs |

Existing `vX.Y.Z` tags and `CHANGELOG.md` continue as they are. See
[docs/configuration.md](docs/configuration.md#strategy-switches) to change the tag layout of an
existing repository.

## License

[MIT](LICENSE)
