package release

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Outputs renders a release result as KEY=value lines. The format is both a
// GitHub Actions step output file and a GitLab CI dotenv report, so a CI job
// reads the result without parsing the human readable log.
//
// RELEASE_CREATED is true only when a release was actually written: a dry run
// or a run without releasable commits reports false.
//
// A monorepo adds RELEASE_COMPONENTS, a JSON array of the released
// components, RELEASE_RELEASES, a JSON object of their versions, tags and
// URLs, and RELEASE_<COMPONENT>_* lines per configured component for GitLab,
// whose dotenv reports hold plain values better than JSON.
func Outputs(result Result, dryRun bool) string {
	created := result.Released && !dryRun
	values := []outputValue{
		{"RELEASE_CREATED", fmt.Sprint(created)},
		{"RELEASE_VERSION", result.NextVersion},
		{"RELEASE_PREVIOUS_VERSION", result.LastVersion},
		{"RELEASE_TAG", result.TagName},
		{"RELEASE_COMMIT", result.ReleaseCommit},
		{"RELEASE_URL", result.ReleaseURL},
	}
	if len(result.Components) > 0 {
		values = append(values, componentOutputs(result, created)...)
	}
	var builder strings.Builder
	for _, entry := range values {
		fmt.Fprintf(&builder, "%s=%s\n", entry.key, entry.value)
	}
	return builder.String()
}

type outputValue struct {
	key   string
	value string
}

// componentRelease is the JSON shape of one released component.
type componentRelease struct {
	Version         string `json:"version"`
	PreviousVersion string `json:"previous-version"`
	Tag             string `json:"tag"`
	URL             string `json:"release-url"`
}

func componentOutputs(result Result, created bool) []outputValue {
	released := []string{}
	releases := map[string]componentRelease{}
	var lines []outputValue
	for _, component := range result.Components {
		componentCreated := created && component.Released
		if componentCreated {
			released = append(released, component.Component)
			releases[component.Component] = componentRelease{
				Version:         component.NextVersion,
				PreviousVersion: component.LastVersion,
				Tag:             component.TagName,
				URL:             component.ReleaseURL,
			}
		}
		prefix := "RELEASE_" + outputName(component.Component) + "_"
		lines = append(lines,
			outputValue{prefix + "CREATED", fmt.Sprint(componentCreated)},
			outputValue{prefix + "VERSION", component.NextVersion},
			outputValue{prefix + "PREVIOUS_VERSION", component.LastVersion},
			outputValue{prefix + "TAG", component.TagName},
			outputValue{prefix + "URL", component.ReleaseURL},
		)
	}
	componentsJSON, _ := json.Marshal(released)
	releasesJSON, _ := json.Marshal(releases)
	return append([]outputValue{
		{"RELEASE_COMPONENTS", string(componentsJSON)},
		{"RELEASE_RELEASES", string(releasesJSON)},
	}, lines...)
}

// outputName turns a component into the part of a variable name: upper case,
// with every other character than a letter or a digit as an underscore.
func outputName(component string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, component)
}
