package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGeneric(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "footer.html")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func readGeneric(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(data)
}

// The inline annotation rewrites the version on its own line only.
func TestGenericInlineAnnotation(t *testing.T) {
	path := writeGeneric(t, "<div>App v2026.44 &copy; Acme</div> <!-- x-magic-release-version -->\n<p>unrelated 1.2.3 text stays</p>\n")

	if err := bumpGenericFile(path, "", "", "2026.45"); err != nil {
		t.Fatalf("bumpGenericFile() error = %v", err)
	}
	got := readGeneric(t, path)
	if !strings.Contains(got, "App v2026.45") {
		t.Fatalf("annotated version not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "unrelated 1.2.3") {
		t.Fatalf("unannotated line was modified:\n%s", got)
	}
}

// A custom marker replaces the default one entirely.
func TestGenericCustomMarker(t *testing.T) {
	path := writeGeneric(t, `version 2026.44 <!-- my-tool-version -->`)

	if err := bumpGenericFile(path, "my-tool", "", "2026.45"); err != nil {
		t.Fatalf("bumpGenericFile() error = %v", err)
	}
	if got := readGeneric(t, path); !strings.Contains(got, "version 2026.45") {
		t.Fatalf("custom marker not honoured:\n%s", got)
	}
}

// Release-please annotations keep working for migrating repositories.
func TestGenericReleasePleaseMarkerIsOptIn(t *testing.T) {
	path := writeGeneric(t, `version 2026.44 <!-- x-release-please-version -->`)

	if err := bumpGenericFile(path, ReleasePleaseMarker, "", "2026.45"); err != nil {
		t.Fatalf("bumpGenericFile() error = %v", err)
	}
	if got := readGeneric(t, path); !strings.Contains(got, "version 2026.45") {
		t.Fatalf("release-please marker not honoured:\n%s", got)
	}
}

// A block rewrites every version inside it, and the markers stay untouched.
func TestGenericBlockAnnotation(t *testing.T) {
	path := writeGeneric(t, "header 9.9.9 untouched\n<!-- x-magic-release-start-version -->\nversion 2026.44\nversion 2026.44\n<!-- x-magic-release-end -->\nfooter 9.9.9 untouched\n")

	if err := bumpGenericFile(path, "", "", "2026.45"); err != nil {
		t.Fatalf("bumpGenericFile() error = %v", err)
	}
	got := readGeneric(t, path)
	if strings.Count(got, "2026.45") != 2 {
		t.Fatalf("expected two rewritten versions:\n%s", got)
	}
	if strings.Count(got, "9.9.9") != 2 {
		t.Fatalf("versions outside the block were modified:\n%s", got)
	}
	if !strings.Contains(got, "<!-- x-magic-release-start-version -->") {
		t.Fatalf("start marker was lost:\n%s", got)
	}
	if !strings.Contains(got, "<!-- x-magic-release-end -->") {
		t.Fatalf("end marker was lost:\n%s", got)
	}
}

// Component markers rewrite a single dotted part of the version.
func TestGenericComponentAnnotations(t *testing.T) {
	tests := []struct {
		name    string
		content string
		marker  string
		version string
		want    string
	}{
		{"major of a two component calver", "v 2026.44 <!-- x-magic-release-major -->", "", "2027.1", "v 2027.44 <!-- x-magic-release-major -->"},
		{"patch of a two component calver", "v 2026.44 <!-- x-magic-release-patch -->", "", "2027.1", "v 2026.1 <!-- x-magic-release-patch -->"},
		{"minor of a semver", "v 1.2.3 <!-- x-magic-release-minor -->", "", "1.4.0", "v 1.4.3 <!-- x-magic-release-minor -->"},
		{"whole version", "v 1.2.3 <!-- x-magic-release-version -->", "", "2.0.0", "v 2.0.0 <!-- x-magic-release-version -->"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeGeneric(t, test.content+"\n")
			if err := bumpGenericFile(path, test.marker, "", test.version); err != nil {
				t.Fatalf("bumpGenericFile() error = %v", err)
			}
			if got := strings.TrimSpace(readGeneric(t, path)); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

// A two component version is what YYYY.PATCH projects use, so it must work.
func TestGenericHandlesTwoComponentVersions(t *testing.T) {
	path := writeGeneric(t, `<b>App v2026.44 &copy; Acme</b> <!-- x-magic-release-version -->`)

	if err := bumpGenericFile(path, "", "", "2026.45"); err != nil {
		t.Fatalf("bumpGenericFile() error = %v", err)
	}
	if got := readGeneric(t, path); !strings.Contains(got, "App v2026.45") {
		t.Fatalf("two component version not rewritten:\n%s", got)
	}
}

// A file listed in the config but never annotated is a misconfiguration.
func TestGenericFailsWithoutAnnotation(t *testing.T) {
	path := writeGeneric(t, `<b>App v2026.44</b>`)

	err := bumpGenericFile(path, "", "", "2026.45")
	if err == nil {
		t.Fatal("bumpGenericFile() error = nil, want a missing annotation error")
	}
	if !strings.Contains(err.Error(), "x-magic-release-version") {
		t.Fatalf("error should name the expected annotation, got: %v", err)
	}
}

// Rerunning must not append a line to the file.
func TestGenericIsIdempotent(t *testing.T) {
	path := writeGeneric(t, "a 1.2.3 <!-- x-magic-release-version -->\nb\n")

	if err := bumpGenericFile(path, "", "", "2.0.0"); err != nil {
		t.Fatalf("bumpGenericFile() error = %v", err)
	}
	first := readGeneric(t, path)

	// Applying the same version again changes nothing and must not error.
	if err := bumpGenericFile(path, "", "", "2.0.0"); err != nil {
		t.Fatalf("bumpGenericFile() second run error = %v", err)
	}
	if second := readGeneric(t, path); second != first {
		t.Fatalf("second run changed the file:\nfirst:  %q\nsecond: %q", first, second)
	}
	if strings.Count(first, "\n") != 2 {
		t.Fatalf("line count changed, got %q", first)
	}
}

// A file without a trailing newline must keep that shape.
func TestGenericPreservesMissingTrailingNewline(t *testing.T) {
	path := writeGeneric(t, `v 1.2.3 <!-- x-magic-release-version -->`)

	if err := bumpGenericFile(path, "", "", "1.3.0"); err != nil {
		t.Fatalf("bumpGenericFile() error = %v", err)
	}
	got := readGeneric(t, path)
	if strings.HasSuffix(got, "\n") {
		t.Fatalf("a trailing newline was added: %q", got)
	}
	if got != `v 1.3.0 <!-- x-magic-release-version -->` {
		t.Fatalf("got %q", got)
	}
}

// A custom pattern lets a project recognise its own version shape.
func TestGenericCustomPattern(t *testing.T) {
	path := writeGeneric(t, `build-number: 2026.44.7 <!-- x-magic-release-version -->`)

	if err := bumpGenericFile(path, "", `\d{4}\.\d{2}\.\d+`, "2026.45.0"); err != nil {
		t.Fatalf("bumpGenericFile() error = %v", err)
	}
	if got := readGeneric(t, path); !strings.Contains(got, "2026.45.0") {
		t.Fatalf("custom pattern not applied:\n%s", got)
	}
}

func TestGenericRejectsBrokenPattern(t *testing.T) {
	path := writeGeneric(t, `v 1.2.3 <!-- x-magic-release-version -->`)

	err := bumpGenericFile(path, "", "[unclosed", "2.0.0")
	if err == nil {
		t.Fatal("bumpGenericFile() error = nil, want a pattern compile error")
	}
}

func TestGenericReportsMissingFile(t *testing.T) {
	err := bumpGenericFile(filepath.Join(t.TempDir(), "absent.html"), "", "", "1.0.0")
	if err == nil {
		t.Fatal("bumpGenericFile() error = nil, want a read error")
	}
}

// A version right before the end of an HTML comment or a sentence keeps
// the punctuation that follows it.
func TestGenericKeepsPunctuationAfterTheVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"html comment end", "<p><!-- x-magic-release-version -->1.0.0--></p>\n", "<p><!-- x-magic-release-version -->2.0.0--></p>\n"},
		{"full stop", "Version 1.0.0. x-magic-release-version\n", "Version 2.0.0. x-magic-release-version\n"},
		{"pre-release suffix", "v1.0.0-rc.1 x-magic-release-version\n", "v2.0.0 x-magic-release-version\n"},
		{"version before the comment", "App v2026.56 © Acme <!-- x-magic-release-version -->\n", "App v2.0.0 © Acme <!-- x-magic-release-version -->\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "file.txt", test.in)
			if err := bumpGenericFile(filepath.Join(dir, "file.txt"), "", "", "2.0.0"); err != nil {
				t.Fatalf("bumpGenericFile() error = %v", err)
			}
			if got := readFile(t, dir, "file.txt"); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

// The error names the block annotation instead of an empty string.
func TestGenericErrorNamesTheBlockAnnotation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "file.txt", "nothing here\n")
	err := bumpGenericFile(filepath.Join(dir, "file.txt"), "", "", "2.0.0")
	if err == nil || !strings.Contains(err.Error(), "x-magic-release-start-version") {
		t.Fatalf("error = %v, want it to name x-magic-release-start-version", err)
	}
}
