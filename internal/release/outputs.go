package release

import (
	"fmt"
	"strings"
)

// Outputs renders a release result as KEY=value lines. The format is both a
// GitHub Actions step output file and a GitLab CI dotenv report, so a CI job
// reads the result without parsing the human readable log.
//
// RELEASE_CREATED is true only when a release was actually written: a dry run
// or a run without releasable commits reports false.
func Outputs(result Result, dryRun bool) string {
	values := []struct {
		key   string
		value string
	}{
		{"RELEASE_CREATED", fmt.Sprint(result.Released && !dryRun)},
		{"RELEASE_VERSION", result.NextVersion},
		{"RELEASE_PREVIOUS_VERSION", result.LastVersion},
		{"RELEASE_TAG", result.TagName},
		{"RELEASE_COMMIT", result.ReleaseCommit},
		{"RELEASE_URL", result.ReleaseURL},
	}
	var builder strings.Builder
	for _, entry := range values {
		fmt.Fprintf(&builder, "%s=%s\n", entry.key, entry.value)
	}
	return builder.String()
}
