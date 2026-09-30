package release

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// DefaultMarker is the annotation prefix the project uses for itself.
// Release-please annotations stay supported by setting marker: x-release-please,
// which is what migrating repositories need.
const DefaultMarker = "x-magic-release"

// ReleasePleaseMarker is the prefix of the release-please annotation family,
// supported for repositories annotated by that tool.
const ReleasePleaseMarker = "x-release-please"

// DefaultVersionPattern matches the shapes a released version takes in practice:
// a two component CalVer such as 2026.44, a three component SemVer such as
// 1.2.3, and an optional pre-release or build suffix. A suffix identifier has
// to start with a letter or digit, so the version in "1.2.3-->" or "1.2.3." does
// not swallow the end of an HTML comment or a full stop.
const DefaultVersionPattern = `\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z][0-9A-Za-z-]*(?:\.[0-9A-Za-z][0-9A-Za-z-]*)*)?`

// markerScope says which part of a version a marker asks to rewrite.
type markerScope int

const (
	scopeNone markerScope = iota
	scopeVersion
	scopeMajor
	scopeMinor
	scopePatch
)

// markerNames holds the annotation names derived from a marker prefix.
// A user supplies the prefix once and gets the whole family, the same way
// release-please does.
type markerNames struct {
	version      string
	major        string
	minor        string
	patch        string
	startVersion string
	startOf      map[string]markerScope
	blockEnds    string
}

func newMarkerNames(prefix string) markerNames {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = DefaultMarker
	}
	return markerNames{
		version:      prefix + "-version",
		startVersion: prefix + "-start-version",
		major:        prefix + "-major",
		minor:        prefix + "-minor",
		patch:        prefix + "-patch",
		startOf: map[string]markerScope{
			prefix + "-start-version": scopeVersion,
			prefix + "-start-major":   scopeMajor,
			prefix + "-start-minor":   scopeMinor,
			prefix + "-start-patch":   scopePatch,
		},
		blockEnds: prefix + "-end",
	}
}

// inlineScope reports the scope requested by an inline annotation on a line.
func (names markerNames) inlineScope(line string) markerScope {
	switch {
	case strings.Contains(line, names.version):
		return scopeVersion
	case strings.Contains(line, names.major):
		return scopeMajor
	case strings.Contains(line, names.minor):
		return scopeMinor
	case strings.Contains(line, names.patch):
		return scopePatch
	default:
		return scopeNone
	}
}

// startScope reports the scope requested by a block opening line.
func (names markerNames) startScope(line string) (markerScope, bool) {
	for annotation, scope := range names.startOf {
		if strings.Contains(line, annotation) {
			return scope, true
		}
	}
	return scopeNone, false
}

// bumpGenericFile rewrites the versions annotated in a file.
//
// Two annotation shapes are supported, matching release-please:
// an inline annotation rewrites the version on that line only, and a block
// between a start and an end annotation rewrites every version inside it.
//
// A configured file without any annotation is an error rather than a silent
// no-op: the file was asked for explicitly, and quietly doing nothing would
// look exactly like a working setup.
func bumpGenericFile(path, marker, pattern, version string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	names := newMarkerNames(marker)

	versionRe, err := regexp.Compile(compileVersionPattern(pattern))
	if err != nil {
		return fmt.Errorf("version pattern for %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	// A trailing empty element means the file ended with a newline. It has to
	// be restored on write, otherwise every run would add a line to the file.
	trailingNewline := lines[len(lines)-1] == ""
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}

	out := make([]string, 0, len(lines))
	rewritten, annotated := 0, 0
	blockScope := scopeNone
	inBlock := false

	for _, line := range lines {
		switch {
		case strings.Contains(line, names.blockEnds):
			inBlock, blockScope = false, scopeNone
			out = append(out, line)
			continue
		case blockOpen(names, line, &blockScope):
			annotated++
			inBlock = true
			out = append(out, line)
			continue
		}

		scope := blockScope
		if !inBlock {
			scope = names.inlineScope(line)
			if scope != scopeNone {
				annotated++
			}
		}
		if scope == scopeNone {
			out = append(out, line)
			continue
		}
		updated, changed := replaceAnnotatedVersion(line, versionRe, version, scope)
		if changed {
			rewritten++
		}
		out = append(out, updated)
	}

	if annotated == 0 {
		return fmt.Errorf("%s has no %q annotation: add %q, or a block from %q to %q",
			path, names.version, names.version, firstStart(names), names.blockEnds)
	}
	if rewritten == 0 {
		// The annotations are present and already carry this version, which is
		// what a rerun looks like. Nothing to do is a valid outcome here.
		return nil
	}
	text := strings.Join(out, "\n")
	if trailingNewline {
		text += "\n"
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func blockOpen(names markerNames, line string, scope *markerScope) bool {
	found, ok := names.startScope(line)
	if !ok {
		return false
	}
	*scope = found
	return true
}

func firstStart(names markerNames) string {
	return names.startVersion
}

func compileVersionPattern(pattern string) string {
	if strings.TrimSpace(pattern) == "" {
		return DefaultVersionPattern
	}
	return "(?:" + pattern + ")"
}

// replaceAnnotatedVersion rewrites the first version on the line according to
// the requested scope.
func replaceAnnotatedVersion(line string, versionRe *regexp.Regexp, version string, scope markerScope) (string, bool) {
	location := versionRe.FindStringIndex(line)
	if location == nil {
		return line, false
	}
	current := line[location[0]:location[1]]
	updated := applyScope(current, version, scope)
	if updated == current {
		return line, false
	}
	return line[:location[0]] + updated + line[location[1]:], true
}

// applyScope rewrites the whole version or only one dotted component of it.
// Component scope is what makes a marker like -minor work for a two component
// CalVer, where "minor" is the release counter.
func applyScope(current, next string, scope markerScope) string {
	if scope == scopeVersion {
		return next
	}
	base, suffix := splitSuffix(current)
	parts := strings.Split(base, ".")
	index := componentIndex(scope, len(parts))
	if index < 0 {
		return current
	}
	nextBase, _ := splitSuffix(next)
	nextParts := strings.Split(nextBase, ".")
	if index >= len(nextParts) {
		return current
	}
	parts[index] = nextParts[index]
	return strings.Join(parts, ".") + suffix
}

func componentIndex(scope markerScope, count int) int {
	switch scope {
	case scopeMajor:
		return 0
	case scopeMinor:
		if count < 2 {
			return -1
		}
		return 1
	case scopePatch:
		if count == 0 {
			return -1
		}
		return count - 1
	default:
		return -1
	}
}

func splitSuffix(version string) (base, suffix string) {
	if index := strings.IndexAny(version, "-+"); index >= 0 {
		return version[:index], version[index:]
	}
	return version, ""
}
