package release

import (
	"bytes"
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every file type rewrites the version in place and leaves the versions that
// belong to something else (a parent, a dependency, a build number) alone.
func TestFileTypesRewriteOnlyTheReleaseVersion(t *testing.T) {
	tests := []struct {
		name     string
		fileType string
		files    map[string]string
		path     string
		want     map[string]string
	}{
		{
			name:     "dotnet project",
			fileType: "dotnet",
			path:     "EFCoreDemo/EFCoreDemo.csproj",
			files: map[string]string{"EFCoreDemo/EFCoreDemo.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web">
  <PropertyGroup>
    <TargetFramework>net9.0</TargetFramework>
    <Version>4.1.2</Version> <!-- x-magic-release-version -->
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Include="Npgsql.EntityFrameworkCore.PostgreSQL" Version="9.0.3"/>
  </ItemGroup>
</Project>
`},
			want: map[string]string{"EFCoreDemo/EFCoreDemo.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web">
  <PropertyGroup>
    <TargetFramework>net9.0</TargetFramework>
    <Version>4.2.0</Version> <!-- x-magic-release-version -->
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Include="Npgsql.EntityFrameworkCore.PostgreSQL" Version="9.0.3"/>
  </ItemGroup>
</Project>
`},
		},
		{
			name:     "dotnet props with several versions and a computed one",
			fileType: "dotnet",
			path:     "Directory.Build.props",
			files: map[string]string{"Directory.Build.props": `<Project>
  <PropertyGroup>
    <Version>1.0.0.0</Version>
    <FileVersion> 1.0.0.0 </FileVersion>
    <AssemblyVersion>1.0.0.0</AssemblyVersion>
    <InformationalVersion>$(Version)-local</InformationalVersion>
  </PropertyGroup>
</Project>
`},
			want: map[string]string{"Directory.Build.props": `<Project>
  <PropertyGroup>
    <Version>4.2.0</Version>
    <FileVersion> 4.2.0 </FileVersion>
    <AssemblyVersion>4.2.0</AssemblyVersion>
    <InformationalVersion>$(Version)-local</InformationalVersion>
  </PropertyGroup>
</Project>
`},
		},
		{
			name:     "maven project, not its parent or dependencies",
			fileType: "maven",
			path:     "pom.xml",
			files: map[string]string{"pom.xml": `<project xmlns="http://maven.apache.org/POM/4.0.0">
  <parent>
    <groupId>org.acme</groupId>
    <version>9.9.9</version>
  </parent>
  <artifactId>app</artifactId>
  <version>4.1.2</version>
  <dependencies>
    <dependency><artifactId>x</artifactId><version>4.1.2</version></dependency>
  </dependencies>
</project>
`},
			want: map[string]string{"pom.xml": `<project xmlns="http://maven.apache.org/POM/4.0.0">
  <parent>
    <groupId>org.acme</groupId>
    <version>9.9.9</version>
  </parent>
  <artifactId>app</artifactId>
  <version>4.2.0</version>
  <dependencies>
    <dependency><artifactId>x</artifactId><version>4.1.2</version></dependency>
  </dependencies>
</project>
`},
		},
		{
			name:     "node with both lock files, from the directory",
			fileType: "node",
			path:     ".",
			files: map[string]string{
				"package.json":        "{\n  \"name\": \"app\",\n  \"version\": \"4.1.2\"\n}\n",
				"package-lock.json":   "{\n  \"version\": \"4.1.2\",\n  \"packages\": {\"\": {\"version\": \"4.1.2\"}, \"node_modules/x\": {\"version\": \"4.1.2\"}}\n}\n",
				"npm-shrinkwrap.json": "{\n  \"version\": \"4.1.2\"\n}\n",
			},
			want: map[string]string{
				"package.json":        "{\n  \"name\": \"app\",\n  \"version\": \"4.2.0\"\n}\n",
				"package-lock.json":   "{\n  \"version\": \"4.2.0\",\n  \"packages\": {\"\": {\"version\": \"4.2.0\"}, \"node_modules/x\": {\"version\": \"4.1.2\"}}\n}\n",
				"npm-shrinkwrap.json": "{\n  \"version\": \"4.2.0\"\n}\n",
			},
		},
		{
			name:     "helm chart directory",
			fileType: "helm",
			path:     "chart",
			files:    map[string]string{"chart/Chart.yaml": "apiVersion: v2\nname: app\nversion: 4.1.2\nappVersion: \"4.1.2\"\n"},
			want:     map[string]string{"chart/Chart.yaml": "apiVersion: v2\nname: app\nversion: 4.2.0\nappVersion: \"4.2.0\"\n"},
		},
		{
			name:     "python pyproject, not a tool table",
			fileType: "python",
			path:     "pyproject.toml",
			files: map[string]string{"pyproject.toml": `[tool.black]
version = "23.1.0"

[project]
name = "app"
version = "4.1.2"
`},
			want: map[string]string{"pyproject.toml": `[tool.black]
version = "23.1.0"

[project]
name = "app"
version = "4.2.0"
`},
		},
		{
			name:     "python poetry",
			fileType: "python",
			path:     "pyproject.toml",
			files:    map[string]string{"pyproject.toml": "[tool.poetry]\nname = \"app\"\nversion = \"4.1.2\"\n"},
			want:     map[string]string{"pyproject.toml": "[tool.poetry]\nname = \"app\"\nversion = \"4.2.0\"\n"},
		},
		{
			name:     "python setup.cfg",
			fileType: "python",
			path:     "setup.cfg",
			files:    map[string]string{"setup.cfg": "[options]\nversion = 0.0.0\n\n[metadata]\nname = app\nversion = 4.1.2\n"},
			want:     map[string]string{"setup.cfg": "[options]\nversion = 0.0.0\n\n[metadata]\nname = app\nversion = 4.2.0\n"},
		},
		{
			name:     "python setup.py",
			fileType: "python",
			path:     "setup.py",
			files:    map[string]string{"setup.py": "setup(\n    name=\"app\",\n    version=\"4.1.2\",\n)\n"},
			want:     map[string]string{"setup.py": "setup(\n    name=\"app\",\n    version=\"4.2.0\",\n)\n"},
		},
		{
			name:     "python module",
			fileType: "python",
			path:     "app/__init__.py",
			files:    map[string]string{"app/__init__.py": "__version__ = '4.1.2'\n"},
			want:     map[string]string{"app/__init__.py": "__version__ = '4.2.0'\n"},
		},
		{
			name:     "rust crate and its lock entry",
			fileType: "rust",
			path:     "Cargo.toml",
			files: map[string]string{
				"Cargo.toml": "[package]\nname = \"app\"\nversion = \"4.1.2\"\n\n[dependencies]\nserde = { version = \"1.0\" }\n",
				"Cargo.lock": "[[package]]\nname = \"app\"\nversion = \"4.1.2\"\n\n[[package]]\nname = \"serde\"\nversion = \"4.1.2\"\n",
			},
			want: map[string]string{
				"Cargo.toml": "[package]\nname = \"app\"\nversion = \"4.2.0\"\n\n[dependencies]\nserde = { version = \"1.0\" }\n",
				"Cargo.lock": "[[package]]\nname = \"app\"\nversion = \"4.2.0\"\n\n[[package]]\nname = \"serde\"\nversion = \"4.1.2\"\n",
			},
		},
		{
			name:     "rust workspace",
			fileType: "rust",
			path:     "Cargo.toml",
			files:    map[string]string{"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n\n[workspace.package]\nversion = \"4.1.2\"\n"},
			want:     map[string]string{"Cargo.toml": "[workspace]\nmembers = [\"a\"]\n\n[workspace.package]\nversion = \"4.2.0\"\n"},
		},
		{
			name:     "php composer",
			fileType: "php",
			path:     "composer.json",
			files:    map[string]string{"composer.json": "{\n    \"name\": \"acme/app\",\n    \"version\": \"4.1.2\"\n}\n"},
			want:     map[string]string{"composer.json": "{\n    \"name\": \"acme/app\",\n    \"version\": \"4.2.0\"\n}\n"},
		},
		{
			name:     "ruby version constant",
			fileType: "ruby",
			path:     "lib/app/version.rb",
			files:    map[string]string{"lib/app/version.rb": "module App\n  VERSION = \"4.1.2\"\nend\n"},
			want:     map[string]string{"lib/app/version.rb": "module App\n  VERSION = \"4.2.0\"\nend\n"},
		},
		{
			name:     "dart keeps the build number",
			fileType: "dart",
			path:     "pubspec.yaml",
			files:    map[string]string{"pubspec.yaml": "name: app\nversion: 4.1.2+17\nenvironment:\n  sdk: ^3.0.0\n"},
			want:     map[string]string{"pubspec.yaml": "name: app\nversion: 4.2.0+17\nenvironment:\n  sdk: ^3.0.0\n"},
		},
		{
			name:     "elixir attribute",
			fileType: "elixir",
			path:     "mix.exs",
			files:    map[string]string{"mix.exs": "defmodule App.MixProject do\n  @version \"4.1.2\"\n  def project, do: [app: :app, version: @version]\nend\n"},
			want:     map[string]string{"mix.exs": "defmodule App.MixProject do\n  @version \"4.2.0\"\n  def project, do: [app: :app, version: @version]\nend\n"},
		},
		{
			name:     "elixir keyword",
			fileType: "elixir",
			path:     "mix.exs",
			files:    map[string]string{"mix.exs": "def project do\n  [app: :app, version: \"4.1.2\"]\nend\n"},
			want:     map[string]string{"mix.exs": "def project do\n  [app: :app, version: \"4.2.0\"]\nend\n"},
		},
		{
			name:     "r description",
			fileType: "r",
			path:     "DESCRIPTION",
			files:    map[string]string{"DESCRIPTION": "Package: app\nVersion: 4.1.2\nDepends: R (>= 4.1.2)\n"},
			want:     map[string]string{"DESCRIPTION": "Package: app\nVersion: 4.2.0\nDepends: R (>= 4.1.2)\n"},
		},
		{
			name:     "simple",
			fileType: "simple",
			path:     "version.txt",
			files:    map[string]string{"version.txt": "4.1.2\n"},
			want:     map[string]string{"version.txt": "4.2.0\n"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range test.files {
				writeFile(t, dir, name, content)
			}
			changed, err := BumpVersionFiles(dir, []PackageConfig{{Path: ".", Files: []BumpFileConfig{
				{Type: test.fileType, Path: test.path},
			}}}, "", "4.2.0")
			if err != nil {
				t.Fatalf("BumpVersionFiles() error = %v", err)
			}
			wantChanged := make([]string, 0, len(test.want))
			for name, content := range test.want {
				wantChanged = append(wantChanged, filepath.Clean(name))
				if got := readFile(t, dir, name); got != content {
					t.Fatalf("%s =\n%s\nwant\n%s", name, got, content)
				}
			}
			sort.Strings(wantChanged)
			if strings.Join(changed, ",") != strings.Join(wantChanged, ",") {
				t.Fatalf("changed = %v, want %v", changed, wantChanged)
			}
		})
	}
}

// A file whose version cannot be found fails the release instead of passing as
// a working setup.
func TestFileTypesRejectAFileWithoutAVersion(t *testing.T) {
	tests := []struct {
		name     string
		fileType string
		path     string
		content  string
	}{
		{"dotnet without a version", "dotnet", "app.csproj", "<Project><PropertyGroup><TargetFramework>net9.0</TargetFramework></PropertyGroup></Project>"},
		{"dotnet with a computed version only", "dotnet", "app.csproj", "<Project><PropertyGroup><Version>$(Base)</Version></PropertyGroup></Project>"},
		{"maven version inherited from the parent", "maven", "pom.xml", "<project><parent><version>1.0.0</version></parent></project>"},
		{"pyproject with a dynamic version", "python", "pyproject.toml", "[project]\nname = \"app\"\ndynamic = [\"version\"]\n"},
		{"cargo workspace member inheriting the version", "rust", "Cargo.toml", "[package]\nname = \"a\"\nversion.workspace = true\n"},
		{"composer without a version", "php", "composer.json", "{\"name\": \"acme/app\"}"},
		{"unknown type", "gradle", "build.gradle", "version = '1.0.0'"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, test.path, test.content)
			_, err := BumpVersionFiles(dir, []PackageConfig{{Path: ".", Files: []BumpFileConfig{
				{Type: test.fileType, Path: test.path},
			}}}, "", "4.2.0")
			if err == nil {
				t.Fatal("BumpVersionFiles() error = nil, want an error")
			}
			if got := readFile(t, dir, test.path); got != test.content {
				t.Fatalf("the file was modified: %s", got)
			}
		})
	}
}

// The former type names keep working and are named in a warning.
func TestRunWarnsAboutDeprecatedFileTypes(t *testing.T) {
	dir, worktree := newTestRepo(t)
	writeFile(t, dir, "package.json", `{"version":"1.0.0"}`)
	commitExisting(t, worktree, "package.json", "feat: something new")

	var errors bytes.Buffer
	result, err := Run(context.Background(), Options{
		RepoDir:     dir,
		Versioning:  VersioningSemVer,
		Now:         releaseNow,
		Packages:    []PackageConfig{{Path: ".", Files: []BumpFileConfig{{Type: "package-json", Path: "package.json"}}}},
		ErrorOutput: &errors,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(errors.String(), `file type "package-json" of package.json is deprecated, use "node"`) {
		t.Fatalf("no deprecation warning:\n%s", errors.String())
	}
	if got := readFile(t, dir, "package.json"); got != `{"version":"`+result.NextVersion+`"}` {
		t.Fatalf("package.json = %s, want the version %s", got, result.NextVersion)
	}
}
