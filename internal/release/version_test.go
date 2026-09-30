package release

import (
	"testing"
	"time"
)

func TestNextSemVer(t *testing.T) {
	tests := []struct {
		name  string
		last  string
		level Level
		want  string
	}{
		{name: "initial", last: "", level: ReleasePatch, want: "1.0.0"},
		{name: "patch", last: "1.2.3", level: ReleasePatch, want: "1.2.4"},
		{name: "minor", last: "1.2.3", level: ReleaseMinor, want: "1.3.0"},
		{name: "major", last: "1.2.3", level: ReleaseMajor, want: "2.0.0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NextSemVer(test.last, test.level)
			if err != nil {
				t.Fatalf("NextSemVer() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("NextSemVer() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNextCalVer(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		last string
		want string
	}{
		{name: "initial", last: "", want: "2026.08.0"},
		{name: "same month increments micro", last: "2026.08.2", want: "2026.08.3"},
		{name: "new month resets micro", last: "2026.07.9", want: "2026.08.0"},
		{name: "new year resets micro", last: "2025.08.9", want: "2026.08.0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NextCalVer(DefaultCalVerFormat, test.last, now)
			if err != nil {
				t.Fatalf("NextCalVer() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("NextCalVer() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTagNameAndVersionFromTag(t *testing.T) {
	tag, err := TagName("release-{{version}}", "2026.08.0")
	if err != nil {
		t.Fatalf("TagName() error = %v", err)
	}
	if tag != "release-2026.08.0" {
		t.Fatalf("TagName() = %q, want release-2026.08.0", tag)
	}

	version, ok := VersionFromTag("release-{{version}}", tag)
	if !ok {
		t.Fatal("VersionFromTag() ok = false, want true")
	}
	if version != "2026.08.0" {
		t.Fatalf("VersionFromTag() = %q, want 2026.08.0", version)
	}
}

func TestCompareVersions(t *testing.T) {
	got, err := CompareVersionsWithCalVer(VersioningCalVer, DefaultCalVerFormat, "2026.08.10", "2026.08.2")
	if err != nil {
		t.Fatalf("CompareVersions() error = %v", err)
	}
	if got <= 0 {
		t.Fatalf("CompareVersions() = %d, want positive", got)
	}
}

func TestValidVersion(t *testing.T) {
	tests := []struct {
		name     string
		strategy Versioning
		version  string
		want     bool
	}{
		{name: "valid semver", strategy: VersioningSemVer, version: "1.2.3", want: true},
		{name: "invalid semver", strategy: VersioningSemVer, version: "2026.1", want: false},
		{name: "valid calver", strategy: VersioningCalVer, version: "2026.08.0", want: true},
		{name: "calver requires padded month and micro", strategy: VersioningCalVer, version: "2026.1", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidVersionWithCalVer(test.strategy, DefaultCalVerFormat, test.version); got != test.want {
				t.Fatalf("ValidVersion() = %v, want %v", got, test.want)
			}
		})
	}
}
