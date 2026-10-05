package release

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The file types follow the release types of release-please. Each one edits
// the version in place: every other byte of the file is kept, so a release
// commit shows a one line change per file.

// deprecatedFileTypes maps the former type names to the ones that replace them.
// They keep working, so existing configurations do not break.
var deprecatedFileTypes = map[string]string{
	"package-json": "node",
	"package-lock": "node",
	"helm-chart":   "helm",
	"plain":        "simple",
}

// versionLiteral recognises a version written out, as opposed to a property
// reference such as $(Version) or ${revision}.
var versionLiteral = regexp.MustCompile(`^v?\d+(\.\d+)*([-+][0-9A-Za-z.-]+)?$`)

// bumpNode updates package.json and, next to it, package-lock.json and
// npm-shrinkwrap.json when they exist. path is the manifest or its directory.
func bumpNode(path, version string) ([]string, error) {
	manifest := path
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		manifest = filepath.Join(path, "package.json")
	}
	if err := bumpPackageJSON(manifest, version); err != nil {
		return nil, err
	}
	changed := []string{manifest}
	for _, name := range []string{"package-lock.json", "npm-shrinkwrap.json"} {
		lock := filepath.Join(filepath.Dir(manifest), name)
		if !fileExists(lock) {
			continue
		}
		if err := bumpPackageLock(lock, version); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		changed = append(changed, lock)
	}
	return changed, nil
}

// bumpHelm updates Chart.yaml; path is the file or the chart directory.
func bumpHelm(path, version string) ([]string, error) {
	chart := path
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		chart = filepath.Join(path, "Chart.yaml")
	}
	if err := bumpHelmChart(chart, version); err != nil {
		return nil, err
	}
	return []string{chart}, nil
}

// dotnetVersionElements are the MSBuild properties that carry a version.
var dotnetVersionElements = map[string]bool{
	"Version":              true,
	"VersionPrefix":        true,
	"PackageVersion":       true,
	"FileVersion":          true,
	"AssemblyVersion":      true,
	"InformationalVersion": true,
}

// bumpDotnet updates the version properties of an MSBuild project: a .csproj,
// .fsproj or .vbproj, or a Directory.Build.props shared by several projects.
// Properties computed from others, such as $(Version), are left alone.
func bumpDotnet(path, version string) ([]string, error) {
	return []string{path}, rewriteXMLText(path, version, func(stack []string) bool {
		depth := len(stack)
		return depth >= 3 && stack[depth-2] == "PropertyGroup" && dotnetVersionElements[stack[depth-1]]
	}, "no Version, VersionPrefix, FileVersion or AssemblyVersion property with a literal version")
}

// bumpMaven updates the version of the project in pom.xml, not the one of its
// parent or of a dependency.
func bumpMaven(path, version string) ([]string, error) {
	return []string{path}, rewriteXMLText(path, version, func(stack []string) bool {
		return len(stack) == 2 && stack[0] == "project" && stack[1] == "version"
	}, "no literal <project><version>; a version inherited from the parent cannot be released")
}

// bumpPHP updates the version of composer.json.
func bumpPHP(path, version string) ([]string, error) {
	return []string{path}, bumpPackageJSON(path, version)
}

// bumpPython updates the version where the file keeps it: pyproject.toml
// ([project] or [tool.poetry]), setup.cfg ([metadata]), setup.py (version=),
// or any other module (__version__).
func bumpPython(path, version string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var output []byte
	switch filepath.Base(path) {
	case "pyproject.toml":
		output, err = replaceTOMLString(data, version, "project", "tool.poetry")
	case "setup.cfg":
		output, err = replaceINIValue(data, version, "metadata", "version")
	case "setup.py":
		output, err = replaceFirstGroup(data, version, regexp.MustCompile(`\bversion\s*=\s*["']([^"']+)["']`))
	default:
		output, err = replaceFirstGroup(data, version, regexp.MustCompile(`__version__\s*=\s*["']([^"']+)["']`))
	}
	if err != nil {
		return nil, err
	}
	return []string{path}, os.WriteFile(path, output, 0o644)
}

