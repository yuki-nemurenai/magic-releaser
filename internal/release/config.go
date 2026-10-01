package release

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

var configNames = []string{
	".magic-releaser.yaml",
	".magic-releaser.yml",
}

func LoadConfig(repoDir, path string) (Config, string, error) {
	if path != "" {
		config, err := readConfig(path)
		return config, path, err
	}

	for _, name := range configNames {
		candidate := filepath.Join(repoDir, name)
		config, err := readConfig(candidate)
		if err == nil {
			return config, candidate, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Config{}, "", err
		}
	}
	return Config{}, "", nil
}

func MergeConfig(options Options, config Config) Options {
	if options.Mode == "" {
		options.Mode = config.Mode
	}
	if options.Versioning == "" {
		options.Versioning = config.Versioning
	}
	if options.CalVerFormat == "" {
		options.CalVerFormat = config.CalVerFormat
	}
	if options.Timezone == "" {
		options.Timezone = config.Timezone
	}
	if options.TagFormat == "" {
		options.TagFormat = config.TagFormat
	}
	// A flag wins over the config, and a config key that is present but empty
	// disables the changelog just like the --changelog "" flag does.
	if !options.ChangelogSet && config.Changelog != nil {
		options.Changelog = *config.Changelog
		options.ChangelogSet = true
	}
	if options.ReleaseName == "" {
		options.ReleaseName = config.ReleaseName
	}
	if options.ReleaseCommitMessage == "" {
		options.ReleaseCommitMessage = config.ReleaseCommitMessage
	}
	if options.Manifest == "" {
		options.Manifest = config.Manifest
	}
	if len(options.Packages) == 0 {
		options.Packages = config.Packages
	}
	if options.Remote == "" {
		options.Remote = config.Remote
	}
	if options.Notes.Preset == "" && len(options.Notes.Categories) == 0 {
		options.Notes.Preset = config.Notes.Preset
		options.Notes.ShowContributors = config.Notes.ShowContributors
	}
	if len(options.Notes.Categories) == 0 {
		options.Notes.Categories = config.Notes.Categories
	}
	return options
}

// readConfig parses the config file strictly: an unknown key is an error
// rather than a silently ignored setting, so a typo or a removed option can
// never look like it took effect.
func readConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return config, nil
}
