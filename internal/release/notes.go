package release

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yuki-nemurenai/magic-releaser/internal/repository"
)

// Category maps Conventional Commits types to a changelog section.
// A commit lands in exactly one category: the first one that matches.
type Category struct {
	Title    string   `yaml:"title"`
	Types    []string `yaml:"types"`
	Scopes   []string `yaml:"scopes"`
	Breaking bool     `yaml:"breaking"`
	Hidden   bool     `yaml:"hidden"`
}

type Preset string

const (
	PresetConventionalCommits Preset = "conventionalcommits"
	PresetAngular             Preset = "angular"
	PresetNone                Preset = "none"
)

// NotesStyle selects how the heading of a release reads. Each forge has its
// own convention, so the default follows the forge.
type NotesStyle string

const (
	// NotesStyleAuto picks the convention of the forge.
	NotesStyleAuto NotesStyle = "auto"
	// NotesStyleConventionalChangelog is the semantic-release heading on
	// GitHub: "## [1.2.0](compare) (2026-10-01)", titled by the tag.
	NotesStyleConventionalChangelog NotesStyle = "conventional-changelog"
	// NotesStyleKeepAChangelog is the Keep a Changelog heading of git-cliff:
	// "## [1.2.0] - 2026-10-01" with a compare link below, titled
	// "Release 1.2.0".
	NotesStyleKeepAChangelog NotesStyle = "keep-a-changelog"
)

// ResolveNotesStyle turns auto into the convention of the provider: the
// semantic-release layout on GitHub, Keep a Changelog elsewhere.
func ResolveNotesStyle(style NotesStyle, provider repository.Provider) NotesStyle {
	if style != "" && style != NotesStyleAuto {
		return style
	}
	if provider == repository.ProviderGitHub {
		return NotesStyleConventionalChangelog
	}
	return NotesStyleKeepAChangelog
}

// DefaultCategories returns the section layout of a preset. The group titles
// follow semantic-release so existing changelogs stay comparable.
func DefaultCategories(preset Preset) []Category {
	switch preset {
	case PresetAngular:
		return []Category{
			{Title: "⚠ BREAKING CHANGES", Breaking: true},
			{Title: "Features", Types: []string{"feat"}},
			{Title: "Bug Fixes", Types: []string{"fix"}},
			{Title: "Performance Improvements", Types: []string{"perf"}},
			{Title: "Reverts", Types: []string{"revert"}},
			{Title: "Documentation", Types: []string{"docs", "doc"}},
			{Title: "Other Changes", Types: []string{"*"}},
		}
	case PresetNone:
		return []Category{
			{Title: "Changes", Types: []string{"*"}},
		}
	default:
		return []Category{
			{Title: DefaultBreakingTitle, Breaking: true},
			{Title: "Features", Types: []string{"feat"}},
			{Title: "Bug Fixes", Types: []string{"fix"}},
			{Title: "Performance Improvements", Types: []string{"perf"}},
			{Title: "Reverts", Types: []string{"revert"}},
			{Title: "Documentation", Types: []string{"docs", "doc"}},
			{Title: "Continuous Integration", Types: []string{"ci"}},
			{Title: "Miscellaneous", Types: []string{"chore", "style", "refactor", "test", "build"}},
			{Title: "Other Changes", Types: []string{"*"}},
		}
	}
}