// bumpRust updates the [package] or [workspace.package] version of Cargo.toml
// and, when Cargo.lock sits next to it, the entry of that package there.
func bumpRust(path, version string) ([]string, error) {
	manifest := path
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		manifest = filepath.Join(path, "Cargo.toml")
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		return nil, err
	}
	output, err := replaceTOMLString(data, version, "package", "workspace.package")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(manifest, output, 0o644); err != nil {
		return nil, err
	}
	changed := []string{manifest}

	lock := filepath.Join(filepath.Dir(manifest), "Cargo.lock")
	name := tomlString(data, "package", "name")
	if name == "" || !fileExists(lock) {
		return changed, nil
	}
	lockData, err := os.ReadFile(lock)
	if err != nil {
		return nil, err
	}
	lockOutput, found := replaceCargoLockVersion(lockData, name, version)
	if !found {
		return changed, nil
	}
	if err := os.WriteFile(lock, lockOutput, 0o644); err != nil {
		return nil, err
	}
	return append(changed, lock), nil
}

// bumpRuby updates the VERSION constant, as in lib/<gem>/version.rb.
func bumpRuby(path, version string) ([]string, error) {
	return rewriteFirstGroup(path, version, regexp.MustCompile(`\bVERSION\s*=\s*["']([^"']+)["']`))
}

// bumpDart updates the version of pubspec.yaml and keeps its +build number.
func bumpDart(path, version string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`(?m)^version:[ \t]*["']?([0-9A-Za-z.-]+)(\+[0-9A-Za-z.]+)?["']?`)
	location := re.FindSubmatchIndex(data)
	if location == nil {
		return nil, errors.New("no top level version key")
	}
	output := append(append(append([]byte{}, data[:location[2]]...), version...), data[location[3]:]...)
	return []string{path}, os.WriteFile(path, output, 0o644)
}

// bumpElixir updates the @version attribute of mix.exs, or the version key of
// the project when there is no attribute.
func bumpElixir(path, version string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	output, err := replaceFirstGroup(data, version, regexp.MustCompile(`@version\s+"([^"]+)"`))
	if err != nil {
		output, err = replaceFirstGroup(data, version, regexp.MustCompile(`\bversion:\s*"([^"]+)"`))
	}
	if err != nil {
		return nil, err
	}
	return []string{path}, os.WriteFile(path, output, 0o644)
}

// bumpR updates the Version field of an R package DESCRIPTION.
func bumpR(path, version string) ([]string, error) {
	return rewriteFirstGroup(path, version, regexp.MustCompile(`(?m)^Version:[ \t]*(\S+)`))
}

// bumpSimple makes the whole file the version, as version.txt.
func bumpSimple(path, version string) ([]string, error) {
	return []string{path}, os.WriteFile(path, []byte(version+"\n"), 0o644)
}

func rewriteFirstGroup(path, version string, re *regexp.Regexp) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	output, err := replaceFirstGroup(data, version, re)
	if err != nil {
		return nil, err
	}
	return []string{path}, os.WriteFile(path, output, 0o644)
}

// replaceFirstGroup replaces the first capture group of the first match.
func replaceFirstGroup(data []byte, value string, re *regexp.Regexp) ([]byte, error) {
	location := re.FindSubmatchIndex(data)
	if location == nil {
		return nil, fmt.Errorf("no version matching %s", re)
	}
	return splice(data, location[2], location[3], value), nil
}

func splice(data []byte, start, end int, value string) []byte {
	output := make([]byte, 0, len(data)+len(value))
	output = append(output, data[:start]...)
	output = append(output, value...)
	return append(output, data[end:]...)
}

var (
	tomlHeader = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)
	tomlKey    = regexp.MustCompile(`^(\s*([A-Za-z0-9_.-]+)\s*=\s*)"([^"]*)"`)
)

// replaceTOMLString replaces the quoted version key of the first of the given
// tables that has one.
func replaceTOMLString(data []byte, version string, tables ...string) ([]byte, error) {
	for _, table := range tables {
		start, end, found := tomlStringRange(data, table, "version")
		if found {
			return splice(data, start, end, version), nil
		}
	}
	return nil, fmt.Errorf("no version key in [%s]", strings.Join(tables, "] or ["))
}

