package release

import (
	"strings"
	"testing"

	"github.com/yuki-nemurenai/go-magic-releaser/internal/repository"
)

func TestGenerateNotesGroupsCommits(t *testing.T) {
	commits := []Commit{
		ParseCommit("abcdef1234567", "feat(api): add calver"),
		ParseCommit("1234567890abc", "fix: repair changelog"),
		ParseCommit("fedcba9876543", "refactor!: change release API"),
		ParseCommit("aaaabbbbcccc", "perf: cache lookups"),
		ParseCommit("ddddeeeeffff", "docs: explain usage"),
	}

	notes := GenerateNotes(NotesOptions{
		Version:          "2026.08.0",
		Date:             releaseNow,
		Commits:          commits,
		SkipMergeCommits: true,
	})

	for _, want := range []string{
		"## 2026.08.0 - 2026-08-11",
		"### ⚠ BREAKING CHANGES",
		"change release API",
		"### Features",
		"**api:** add calver",
		"### Bug Fixes",
		"repair changelog",
		"### Performance Improvements",
		"cache lookups",
		"### Documentation",
		"explain usage",
	} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes do not contain %q:\n%s", want, notes)
		}
	}
}

// A breaking commit belongs to the breaking group only, never twice.
func TestBreakingCommitAppearsOnce(t *testing.T) {
	commits := []Commit{
		ParseCommit("aaaaaaaaaaaa", "feat!: drop endpoint"),
	}
	notes := GenerateNotes(NotesOptions{
		Version: "2.0.0",
		Date:    releaseNow,
		Commits: commits,
	})
	if strings.Count(notes, "drop endpoint") != 1 {
		t.Fatalf("breaking commit listed more than once:\n%s", notes)
	}
	if strings.Contains(notes, "### Features") {
		t.Fatalf("breaking commit leaked into the Features group:\n%s", notes)
	}
}

func TestNotesCarryForgeLinks(t *testing.T) {
	commits := []Commit{
		ParseCommit("abcdef1234567", "feat(api): add calver"),
	}
	notes := GenerateNotes(NotesOptions{
		Version: "1.3.0",
		Date:    releaseNow,
		Commits: commits,
		Repository: repository.Info{
			Provider: repository.ProviderGitHub,
			Host:     "github.com",
			Slug:     "octo/demo",
			WebURL:   "https://github.com/octo/demo",
		},
		CompareFrom: "v1.2.3",
		CompareTo:   "v1.3.0",
	})

	if !strings.Contains(notes, "https://github.com/octo/demo/compare/v1.2.3...v1.3.0") {
		t.Fatalf("compare link missing:\n%s", notes)
	}
	if !strings.Contains(notes, "https://github.com/octo/demo/commit/abcdef1234567") {
		t.Fatalf("commit link missing:\n%s", notes)
	}
}

// Without a remote the notes stay valid, just without links.
func TestNotesWithoutRepositoryHaveNoLinks(t *testing.T) {
	commits := []Commit{ParseCommit("abcdef1234567", "feat: add calver")}
	notes := GenerateNotes(NotesOptions{
		Version: "1.3.0",
		Date:    releaseNow,
		Commits: commits,
	})
	if strings.Contains(notes, "http") {
		t.Fatalf("unexpected link without a repository:\n%s", notes)
	}
	if !strings.Contains(notes, "(abcdef1)") {
		t.Fatalf("plain hash expected:\n%s", notes)
	}
}

func TestNotesListContributors(t *testing.T) {
	first := ParseCommit("aaaaaaaaaaaa", "feat: one")
	first.AuthorName = "Alice"
	second := ParseCommit("bbbbbbbbbbbb", "fix: two")
	second.AuthorName = "Bob"
	third := ParseCommit("cccccccccccc", "fix: three")
	third.AuthorName = "Alice"

	notes := GenerateNotes(NotesOptions{
		Version:          "1.3.0",
		Date:             releaseNow,
		Commits:          []Commit{first, second, third},
		ShowContributors: true,
	})
	if !strings.Contains(notes, "### Contributors") {
		t.Fatalf("contributors section missing:\n%s", notes)
	}
	if strings.Count(notes, "Alice") != 1 {
		t.Fatalf("contributor should be listed once:\n%s", notes)
	}
	if !strings.Contains(notes, "Bob") {
		t.Fatalf("contributor Bob missing:\n%s", notes)
	}
}

