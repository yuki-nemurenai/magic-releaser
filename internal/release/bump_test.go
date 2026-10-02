package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBumpVersionFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"demo","version":"0.1.0"}`)
	writeFile(t, dir, "Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.1.0\nappVersion: 0.1.0\n")
	writeFile(t, dir, "deploy.yaml", "image: ghcr.io/acme/demo:0.1.0\n")

	changed, err := BumpVersionFiles(dir, []PackageConfig{{
		Name: "root",
		Path: ".",
		Files: []BumpFileConfig{
			{Type: "package-json", Path: "package.json"},
			{Type: "helm-chart", Path: "Chart.yaml"},
			{Type: "docker", Path: "deploy.yaml", Image: "ghcr.io/acme/demo"},
		},
	}}, ".magic-releaser-manifest.json", "2026.08.0")
	if err != nil {
		t.Fatalf("BumpVersionFiles() error = %v", err)
	}

	for _, want := range []string{".magic-releaser-manifest.json", "Chart.yaml", "deploy.yaml", "package.json"} {
		if !contains(changed, want) {
			t.Fatalf("changed = %v, missing %q", changed, want)
		}
	}

	var packageJSON map[string]any
	readJSON(t, filepath.Join(dir, "package.json"), &packageJSON)
	if packageJSON["version"] != "2026.08.0" {
		t.Fatalf("package.json version = %v, want 2026.08.0", packageJSON["version"])
	}
	chart := readFile(t, dir, "Chart.yaml")
	if !strings.Contains(chart, "version: 2026.08.0") || !strings.Contains(chart, "appVersion: 2026.08.0") {
		t.Fatalf("Chart.yaml was not bumped:\n%s", chart)
	}
	deploy := readFile(t, dir, "deploy.yaml")
	if !strings.Contains(deploy, "image: ghcr.io/acme/demo:2026.08.0") {
		t.Fatalf("deploy.yaml was not bumped:\n%s", deploy)
	}
	var manifest map[string]string
	readJSON(t, filepath.Join(dir, ".magic-releaser-manifest.json"), &manifest)
	if manifest["root"] != "2026.08.0" {
		t.Fatalf("manifest[root] = %q, want 2026.08.0", manifest["root"])
	}
}

// Only the version changes. Re-encoding the document sorted every key and
// escaped <, > and &, so a version bump rewrote the whole manifest.
func TestBumpPackageJSONChangesOnlyTheVersion(t *testing.T) {
	dir := t.TempDir()
	original := "{\n  \"name\": \"tms-ts\",\n  \"version\": \"2026.44\",\n  \"scripts\": {\"start\": \"a && b > log\"},\n" +
		"  \"dependencies\": {\"x\": {\"version\": \"1.0.0\"}},\n  \"private\": true\n}\n"
	writeFile(t, dir, "package.json", original)

	if err := bumpPackageJSON(filepath.Join(dir, "package.json"), "2026.09.1"); err != nil {
		t.Fatalf("bumpPackageJSON() error = %v", err)
	}
	want := strings.Replace(original, `"version": "2026.44"`, `"version": "2026.09.1"`, 1)
	if got := readFile(t, dir, "package.json"); got != want {
		t.Fatalf("package.json =\n%s\nwant\n%s", got, want)
	}
}

func TestBumpPackageJSONRejectsAManifestWithoutAVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"no version", `{"name":"demo","nested":{"version":"1.0.0"}}`},
		{"version is not a string", `{"version":1}`},
		{"not an object", `["version"]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "package.json", test.content)
			if err := bumpPackageJSON(filepath.Join(dir, "package.json"), "1.2.3"); err == nil {
				t.Fatal("bumpPackageJSON() error = nil, want an error")
			}
			if got := readFile(t, dir, "package.json"); got != test.content {
				t.Fatalf("the file was modified: %s", got)
			}
		})
	}
}

// Comments, quoting and key order of a chart survive the bump.
func TestBumpHelmChartKeepsTheFileIntact(t *testing.T) {
	dir := t.TempDir()
	original := "# chart of the app\napiVersion: v2\nname: demo\nversion: 0.1.0 # chart\nappVersion: \"0.1.0\"\n" +
		"dependencies:\n  - name: redis\n    version: 17.0.0\n"
	writeFile(t, dir, "Chart.yaml", original)

	if err := bumpHelmChart(filepath.Join(dir, "Chart.yaml"), "1.2.0"); err != nil {
		t.Fatalf("bumpHelmChart() error = %v", err)
	}
	want := "# chart of the app\napiVersion: v2\nname: demo\nversion: 1.2.0 # chart\nappVersion: \"1.2.0\"\n" +
		"dependencies:\n  - name: redis\n    version: 17.0.0\n"
	if got := readFile(t, dir, "Chart.yaml"); got != want {
		t.Fatalf("Chart.yaml =\n%s\nwant\n%s", got, want)
	}
}