func (category Category) matches(commit Commit) bool {
	if category.Breaking != commit.Breaking {
		return false
	}
	if category.Breaking {
		return true
	}
	if len(category.Scopes) > 0 && !containsFold(category.Scopes, commit.Scope) {
		return false
	}
	for _, candidate := range category.Types {
		if candidate == "*" || strings.EqualFold(candidate, commit.Type) {
			return true
		}
	}
	return false
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

// NotesOptions carries everything the release notes need beyond the commits.
type NotesOptions struct {
	Version          string
	Date             time.Time
	Commits          []Commit
	SkipMergeCommits bool
	Categories       []Category
	Repository       repository.Info
	CompareFrom      string
	CompareTo        string
	ShowContributors bool
	// Style is the heading layout; empty means Keep a Changelog.
	Style NotesStyle
}

// GenerateNotes renders the release notes body for a release.
//
// Missing repository information is not an error: the notes are still valid,
// they simply carry plain hashes instead of links.
func GenerateNotes(options NotesOptions) string {
	var builder strings.Builder
	writeHeading(&builder, options)

	categories := options.Categories
	if len(categories) == 0 {
		categories = DefaultCategories(PresetConventionalCommits)
	}
	categories = withBreakingCategory(categories)
	grouped := groupCommits(options.Commits, categories, options.SkipMergeCommits)

	for _, category := range categories {
		if category.Hidden {
			continue
		}
		writeCategory(&builder, category, grouped[category.Title], options.Repository)
	}

	if options.ShowContributors {
		writeContributors(&builder, options.Commits, options.SkipMergeCommits)
	}

	return strings.TrimRight(builder.String(), "\n") + "\n"
}

// DefaultBreakingTitle is the section of breaking changes when the layout does
// not define its own.
const DefaultBreakingTitle = "⚠ BREAKING CHANGES"

// withBreakingCategory guarantees a section for breaking changes. A breaking
// commit only ever lands in a breaking category, so a custom layout without
// one, such as a list of emoji sections per type, would silently drop exactly
// the commits that caused a major release.
func withBreakingCategory(categories []Category) []Category {
	for _, category := range categories {
		if category.Breaking {
			return categories
		}
	}
	result := make([]Category, 0, len(categories)+1)
	result = append(result, Category{Title: DefaultBreakingTitle, Breaking: true})
	return append(result, categories...)
}

// writeHeading renders the release heading in the requested style. The same
// text heads CHANGELOG.md, so both read like the rest of the ecosystem.
func writeHeading(builder *strings.Builder, options NotesOptions) {
	date := options.Date.Format("2006-01-02")
	compare := options.Repository.CompareURL(options.CompareFrom, options.CompareTo)
	if options.Style == NotesStyleConventionalChangelog {
		// semantic-release links the version itself to the comparison, and the
		// first release has nothing to compare against.
		if compare != "" && options.CompareFrom != "" {
			fmt.Fprintf(builder, "## [%s](%s) (%s)\n\n", options.Version, compare, date)
			return
		}
		fmt.Fprintf(builder, "## %s (%s)\n\n", options.Version, date)
		return
	}
	fmt.Fprintf(builder, "## [%s] - %s\n\n", options.Version, date)
	if compare != "" {
		fmt.Fprintf(builder, "Full changelog: %s\n\n", compare)
	}
}

// groupCommits assigns every commit to the first matching category.
func groupCommits(commits []Commit, categories []Category, skipMergeCommits bool) map[string][]Commit {
	grouped := map[string][]Commit{}
	for _, commit := range commits {
		if !IncludedCommit(commit, skipMergeCommits) {
			continue
		}
		for _, category := range categories {
			if category.matches(commit) {
				grouped[category.Title] = append(grouped[category.Title], commit)
				break
			}
		}
	}
	return grouped
}

func writeCategory(builder *strings.Builder, category Category, commits []Commit, repo repository.Info) {
	if len(commits) == 0 {
		return
	}
	fmt.Fprintf(builder, "### %s\n\n", category.Title)
	for _, commit := range commits {
		fmt.Fprintf(builder, "- %s", formatCommitSubject(commit))
		if reference := formatReference(commit, repo); reference != "" {
			fmt.Fprintf(builder, " (%s)", reference)
		}
		builder.WriteString("\n")
		// The BREAKING CHANGE footer carries the actual explanation, so it is
		// rendered as the body of the entry rather than dropped.
		if commit.Breaking && commit.BreakingBody != "" {
			for _, line := range strings.Split(commit.BreakingBody, "\n") {
				fmt.Fprintf(builder, "  %s\n", strings.TrimSpace(line))
			}
		}
	}
	builder.WriteString("\n")
}

func writeContributors(builder *strings.Builder, commits []Commit, skipMergeCommits bool) {
	seen := map[string]bool{}
	contributors := make([]Commit, 0, len(commits))
	for _, commit := range commits {
		if !IncludedCommit(commit, skipMergeCommits) {
			continue
		}
		name := strings.TrimSpace(commit.AuthorName)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		contributors = append(contributors, commit)
	}
	if len(contributors) == 0 {
		return
	}
	sort.Slice(contributors, func(i, j int) bool {
		return contributors[i].AuthorName < contributors[j].AuthorName
	})

	fmt.Fprintf(builder, "### Contributors\n\n")
	for _, commit := range contributors {
		email := strings.TrimSpace(commit.AuthorEmail)
		switch {
		case email != "" && strings.Contains(email, "@"):
			fmt.Fprintf(builder, "- %s <%s>\n", commit.AuthorName, email)
		case email != "":
			fmt.Fprintf(builder, "- %s (%s)\n", commit.AuthorName, email)
		default:
			fmt.Fprintf(builder, "- %s\n", commit.AuthorName)
		}
	}
	builder.WriteString("\n")
}

// formatReference renders the commit hash as a link when the forge is known.
func formatReference(commit Commit, repo repository.Info) string {
	short := shortHash(commit.Hash)
	if short == "" {
		return ""
	}
	if url := repo.CommitURL(commit.Hash); url != "" {
		return fmt.Sprintf("[%s](%s)", short, url)
	}
	return short
}

func formatCommitSubject(commit Commit) string {
	description := commit.Description
	if description == "" {
		description = commit.Header
	}
	if commit.Scope != "" {
		return fmt.Sprintf("**%s:** %s", commit.Scope, description)
	}
	return description
}

func shortHash(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}
