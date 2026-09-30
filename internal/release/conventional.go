package release

import (
	"regexp"
	"strings"
)

var headerPattern = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9-]*)(?:\(([^()\r\n]+)\))?(!)?: (.+)$`)

// breakingMarkers are the footers that mark a breaking change. The message text
// behind the marker is the actual explanation and must survive into the notes.
var breakingMarkers = []string{"BREAKING CHANGE:", "BREAKING-CHANGE:"}

func ParseCommit(hash, message string) Commit {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	message = strings.Trim(message, "\n")

	header, _, _ := strings.Cut(message, "\n")
	commit := Commit{
		Hash:        hash,
		Header:      strings.TrimSpace(header),
		Merge:       looksLikeMerge(strings.TrimSpace(header)),
		SkipRelease: hasSkipReleaseMarker(message),
	}

	breakingBody := breakingFooterBody(message)
	commit.BreakingBody = breakingBody

	matches := headerPattern.FindStringSubmatch(commit.Header)
	if matches == nil {
		commit.Description = commit.Header
		commit.Breaking = breakingBody != ""
		return commit
	}

	commit.Type = strings.ToLower(matches[1])
	commit.Scope = matches[2]
	commit.Description = matches[4]
	commit.Breaking = matches[3] == "!" || breakingBody != ""
	return commit
}

func AnalyzeCommitsWithOptions(commits []Commit, skipMergeCommits bool) Level {
	level := ReleaseNone
	for _, commit := range commits {
		if !IncludedCommit(commit, skipMergeCommits) {
			continue
		}
		switch CommitReleaseLevel(commit) {
		case ReleaseMajor:
			return ReleaseMajor
		case ReleaseMinor:
			level = maxLevel(level, ReleaseMinor)
		case ReleasePatch:
			level = maxLevel(level, ReleasePatch)
		}
	}
	return level
}

func CommitReleaseLevel(commit Commit) Level {
	if commit.SkipRelease {
		return ReleaseNone
	}
	if commit.Breaking {
		return ReleaseMajor
	}
	switch commit.Type {
	case "feat":
		return ReleaseMinor
	case "fix", "perf":
		return ReleasePatch
	default:
		return ReleaseNone
	}
}

// IncludedCommit reports whether a commit takes part in the release decision.
// Merge commits carry no meaningful Conventional Commits header, so the
// conventionalcommits preset drops them instead of listing them as noise.
func IncludedCommit(commit Commit, skipMergeCommits bool) bool {
	if commit.SkipRelease {
		return false
	}
	if skipMergeCommits && commit.Merge {
		return false
	}
	return true
}

func breakingFooterBody(message string) string {
	lines := strings.Split(message, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, marker := range breakingMarkers {
			if !strings.HasPrefix(trimmed, marker) {
				continue
			}
			first := strings.TrimSpace(strings.TrimPrefix(trimmed, marker))
			rest := make([]string, 0, len(lines)-index)
			if first != "" {
				rest = append(rest, first)
			}
			for _, next := range lines[index+1:] {
				if strings.TrimSpace(next) == "" {
					break
				}
				rest = append(rest, strings.TrimSpace(next))
			}
			return strings.Join(rest, "\n")
		}
	}
	return ""
}

// looksLikeMerge is a cheap header check used when the parent count is not
// available, for example when a commit message is parsed outside git history.
func looksLikeMerge(header string) bool {
	return strings.HasPrefix(header, "Merge ")
}

func hasSkipReleaseMarker(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "[skip release]") || strings.Contains(lower, "[release skip]")
}

func maxLevel(left, right Level) Level {
	if right > left {
		return right
	}
	return left
}
