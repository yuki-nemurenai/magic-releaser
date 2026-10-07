// Package release is the core of magic-releaser: it reads Conventional
// Commits, computes the next version, renders the notes, rewrites the version
// files and orchestrates the delivery and publication ports.
package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// BumpVersionFiles rewrites the version in every configured file. Only files
// that are configured explicitly are touched: guessing from what happens to be
// in the repository rewrote files nobody asked to version.
func BumpVersionFiles(repoDir string, packages []PackageConfig, manifestPath, version string) ([]string, error) {
	changed := map[string]bool{}

	for _, pkg := range packages {
		for _, file := range pkg.Files {
			paths, err := bumpFile(repoDir, pkg.Path, file, version)
			if err != nil {
				return nil, err
			}
			for _, path := range paths {
				changed[path] = true
			}
		}
	}

	if manifestPath != "" {
		path, err := bumpManifest(repoDir, manifestPath, packages, version)
		if err != nil {
			return nil, err
		}
		if path != "" {
			changed[path] = true
		}
	}

	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

// fileTypes lists the supported types of version files.
const fileTypes = "node, helm, dotnet, maven, python, rust, php, ruby, dart, elixir, r, simple, docker or generic"

func bumpFile(repoDir, packagePath string, file BumpFileConfig, version string) ([]string, error) {
	fullPath := filepath.Join(repoDir, packagePath, file.Path)
	relativePath, err := filepath.Rel(repoDir, fullPath)
	if err != nil {
		relativePath = fullPath
	}

	var paths []string
	switch file.Type {
	case "node":
		paths, err = bumpNode(fullPath, version)
	case "helm":
		paths, err = bumpHelm(fullPath, version)
	case "dotnet":
		paths, err = bumpDotnet(fullPath, version)
	case "maven":
		paths, err = bumpMaven(fullPath, version)
	case "python":
		paths, err = bumpPython(fullPath, version)
	case "rust":
		paths, err = bumpRust(fullPath, version)
	case "php":
		paths, err = bumpPHP(fullPath, version)
	case "ruby":
		paths, err = bumpRuby(fullPath, version)
	case "dart":
		paths, err = bumpDart(fullPath, version)
	case "elixir":
		paths, err = bumpElixir(fullPath, version)
	case "r":
		paths, err = bumpR(fullPath, version)
	case "simple", "plain":
		paths, err = bumpSimple(fullPath, version)
	case "docker":
		paths, err = []string{fullPath}, bumpDockerTag(fullPath, file.Image, version)
	case "generic":
		paths, err = []string{fullPath}, bumpGenericFile(fullPath, file.Marker, file.Pattern, version)
	// The former names keep their exact former behaviour.
	case "package-json":
		paths, err = []string{fullPath}, bumpPackageJSON(fullPath, version)
	case "package-lock":
		paths, err = []string{fullPath}, bumpPackageLock(fullPath, version)
	case "helm-chart":
		paths, err = []string{fullPath}, bumpHelmChart(fullPath, version)
	default:
		return nil, fmt.Errorf("unsupported bump file type %q for %s: use %s", file.Type, relativePath, fileTypes)
	}
	if err != nil {
		return nil, fmt.Errorf("bump %s: %w", relativePath, err)
	}
	relative := make([]string, 0, len(paths))
	for _, path := range paths {
		if rel, err := filepath.Rel(repoDir, path); err == nil {
			path = rel
		}
		relative = append(relative, path)
	}
	return relative, nil
}

// bumpPackageJSON rewrites the top level version in place. Decoding into a map
// and encoding it again would sort every key of the file and escape <, > and &,
// turning a one line version bump into a rewrite of the whole manifest.
func bumpPackageJSON(path, version string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	start, end, found, err := jsonStringRange(data, "version")
	if err != nil {
		return err
	}
	if !found {
		return errors.New(`no top level "version" key`)
	}
	return writeJSONStrings(path, data, version, [][2]int{{start, end}})
}

// bumpPackageLock rewrites the version of the root package in an npm lock file:
// the top level version and, from lockfileVersion 2 on, packages[""].version.
// npm keeps them equal to package.json, which is what release-please's node
// release type maintains as well.
func bumpPackageLock(path, version string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	start, end, found, err := jsonStringRange(data, "version")
	if err != nil {
		return err
	}
	if !found {
		return errors.New(`no top level "version" key`)
	}
	ranges := [][2]int{{start, end}}
	start, end, found, err = jsonStringRange(data, "packages", "", "version")
	if err != nil {
		return err
	}
	if found {
		ranges = append(ranges, [2]int{start, end})
	}
	return writeJSONStrings(path, data, version, ranges)
}

// writeJSONStrings replaces the given string values, quotes included, and
// leaves every other byte of the file as it is.
func writeJSONStrings(path string, data []byte, value string, ranges [][2]int) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	output := make([]byte, 0, len(data)+len(ranges)*len(encoded))
	last := 0
	for _, r := range ranges {
		output = append(output, data[last:r[0]]...)
		output = append(output, encoded...)
		last = r[1]
	}
	output = append(output, data[last:]...)
	return os.WriteFile(path, output, 0o644)
}

