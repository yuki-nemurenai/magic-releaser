# Configuration

Every option can be set in `.magic-releaser.yaml` (or `.yml`) in the repository
root, or passed as a CLI flag. A flag always wins over the config file, and the
config file wins over the built-in default.

Unknown keys are an error, not a silently ignored setting: a typo such as
`verisoning` fails the run and points at the offending line.

## Full example

```yaml
# direct writes files, commits and tags; pull-request only prepares the changes
mode: direct

# semver or calver
versioning: calver

# only used when versioning is calver
calverFormat: YYYY.0M.MICRO

# IANA timezone the calver date is read in; UTC when absent
timezone: Europe/Moscow

# must contain the {{version}} placeholder
tagFormat: "v{{version}}"

# empty string disables the changelog
changelog: CHANGELOG.md

# message of the release commit; {{tag}} and {{version}} are substituted
releaseCommitMessage: "chore(release): {{tag}}"

# JSON file that receives the version of every package
manifest: .magic-releaser-manifest.json

# remote used for detection, pushing and links; defaults to origin
remote: origin

notes:
  preset: conventionalcommits
  showContributors: true
  # categories replaces the preset entirely
  # categories:
  #   - title: "⚠ BREAKING CHANGES"
  #     breaking: true
  #   - title: Features
  #     types: [feat]
  #   - title: Everything else
  #     types: ["*"]

packages:
  - name: root
    path: .
    changelog: CHANGELOG.md
    files:
      - type: package-json
        path: package.json
      - type: generic
        path: src/footer.html
```

## Versioning

### semver

Conventional Commits drive the bump: `feat` gives a minor release,
`fix` and `perf` a patch release, and a `!` marker or a `BREAKING CHANGE:`
footer a major release. The first release is `1.0.0`.

### calver

CalVer is **clean**: the version encodes the calendar, not the shape of the
change. The release level is still computed and reported, but it does not
influence the version, so a patch release and a breaking release inside the
same month produce the same form of version. The breaking change is carried by
the release notes instead.

The layout is configurable with `calverFormat` / `--calver-format`:

| Token | Meaning | Example |
|-------|---------|---------|
| `YYYY` | year, four digits | `2026` |
| `YY` | year, last two digits | `26` |
| `0M`, `MM` | month, zero padded | `08` |
| `M` | month | `8` |
| `0D`, `DD` | day, zero padded | `11` |
| `D` | day | `11` |
| `MICRO`, `PATCH` | release counter within the month | `3` |

Supported examples: `YYYY.0M.MICRO` (the default), `YYYY.MM.DD`, `YY.MM.MICRO`,
`YYYYMMDD`, `YYYY.MM.DD.MICRO`.

Rules:

- `MICRO` starts at `0` in a new month and increments for further releases.
- A version is never allowed to move backwards, even if the newest tag sits in
  the future.
- A daily layout such as `YYYY.MM.DD` cannot express two releases on one day.
  The tool refuses with an explanation instead of colliding with the existing
  tag; add a `MICRO` token if you need several releases per day.
- The date is read in UTC unless `timezone` / `--timezone` names an IANA zone.
  A release at 00:30 on October 1st in Moscow is still September in UTC, so
  a team away from UTC should set it.
- A CalVer with a zero padded month such as `2026.09.0` is not a valid SemVer
  (leading zero). Tools that validate SemVer, npm and Helm among them, reject
  it in `package.json` and `Chart.yaml`; use `YYYY.MM.MICRO` or `M` there.

## Release notes

Categories map Conventional Commits types to changelog sections. A commit lands
in the first matching category, and a breaking commit only ever appears in the
category with `breaking: true`.

Presets:

- `conventionalcommits` (default) — `⚠ BREAKING CHANGES`, `Features`,
  `Bug Fixes`, `Performance Improvements`, `Reverts`, `Documentation`,
  `Continuous Integration`, `Miscellaneous`, `Other Changes`.
- `angular` — the same idea with angular's titles.
- `none` — a single `Changes` section.

`categories` in the config replaces the preset completely. `types` accepts the
wildcard `*`, and `scopes` narrows a category to specific scopes.
`hidden: true` keeps a category out of the notes, and a type listed in no
category is left out as well. When the categories define no `breaking: true`
section, a `⚠ BREAKING CHANGES` section is added in front: a breaking commit is
never dropped from the notes of the release it made major.

Merge commits are excluded from the analysis and the notes unless
`--include-merge-commits` is passed. A commit containing `[skip release]` or
`[release skip]` is ignored.

When a git remote is available, the notes contain a compare link and a link per
commit. Without a remote the notes stay valid, just without links.

## Version files

Only the files listed under `packages` are touched: nothing is discovered or
guessed. Each file is edited in place, so formatting, key order and comments
survive and the release commit shows a one line change.

| Type | Effect |
|------|--------|
| `package-json` | sets the top level `version`; the key has to exist |
| `helm-chart` | sets the top level `version` and `appVersion` |
| `docker` | rewrites the tag of `image`, which is required: base images are left alone |
| `plain` | writes the bare version |
| `generic` | rewrites the versions annotated in the file |

There is no `go.mod` type: a Go module has no version field, it is versioned
by its tags alone. Use `tagFormat: "v{{version}}"` for Go modules.

A configured file that cannot be bumped (no version key, no annotation, image
not found) fails the release before anything is committed.

## Custom files: the generic type

A project often keeps its version somewhere the tool knows nothing about, a
footer in a template, a generated descriptor, a line in a Helm docs page. The
`generic` type rewrites the versions found in such a file, driven by an
annotation you place in the file itself.

