package release

import (
	"os"
	"path/filepath"
	"strings"
)

func PrependChangelog(repoDir, path, notes string) error {
	if path == "" {
		return nil
	}
	fullPath := path
	if !filepath.IsAbs(path) {
		fullPath = filepath.Join(repoDir, path)
	}

	existing, err := os.ReadFile(fullPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	content := "# Changelog\n\n" + strings.TrimRight(notes, "\n") + "\n"
	if len(existing) > 0 {
		text := string(existing)
		if strings.HasPrefix(text, "# Changelog\n\n") {
			content = "# Changelog\n\n" + strings.TrimRight(notes, "\n") + "\n\n" + strings.TrimPrefix(text, "# Changelog\n\n")
		} else {
			content = strings.TrimRight(notes, "\n") + "\n\n" + text
		}
	}
	return os.WriteFile(fullPath, []byte(content), 0o644)
}
