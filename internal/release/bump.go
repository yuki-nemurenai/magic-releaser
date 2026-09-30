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
			path, err := bumpFile(repoDir, pkg.Path, file, version)
			if err != nil {
				return nil, err
			}
			if path != "" {
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

func bumpFile(repoDir, packagePath string, file BumpFileConfig, version string) (string, error) {
	fullPath := filepath.Join(repoDir, packagePath, file.Path)
	relativePath, err := filepath.Rel(repoDir, fullPath)
	if err != nil {
		relativePath = fullPath
	}

	switch file.Type {
	case "package-json":
		err = bumpPackageJSON(fullPath, version)
	case "helm-chart":
		err = bumpHelmChart(fullPath, version)
	case "docker":
		err = bumpDockerTag(fullPath, file.Image, version)
	case "generic":
		err = bumpGenericFile(fullPath, file.Marker, file.Pattern, version)
	case "plain":
		err = os.WriteFile(fullPath, []byte(version+"\n"), 0o644)
	default:
		return "", fmt.Errorf("unsupported bump file type %q for %s: use package-json, helm-chart, docker, generic or plain",
			file.Type, relativePath)
	}
	if err != nil {
		return "", fmt.Errorf("bump %s: %w", relativePath, err)
	}
	return relativePath, nil
}

// bumpPackageJSON rewrites the top level version in place. Decoding into a map
// and encoding it again would sort every key of the file and escape <, > and &,
// turning a one line version bump into a rewrite of the whole manifest.
func bumpPackageJSON(path, version string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	start, end, err := topLevelJSONString(data, "version")
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(version)
	if err != nil {
		return err
	}
	output := make([]byte, 0, len(data)+len(encoded))
	output = append(output, data[:start]...)
	output = append(output, encoded...)
	output = append(output, data[end:]...)
	return os.WriteFile(path, output, 0o644)
}

// topLevelJSONString returns the byte range of the string value of a top level
// key, quotes included.
func topLevelJSONString(data []byte, key string) (int, int, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return 0, 0, errors.New("not a JSON object")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return 0, 0, fmt.Errorf("parse JSON: %w", err)
		}
		afterKey := int(decoder.InputOffset())
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return 0, 0, fmt.Errorf("parse JSON: %w", err)
		}
		if token != key {
			continue
		}
		end := int(decoder.InputOffset())
		start := bytes.IndexByte(data[afterKey:end], '"')
		if start < 0 || len(value) == 0 || value[0] != '"' {
			return 0, 0, fmt.Errorf("top level %q is not a string", key)
		}
		return afterKey + start, end, nil
	}
	return 0, 0, fmt.Errorf("no top level %q key", key)
}

// helmVersionLine matches a top level version or appVersion key, keeping the
// quoting and any trailing comment of the line intact.
var helmVersionLine = regexp.MustCompile(`(?m)^((?:version|appVersion):[ \t]*)(["']?)[^"'\s#]+(["']?)`)

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
	output := helmVersionLine.ReplaceAll(data, []byte("${1}${2}"+version+"${3}"))
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