```yaml
packages:
  - name: root
    path: .
    files:
      - type: generic
        path: src/app/core/components/menus/footer/footer.component.html
```

The file is marked up once, and from then on the tool keeps it in sync:

```html
<span><b>App v2026.44 &copy; Acme</b></span> <!-- x-magic-release-version -->
```

### Annotations

The default marker is `x-magic-release`. Given a marker prefix, the whole
family of annotations is derived from it.

| Annotation | Effect |
|------------|--------|
| `x-magic-release-version` | rewrites the whole version on that line |
| `x-magic-release-major` | rewrites only the first component |
| `x-magic-release-minor` | rewrites only the second component |
| `x-magic-release-patch` | rewrites only the last component |
| `x-magic-release-start-version` | opens a block, every version inside is rewritten |
| `x-magic-release-start-major`, `-minor`, `-patch` | opens a block for one component |
| `x-magic-release-end` | closes the block |

Component annotations are what make a two component CalVer such as `2026.44`
work: there `-major` is the year and `-patch` is the release counter.

The marker lines themselves are never rewritten, and a version outside an
annotated line or a block is left alone.

### A block, for files that repeat the version

```
Docker image: tms <!-- x-magic-release-start-version -->
image: registry/tms:2026.44
tag: 2026.44
<!-- x-magic-release-end -->
```

### Custom marker and custom pattern

```yaml
      - type: generic
        path: build/descriptor.txt
        marker: my-tool
        pattern: '[0-9]{4}\.[0-9]{2}\.[0-9]+'
```

`marker` replaces the default prefix, and `pattern` overrides the regexp used
to recognise a version. The default pattern covers two component CalVer, three
component SemVer and an optional pre-release suffix. The first version on an
annotated line is rewritten; the punctuation after it, such as the `-->` of an
HTML comment or a full stop, is kept.

Repositories annotated by release-please keep working by naming its prefix:

```yaml
      - type: generic
        path: path/to/file.md
        marker: x-release-please
```

A file listed in the config that carries no annotation is an error, not a
silent no-op: the file was asked for explicitly, and doing nothing quietly
would look exactly like a working setup.

## Delivery and publishing

The release is written locally first: the version files and the changelog, a
release commit containing only those files, and an annotated tag on it.

- `--push` delivers the release commit to its branch and the tag, in **one
  atomic push**: if the branch update is rejected (a protected branch, a
  concurrent push) the tag is rejected too, so the remote never carries a tag
  on a commit its branch does not have. The commit is pushed by hash, which is
  what a CI runner needs: it checks the commit out on a detached HEAD, without
  a local branch. `--push-branch-name` names the branch and is required on a
  detached HEAD; the run fails before writing anything when it is missing.
  `--push-tag-only` pushes the tag alone.
- `--publish` creates the release through the GitHub or GitLab Releases API.
  Nested GitLab groups are supported. A rerun updates the existing release,
  drafts included, instead of creating a second one.
- `--provider github|gitlab` and `--api-url` override detection. They are
  rarely needed: a host name containing `github` or `gitlab` is recognised, and
  inside a CI job the job's own server (`CI_SERVER_HOST` / `CI_API_V4_URL` on
  GitLab, `GITHUB_SERVER_URL` / `GITHUB_API_URL` on GitHub Enterprise) is used.
- `--draft` and `--prerelease` are passed through to the forge.
- `--output-file` appends `KEY=value` lines describing the result:
  `RELEASE_CREATED`, `RELEASE_VERSION`, `RELEASE_PREVIOUS_VERSION`,
  `RELEASE_TAG`, `RELEASE_COMMIT`, `RELEASE_URL`. The format is both a GitHub
  step output file and a GitLab dotenv report. `RELEASE_CREATED` is `false` for
  a dry run and when there is nothing to release.

Rate limited and temporarily failing API calls are retried with backoff,
honouring `Retry-After`. `--dry-run` never touches the network.

### Credentials

`--token`, otherwise the first set variable of:

| Provider | Variables | Sent as |
|----------|-----------|---------|
| GitHub | `GITHUB_TOKEN`, `GH_TOKEN` | `Authorization: Bearer`, push user `x-access-token` |
| GitLab | `GITLAB_TOKEN` | `Authorization: Bearer`, push user `oauth2` |
| GitLab | `CI_JOB_TOKEN` | `JOB-TOKEN` header, push user `gitlab-ci-token` |

On GitLab use a project access token with the `api` and `write_repository`
scopes, allowed to push to the release branch. The job token works only when
the project allows job tokens to push (GitLab 17.2 or later).

The token is only used for http(s) remotes. An ssh remote is pushed with the
ssh agent; key files without an agent are not read.

### Release commit and CI loops

The release commit is `chore(release): <tag>`, configurable with
`releaseCommitMessage`. It deliberately carries no `[skip ci]`: GitHub and
GitLab apply that marker to every pipeline of the commit, the tag pipeline
included, which would skip the build of the release itself. Skip the branch
pipeline of the release commit with a rule instead, as the bundled GitLab
template and GitHub workflow do.

### History

The release boundary is computed from the full history and all tags. A shallow
clone is refused with an explanation; use `fetch-depth: 0` on GitHub Actions and
`GIT_DEPTH: 0` on GitLab CI.

## Strategy switches

If the repository has version tags that match neither the configured
`versioning` nor the `tagFormat`, the release boundary is unknown and the whole
history would be released. The tool stops and names the offending tags instead.
`--force-first-release` accepts a full first release if that is really the
intent.