// jsonStringRange follows a path of keys through nested objects to a string
// value and returns its byte range in data, quotes included. A missing key is
// reported as not found; a value of another type is an error.
func jsonStringRange(data []byte, path ...string) (int, int, bool, error) {
	base := 0
	object := data
	for index, key := range path {
		start, end, found, err := jsonValueRange(object, key)
		if err != nil || !found {
			return 0, 0, found, err
		}
		value := object[start:end]
		if index == len(path)-1 {
			if len(value) == 0 || value[0] != '"' {
				return 0, 0, false, fmt.Errorf("%q is not a string", strings.Join(path, "."))
			}
			return base + start, base + end, true, nil
		}
		if len(value) == 0 || value[0] != '{' {
			return 0, 0, false, nil
		}
		base += start
		object = value
	}
	return 0, 0, false, nil
}

// jsonValueRange returns the byte range of the raw value of a key of the JSON
// object in data.
func jsonValueRange(data []byte, key string) (int, int, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return 0, 0, false, errors.New("not a JSON object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return 0, 0, false, fmt.Errorf("parse JSON: %w", err)
		}
		afterKey := int(decoder.InputOffset())
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return 0, 0, false, fmt.Errorf("parse JSON: %w", err)
		}
		if token != key {
			continue
		}
		end := int(decoder.InputOffset())
		start := end - len(bytes.TrimLeft(data[afterKey:end], " \t\r\n:"))
		return start, end, true, nil
	}
	return 0, 0, false, nil
}

// helmVersionLine matches a top level version or appVersion key, keeping the
// quoting and any trailing comment of the line intact. A quoted value may be
// empty, as the appVersion: "" of a new chart.
var helmVersionLine = regexp.MustCompile(`(?m)^((?:version|appVersion):[ \t]*)(?:(["'])[^"'\r\n]*["']|[^"'\s#]+)`)

// bumpHelmChart rewrites the chart version and appVersion in place. Encoding
// the parsed document again would drop every comment and reorder the keys.
func bumpHelmChart(path, version string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`(?m)^version:`).Match(data) {
		return errors.New("no top level version key")
	}
	output := helmVersionLine.ReplaceAll(data, []byte("${1}${2}"+version+"${2}"))
	return os.WriteFile(path, output, 0o644)
}

// bumpDockerTag rewrites the tag of one image. The image is required: without
// it every image reference in the file, base images included, would be moved
// to the release version.
func bumpDockerTag(path, image, version string) error {
	if strings.TrimSpace(image) == "" {
		return errors.New("the docker type needs image, the repository of the image to retag")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	pattern := regexp.MustCompile(`(` + regexp.QuoteMeta(image) + `:)[^\s"']+`)
	if !pattern.Match(data) {
		return fmt.Errorf("no tagged reference to image %s", image)
	}
	return os.WriteFile(path, pattern.ReplaceAll(data, []byte("${1}"+version)), 0o644)
}

func bumpManifest(repoDir, path string, packages []PackageConfig, version string) (string, error) {
	fullPath := path
	if !filepath.IsAbs(path) {
		fullPath = filepath.Join(repoDir, path)
	}
	manifest := map[string]string{}
	data, err := os.ReadFile(fullPath)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &manifest); err != nil {
			return "", err
		}
	}
	for _, pkg := range packages {
		name := pkg.Name
		if name == "" {
			name = pkg.Path
		}
		if name == "" || name == "." {
			name = "root"
		}
		manifest[name] = version
	}

	output, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	output = append(output, '\n')
	if err := os.WriteFile(fullPath, output, 0o644); err != nil {
		return "", err
	}
	relativePath, err := filepath.Rel(repoDir, fullPath)
	if err != nil {
		return fullPath, nil
	}
	return relativePath, nil
}

func FormatPullRequestBody(result Result) string {
	var builder bytes.Buffer
	fmt.Fprintf(&builder, "Release %s\n\n", result.NextVersion)
	if len(result.BumpedFiles) > 0 {
		builder.WriteString("Updated files:\n")
		for _, path := range result.BumpedFiles {
			fmt.Fprintf(&builder, "- %s\n", path)
		}
		builder.WriteString("\n")
	}
	builder.WriteString(result.Notes)
	return builder.String()
}