// Custom categories replace the preset.
func TestCustomCategoriesOverridePreset(t *testing.T) {
	commits := []Commit{
		ParseCommit("aaaaaaaaaaaa", "feat: user facing"),
		ParseCommit("bbbbbbbbbbbb", "infra: resize pool"),
	}
	notes := GenerateNotes(NotesOptions{
		Version: "1.0.0",
		Date:    releaseNow,
		Commits: commits,
		Categories: []Category{
			{Title: "Infrastructure", Types: []string{"infra"}},
			{Title: "Everything else", Types: []string{"*"}},
		},
	})
	if !strings.Contains(notes, "### Infrastructure") || !strings.Contains(notes, "resize pool") {
		t.Fatalf("custom category missing:\n%s", notes)
	}
	if !strings.Contains(notes, "### Everything else") || !strings.Contains(notes, "user facing") {
		t.Fatalf("fallback category missing:\n%s", notes)
	}
}

func TestAngularPresetUsesItsOwnTitles(t *testing.T) {
	commits := []Commit{ParseCommit("aaaaaaaaaaaa", "fix: repair parser")}
	notes := GenerateNotes(NotesOptions{
		Version:    "1.0.1",
		Date:       releaseNow,
		Commits:    commits,
		Categories: DefaultCategories(PresetAngular),
	})
	if !strings.Contains(notes, "### Bug Fixes") {
		t.Fatalf("angular preset title missing:\n%s", notes)
	}
}

func TestHiddenCategoryIsNotRendered(t *testing.T) {
	commits := []Commit{ParseCommit("aaaaaaaaaaaa", "feat: one")}
	notes := GenerateNotes(NotesOptions{
		Version: "1.0.0",
		Date:    releaseNow,
		Commits: commits,
		Categories: []Category{
			{Title: "Secret", Types: []string{"feat"}, Hidden: true},
			{Title: "Everything else", Types: []string{"*"}},
		},
	})
	if strings.Contains(notes, "### Secret") {
		t.Fatalf("hidden category rendered:\n%s", notes)
	}
}

func TestCategoryScopesFilter(t *testing.T) {
	commit := ParseCommit("aaaaaaaaaaaa", "fix(api): repair parser")
	category := Category{Title: "API", Types: []string{"fix"}, Scopes: []string{"api"}}
	if !category.matches(commit) {
		t.Fatal("category should match a commit with the api scope")
	}
	other := ParseCommit("bbbbbbbbbbbb", "fix(ui): repair parser")
	if category.matches(other) {
		t.Fatal("category should not match a different scope")
	}
}

// A layout with one section per type and no breaking section, the usual shape
// of an emoji styled changelog, must still show the breaking change that made
// the release major.
func TestCustomCategoriesWithoutABreakingSectionKeepBreakingChanges(t *testing.T) {
	notes := GenerateNotes(NotesOptions{
		Version: "2.0.0",
		Categories: []Category{
			{Title: "✨ Features", Types: []string{"feat"}},
			{Title: "🧹 Chores", Types: []string{"chore"}, Hidden: true},
		},
		Commits: []Commit{
			ParseCommit("a1", "feat!: drop the v1 API\n\nBREAKING CHANGE: clients must migrate"),
			ParseCommit("b2", "feat: add export"),
			ParseCommit("c3", "chore: bump deps"),
		},
	})
	for _, want := range []string{"### ⚠ BREAKING CHANGES", "drop the v1 API", "clients must migrate", "### ✨ Features", "add export"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "Chores") || strings.Contains(notes, "bump deps") {
		t.Fatalf("hidden section was rendered:\n%s", notes)
	}
}
