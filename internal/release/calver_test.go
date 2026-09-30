package release

import (
	"strings"
	"testing"
	"time"
)

// Every documented CalVer layout has to render and round trip.
func TestCalVerLayoutsRoundTrip(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		layout string
		last   string
		want   string
	}{
		{"YYYY.0M.MICRO", "", "2026.08.0"},
		{"YYYY.0M.MICRO", "2026.08.3", "2026.08.4"},
		{"YYYY.0M.MICRO", "2026.07.9", "2026.08.0"},
		{"YYYY.MM.MICRO", "2026.07.9", "2026.08.0"},
		{"YYYY.MM.MICRO", "2026.08.0", "2026.08.1"},
		{"YY.MM.MICRO", "26.07.9", "26.08.0"},
		{"YYYYMMDD", "", "20260811"},
		{"YYYY.MM.DD", "", "2026.08.11"},
		{"YYYY.MM.DD", "2026.08.01", "2026.08.11"},
		{"YYYY.MM.DD.MICRO", "2026.08.11.0", "2026.08.11.1"},
		{"YYYY.M.D", "", "2026.8.11"},
		{"YYYY0MMICRO", "2026079", "2026080"},
		{"YYYY0MMICRO", "2026082", "2026083"},
		{"release-YYYY.MM", "", "release-2026.08"},
	}

	for _, test := range tests {
		t.Run(test.layout+"/"+test.last, func(t *testing.T) {
			got, err := NextCalVer(test.layout, test.last, now)
			if err != nil {
				t.Fatalf("NextCalVer(%q, %q) error = %v", test.layout, test.last, err)
			}
			if got != test.want {
				t.Fatalf("NextCalVer(%q, %q) = %q, want %q", test.layout, test.last, got, test.want)
			}
			if _, err := parseCalVer(test.layout, got); err != nil {
				t.Fatalf("parseCalVer(%q, %q) error = %v", test.layout, got, err)
			}
		})
	}
}

// A daily layout cannot express two releases on the same day, and must say so
// instead of colliding with the existing tag.
func TestDailyLayoutRejectsSecondReleaseInADay(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	_, err := NextCalVer("YYYY.MM.DD", "2026.08.11", now)
	if err == nil {
		t.Fatal("NextCalVer() error = nil, want a same-day conflict")
	}
	if !strings.Contains(err.Error(), "MICRO") {
		t.Fatalf("error should suggest adding a MICRO token, got: %v", err)
	}

	// The same layout with a MICRO token is fine.
	got, err := NextCalVer("YYYY.MM.DD.MICRO", "2026.08.11.0", now)
	if err != nil {
		t.Fatalf("NextCalVer() error = %v", err)
	}
	if got != "2026.08.11.1" {
		t.Fatalf("got %q, want 2026.08.11.1", got)
	}
}

func TestCalVerLayoutValidation(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	for _, layout := range []string{"", "MICRO.MICRO", "version"} {
		if _, err := NextCalVer(layout, "", now); err == nil {
			t.Fatalf("NextCalVer(%q) error = nil, want a validation error", layout)
		}
	}
}

func TestParseCalVerRejectsWrongLayout(t *testing.T) {
	if _, err := parseCalVer("YYYY.MM.DD", "2026.08"); err == nil {
		t.Fatal("ParseCalVer accepted a value that does not match the layout")
	}
	if _, err := parseCalVer("YYYY.0M.MICRO", "2026.13.0"); err == nil {
		t.Fatal("ParseCalVer accepted month 13")
	}
	if _, err := parseCalVer("YYYY.0M.MICRO", "2026.00.0"); err == nil {
		t.Fatal("ParseCalVer accepted month 0")
	}
}

func TestCalVerCompareIsLayoutAware(t *testing.T) {
	compare, err := CompareVersionsWithCalVer(VersioningCalVer, "YYYY.0M.MICRO", "2026.09.1", "2026.08.4")
	if err != nil {
		t.Fatalf("CompareVersions() error = %v", err)
	}
	if compare <= 0 {
		t.Fatalf("compare = %d, want positive", compare)
	}

	// Under a daily layout these values are not versions at all.
	if _, err := CompareVersionsWithCalVer(VersioningCalVer, "YYYY.MM.DD", "2026.09.1", "2026.08.4"); err == nil {
		t.Fatal("CompareVersions accepted values from a different layout")
	}
}

func TestValidVersionIsLayoutAware(t *testing.T) {
	if !ValidVersionWithCalVer(VersioningCalVer, "YYYY.0M.MICRO", "2026.08.0") {
		t.Fatal("default layout should accept 2026.08.0")
	}
	if ValidVersionWithCalVer(VersioningCalVer, "YYYY.MM.DD", "2026.08.0") {
		t.Fatal("daily layout should reject 2026.08.0")
	}
	if !ValidVersionWithCalVer(VersioningCalVer, "YYYY.MM.DD", "2026.08.11") {
		t.Fatal("daily layout should accept 2026.08.11")
	}
}

// The default layout keeps the historical YYYY.0M.MICRO shape.
func TestDefaultCalVerFormatIsUnchanged(t *testing.T) {
	got, err := NextCalVer(DefaultCalVerFormat, "", time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NextCalVer() error = %v", err)
	}
	if got != "2026.08.0" {
		t.Fatalf("got %q, want 2026.08.0", got)
	}
}

// A layout that omits a component must not be range checked on that component:
// YYYY.PATCH has no month, so a parsed value carries month zero legitimately.
func TestCalVerLayoutsWithoutMonthOrDay(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		layout string
		last   string
		want   string
	}{
		{"YYYY.PATCH", "", "2026.0"},
		{"YYYY.PATCH", "2026.73", "2026.74"},
		{"YYYY.PATCH", "2026.9", "2026.10"},
		{"YYYY.PATCH", "2025.99", "2026.0"},
		{"YY.PATCH", "26.5", "26.6"},
	}
	for _, test := range tests {
		t.Run(test.layout+"/"+test.last, func(t *testing.T) {
			if test.last != "" && !ValidVersionWithCalVer(VersioningCalVer, test.layout, test.last) {
				t.Fatalf("ValidVersionWithCalVer(%q, %q) = false", test.layout, test.last)
			}
			got, err := NextCalVer(test.layout, test.last, now)
			if err != nil {
				t.Fatalf("NextCalVer(%q, %q) error = %v", test.layout, test.last, err)
			}
			if got != test.want {
				t.Fatalf("NextCalVer(%q, %q) = %q, want %q", test.layout, test.last, got, test.want)
			}
			if _, err := parseCalVer(test.layout, got); err != nil {
				t.Fatalf("parseCalVer(%q, %q) error = %v", test.layout, got, err)
			}
		})
	}
}

func TestParseCalVerStillValidatesPresentComponents(t *testing.T) {
	if _, err := parseCalVer("YYYY.0M.PATCH", "2026.13.1"); err == nil {
		t.Fatal("ParseCalVer accepted month 13 in a layout that has a month")
	}
	if _, err := parseCalVer("YYYY.0M.0D.PATCH", "2026.08.32.1"); err == nil {
		t.Fatal("ParseCalVer accepted day 32 in a layout that has a day")
	}
}
