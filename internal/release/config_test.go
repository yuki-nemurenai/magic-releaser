package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".magic-releaser.yaml")
	data := []byte(`mode: pull-request
versioning: calver
tagFormat: "release-{{version}}"
changelog: HISTORY.md
manifest: .magic-releaser-manifest.json
packages:
  - name: api
    path: services/api
    files:
      - type: package-json
        path: package.json
`)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	config, path, err := LoadConfig(dir, "")
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if path != configPath {
		t.Fatalf("path = %q, want %q", path, configPath)
	}
	if config.Mode != ModePullRequest {
		t.Fatalf("Mode = %q, want pull-request", config.Mode)
	}
	if config.Versioning != VersioningCalVer {
		t.Fatalf("Versioning = %q, want calver", config.Versioning)
	}
	if len(config.Packages) != 1 {
		t.Fatalf("len(Packages) = %d, want 1", len(config.Packages))
	}
	if config.Packages[0].Files[0].Type != "package-json" {
		t.Fatalf("file type = %q, want package-json", config.Packages[0].Files[0].Type)
	}
}

// An unknown key must fail the config load instead of being ignored, otherwise
// a typo silently disables a setting that looks like it took effect.
func TestReadConfigRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".magic-releaser.yaml")
	if err := os.WriteFile(path, []byte("versioning: calver\nverisoning: semver\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := readConfig(path)
	if err == nil {
		t.Fatal("readConfig() error = nil, want an unknown field error")
	}
	if !strings.Contains(err.Error(), "verisoning") {
		t.Fatalf("error should name the unknown field, got: %v", err)
	}
}

// The removed per package versioning keys must now be reported, not accepted.
func TestReadConfigRejectsRemovedPackageKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".magic-releaser.yaml")
	content := "packages:\n  - name: root\n    path: .\n    versioning: calver\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := readConfig(path)
	if err == nil {
		t.Fatal("readConfig() error = nil, want the removed key to be rejected")
	}
}

func TestReadConfigAcceptsSupportedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".magic-releaser.yaml")
	content := `versioning: calver
calverFormat: YYYY.MM.DD
timezone: Asia/Tomsk
requirePreviousRelease: true
tagFormat: "v{{version}}"
changelog: CHANGELOG.md
releaseCommitMessage: "chore(release): {{tag}}"
releaseName: "Release {{version}}"
manifest: versions.json
remote: origin
notes:
  preset: conventionalcommits
  style: keep-a-changelog
  showContributors: true
  categories:
    - title: Features
      types: [feat]
      scopes: [api]
      hidden: false
packages:
  - name: root
    path: .
    changelog: CHANGELOG.md
    files:
      - type: package-json
        path: package.json
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	config, err := readConfig(path)
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if config.CalVerFormat != "YYYY.MM.DD" {
		t.Fatalf("CalVerFormat = %q", config.CalVerFormat)
	}
	if !config.RequirePreviousRelease || config.Timezone != "Asia/Tomsk" || config.ReleaseName != "Release {{version}}" ||
		config.ReleaseCommitMessage != "chore(release): {{tag}}" {
		t.Fatalf("config = %+v", config)
	}
	if config.Notes.Style != NotesStyleKeepAChangelog {
		t.Fatalf("Notes.Style = %q", config.Notes.Style)
	}
	if !config.Notes.ShowContributors {
		t.Fatal("ShowContributors = false, want true")
	}
	if len(config.Notes.Categories) != 1 {
		t.Fatalf("len(Categories) = %d, want 1", len(config.Notes.Categories))
	}
	if len(config.Packages) != 1 || config.Packages[0].Changelog != "CHANGELOG.md" {
		t.Fatalf("Packages = %+v", config.Packages)
	}
}

// A config key that is present but empty has to disable the changelog, exactly
// like the --changelog "" flag does, for repositories that publish the notes
// only as forge releases.
func TestConfigCanDisableTheChangelog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".magic-releaser.yaml")
	if err := os.WriteFile(path, []byte("versioning: calver\nchangelog: \"\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	config, err := readConfig(path)
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if config.Changelog == nil {
		t.Fatal("Changelog = nil, want a pointer to an empty string")
	}
	if *config.Changelog != "" {
		t.Fatalf("Changelog = %q, want empty", *config.Changelog)
	}

	options := MergeConfig(Options{}, config)
	if options.Changelog != "" {
		t.Fatalf("Changelog = %q, want empty", options.Changelog)
	}
	if !options.ChangelogSet {
		t.Fatal("ChangelogSet = false, want true so the default is not re-applied")
	}
	options = withDefaults(options)
	if options.Changelog != "" {
		t.Fatalf("after defaults Changelog = %q, want empty", options.Changelog)
	}
}

// An absent key must not count as an explicit empty, or the default would
// never apply.
func TestConfigWithoutChangelogKeepsTheDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".magic-releaser.yaml")
	if err := os.WriteFile(path, []byte("versioning: calver\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	config, err := readConfig(path)
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if config.Changelog != nil {
		t.Fatal("Changelog should be nil when the key is absent")
	}
	options := withDefaults(MergeConfig(Options{}, config))
	if options.Changelog != "CHANGELOG.md" {
		t.Fatalf("Changelog = %q, want the CHANGELOG.md default", options.Changelog)
	}
}

// A flag has to win over the config.
func TestChangelogFlagWinsOverConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".magic-releaser.yaml")
	if err := os.WriteFile(path, []byte("changelog: HISTORY.md\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	config, err := readConfig(path)
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	options := withDefaults(MergeConfig(Options{Changelog: "OTHER.md", ChangelogSet: true}, config))
	if options.Changelog != "OTHER.md" {
		t.Fatalf("Changelog = %q, want the flag value OTHER.md", options.Changelog)
	}
}
