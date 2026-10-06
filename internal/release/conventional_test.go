package release

import "testing"

func TestParseCommitConventionalFields(t *testing.T) {
	commit := ParseCommit("abcdef123", "feat(api): add token refresh\n\nMore details")

	if commit.Type != "feat" {
		t.Fatalf("Type = %q, want feat", commit.Type)
	}
	if commit.Scope != "api" {
		t.Fatalf("Scope = %q, want api", commit.Scope)
	}
	if commit.Description != "add token refresh" {
		t.Fatalf("Description = %q, want add token refresh", commit.Description)
	}
	if commit.Breaking {
		t.Fatal("Breaking = true, want false")
	}
}

func TestParseCommitBreakingBang(t *testing.T) {
	commit := ParseCommit("abcdef123", "feat(api)!: change auth contract")

	if !commit.Breaking {
		t.Fatal("Breaking = false, want true")
	}
}

func TestParseCommitBreakingFooter(t *testing.T) {
	commit := ParseCommit("abcdef123", "fix: parse config\n\nBREAKING CHANGE: config schema changed")

	if !commit.Breaking {
		t.Fatal("Breaking = false, want true")
	}
}

func TestParseCommitGitRevert(t *testing.T) {
	tests := []struct {
		name            string
		message         string
		wantType        string
		wantDescription string
	}{
		{
			name:            "git revert message",
			message:         "Revert \"feat: add calver\"\n\nThis reverts commit 0288516715cdff98c645d5dd6858b95d43a007b5.",
			wantType:        "revert",
			wantDescription: "feat: add calver",
		},
		{
			name:            "revert type",
			message:         "revert: add calver\n\nThis reverts commit 0288516.",
			wantType:        "revert",
			wantDescription: "add calver",
		},
		{
			name:            "revert header without the reverted commit",
			message:         "Revert \"feat: add calver\"",
			wantType:        "",
			wantDescription: "Revert \"feat: add calver\"",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commit := ParseCommit("abcdef123", test.message)
			if commit.Type != test.wantType || commit.Description != test.wantDescription {
				t.Fatalf("ParseCommit() = (%q, %q), want (%q, %q)",
					commit.Type, commit.Description, test.wantType, test.wantDescription)
			}
		})
	}
}

func TestRevertLandsInRevertsSection(t *testing.T) {
	commit := ParseCommit("abcdef123", "Revert \"feat: add calver\"\n\nThis reverts commit 0288516.")
	for _, category := range DefaultCategories(PresetConventionalCommits) {
		if category.matches(commit) {
			if category.Title != "Reverts" {
				t.Fatalf("category = %q, want Reverts", category.Title)
			}
			return
		}
	}
	t.Fatal("no category matches the revert")
}

func TestParseCommitSkipRelease(t *testing.T) {
	commit := ParseCommit("abcdef123", "feat: add endpoint\n\n[skip release]")

	if !commit.SkipRelease {
		t.Fatal("SkipRelease = false, want true")
	}
}

func TestAnalyzeCommits(t *testing.T) {
	tests := []struct {
		name    string
		commits []Commit
		want    Level
	}{
		{
			name: "fix means patch",
			commits: []Commit{
				ParseCommit("1", "fix: repair parser"),
			},
			want: ReleasePatch,
		},
		{
			name: "feat beats fix",
			commits: []Commit{
				ParseCommit("1", "fix: repair parser"),
				ParseCommit("2", "feat: add calver"),
			},
			want: ReleaseMinor,
		},
		{
			name: "breaking beats feat",
			commits: []Commit{
				ParseCommit("1", "feat: add calver"),
				ParseCommit("2", "refactor!: redesign API"),
			},
			want: ReleaseMajor,
		},
		{
			name: "revert means patch",
			commits: []Commit{
				ParseCommit("1", "Revert \"feat: add calver\"\n\nThis reverts commit 0288516."),
			},
			want: ReleasePatch,
		},
		{
			name: "docs do not release",
			commits: []Commit{
				ParseCommit("1", "docs: update readme"),
			},
			want: ReleaseNone,
		},
		{
			name: "skip release suppresses impact",
			commits: []Commit{
				ParseCommit("1", "feat: add endpoint\n\n[release skip]"),
			},
			want: ReleaseNone,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AnalyzeCommitsWithOptions(test.commits, true); got != test.want {
				t.Fatalf("AnalyzeCommits() = %s, want %s", got, test.want)
			}
		})
	}
}