// tomlString returns the value of a quoted key of a table, or "".
func tomlString(data []byte, table, key string) string {
	start, end, found := tomlStringRange(data, table, key)
	if !found {
		return ""
	}
	return string(data[start:end])
}

// tomlStringRange finds a quoted key in a table and returns the byte range of
// its value, without the quotes.
func tomlStringRange(data []byte, table, key string) (int, int, bool) {
	current := ""
	offset := 0
	reader := bufio.NewReader(bytes.NewReader(data))
	for {
		line, err := reader.ReadString('\n')
		if match := tomlHeader.FindStringSubmatch(line); match != nil {
			current = match[1]
		} else if current == table {
			if match := tomlKey.FindStringSubmatchIndex(line); match != nil && line[match[4]:match[5]] == key {
				return offset + match[6], offset + match[7], true
			}
		}
		offset += len(line)
		if err != nil {
			return 0, 0, false
		}
	}
}

// replaceINIValue replaces an unquoted key of an INI section, as in setup.cfg.
func replaceINIValue(data []byte, version, section, key string) ([]byte, error) {
	header := regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
	entry := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*[=:]\s*(\S+)`)
	current := ""
	offset := 0
	reader := bufio.NewReader(bytes.NewReader(data))
	for {
		line, err := reader.ReadString('\n')
		if match := header.FindStringSubmatch(line); match != nil {
			current = match[1]
		} else if current == section {
			if match := entry.FindStringSubmatchIndex(line); match != nil {
				return splice(data, offset+match[2], offset+match[3], version), nil
			}
		}
		offset += len(line)
		if err != nil {
			return nil, fmt.Errorf("no %s in [%s]", key, section)
		}
	}
}

// replaceCargoLockVersion updates the version of the named package entry of
// Cargo.lock.
func replaceCargoLockVersion(data []byte, name, version string) ([]byte, bool) {
	block := regexp.MustCompile(`(?m)^\[\[package\]\]\nname = "` + regexp.QuoteMeta(name) + `"\nversion = "([^"]+)"`)
	location := block.FindSubmatchIndex(data)
	if location == nil {
		return data, false
	}
	return splice(data, location[2], location[3], version), true
}

// rewriteXMLText rewrites the text of the elements selected by match, given
// the path of element names from the root. Only literal versions are touched;
// at least one has to be found.
func rewriteXMLText(path, version string, match func(stack []string) bool, missing string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	ranges, err := xmlTextRanges(data, match)
	if err != nil {
		return fmt.Errorf("parse XML: %w", err)
	}
	if len(ranges) == 0 {
		return errors.New(missing)
	}
	output := make([]byte, 0, len(data))
	last := 0
	for _, r := range ranges {
		output = append(output, data[last:r[0]]...)
		output = append(output, version...)
		last = r[1]
	}
	output = append(output, data[last:]...)
	return os.WriteFile(path, output, 0o644)
}

// xmlTextRanges returns the byte ranges of the literal versions held by the
// elements selected by match, surrounding whitespace excluded.
func xmlTextRanges(data []byte, match func(stack []string) bool) ([][2]int, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var stack []string
	var ranges [][2]int
	textStart := -1
	for {
		token, err := decoder.RawToken()
		if errors.Is(err, io.EOF) {
			return ranges, nil
		}
		if err != nil {
			return nil, err
		}
		switch element := token.(type) {
		case xml.StartElement:
			stack = append(stack, element.Name.Local)
			textStart = -1
			if match(stack) {
				textStart = int(decoder.InputOffset())
			}
		case xml.EndElement:
			if textStart >= 0 {
				// The end tag starts right after the text: InputOffset now
				// points past "</Name>".
				end := int(decoder.InputOffset()) - len("</"+element.Name.Local+">")
				if end > textStart {
					text := data[textStart:end]
					trimmed := bytes.TrimSpace(text)
					if versionLiteral.Match(trimmed) {
						start := textStart + bytes.Index(text, trimmed)
						ranges = append(ranges, [2]int{start, start + len(trimmed)})
					}
				}
				textStart = -1
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