// Without an image every reference, base images included, was retagged.
func TestBumpDockerRequiresTheImage(t *testing.T) {
	dir := t.TempDir()
	original := "FROM golang:1.25 AS build\nFROM ghcr.io/acme/app:1.0.0\n"
	writeFile(t, dir, "Dockerfile", original)

	if err := bumpDockerTag(filepath.Join(dir, "Dockerfile"), "", "2.0.0"); err == nil {
		t.Fatal("bumpDockerTag() without image error = nil, want an error")
	}
	if err := bumpDockerTag(filepath.Join(dir, "Dockerfile"), "ghcr.io/acme/app", "2.0.0"); err != nil {
		t.Fatalf("bumpDockerTag() error = %v", err)
	}
	if got := readFile(t, dir, "Dockerfile"); got != "FROM golang:1.25 AS build\nFROM ghcr.io/acme/app:2.0.0\n" {
		t.Fatalf("Dockerfile =\n%s", got)
	}
	if err := bumpDockerTag(filepath.Join(dir, "Dockerfile"), "ghcr.io/acme/other", "2.0.0"); err == nil {
		t.Fatal("bumpDockerTag() for an absent image error = nil, want an error")
	}
}

// Nothing is bumped unless it is configured.
func TestBumpVersionFilesTouchesNothingUnconfigured(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"demo","version":"0.1.0"}`)
	writeFile(t, dir, "go.mod", "module example.com/demo\n")

	changed, err := BumpVersionFiles(dir, nil, "", "1.0.0")
	if err != nil {
		t.Fatalf("BumpVersionFiles() error = %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("changed = %v, want nothing", changed)
	}
	if got := readFile(t, dir, "package.json"); got != `{"name":"demo","version":"0.1.0"}` {
		t.Fatalf("package.json was modified: %s", got)
	}
}

// go.mod carries no version: a Go module is versioned by its tags alone.
func TestBumpVersionFilesRejectsUnsupportedTypes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/demo\n")
	_, err := BumpVersionFiles(dir, []PackageConfig{{Path: ".", Files: []BumpFileConfig{{Type: "go-mod", Path: "go.mod"}}}}, "", "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "unsupported bump file type") {
		t.Fatalf("BumpVersionFiles() error = %v, want an unsupported type error", err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(data)
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// The root package version lives twice in an npm lock file; dependencies carry
// versions of their own that must stay untouched.
func TestBumpPackageLockChangesOnlyTheRootPackage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"lockfile v3",
			"{\n  \"name\": \"app\",\n  \"version\": \"1.9.1\",\n  \"lockfileVersion\": 3,\n  \"packages\": {\n" +
				"    \"\": {\n      \"name\": \"app\",\n      \"version\": \"1.9.1\"\n    },\n" +
				"    \"node_modules/x\": {\n      \"version\": \"1.9.1\"\n    }\n  }\n}\n",
			"{\n  \"name\": \"app\",\n  \"version\": \"1.10.0\",\n  \"lockfileVersion\": 3,\n  \"packages\": {\n" +
				"    \"\": {\n      \"name\": \"app\",\n      \"version\": \"1.10.0\"\n    },\n" +
				"    \"node_modules/x\": {\n      \"version\": \"1.9.1\"\n    }\n  }\n}\n",
		},
		{
			"lockfile v1 without packages",
			"{\n  \"name\": \"app\",\n  \"version\": \"1.9.1\",\n  \"lockfileVersion\": 1,\n  \"dependencies\": {\"x\": {\"version\": \"1.9.1\"}}\n}\n",
			"{\n  \"name\": \"app\",\n  \"version\": \"1.10.0\",\n  \"lockfileVersion\": 1,\n  \"dependencies\": {\"x\": {\"version\": \"1.9.1\"}}\n}\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "package-lock.json", test.in)
			if err := bumpPackageLock(filepath.Join(dir, "package-lock.json"), "1.10.0"); err != nil {
				t.Fatalf("bumpPackageLock() error = %v", err)
			}
			if got := readFile(t, dir, "package-lock.json"); got != test.want {
				t.Fatalf("package-lock.json =\n%s\nwant\n%s", got, test.want)
			}
		})
	}
}

func TestBumpPackageLockRejectsAFileWithoutAVersion(t *testing.T) {
	dir := t.TempDir()
	original := `{"name":"app","lockfileVersion":3,"packages":{"":{"version":"1.0.0"}}}`
	writeFile(t, dir, "package-lock.json", original)
	if err := bumpPackageLock(filepath.Join(dir, "package-lock.json"), "1.1.0"); err == nil {
		t.Fatal("bumpPackageLock() error = nil, want an error")
	}
	if got := readFile(t, dir, "package-lock.json"); got != original {
		t.Fatalf("the file was modified: %s", got)
	}
}
