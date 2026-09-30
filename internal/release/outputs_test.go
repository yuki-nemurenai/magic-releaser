package release

import "testing"

func TestOutputsReportWhetherAReleaseWasWritten(t *testing.T) {
	released := Result{
		Released:      true,
		LastVersion:   "2026.09.0",
		NextVersion:   "2026.09.1",
		TagName:       "2026.09.1",
		ReleaseCommit: "abc",
		ReleaseURL:    "https://gitlab.example.com/g/p/-/releases/2026.09.1",
	}
	tests := []struct {
		name   string
		result Result
		dryRun bool
		want   string
	}{
		{"release", released, false, "RELEASE_CREATED=true\nRELEASE_VERSION=2026.09.1\nRELEASE_PREVIOUS_VERSION=2026.09.0\n" +
			"RELEASE_TAG=2026.09.1\nRELEASE_COMMIT=abc\nRELEASE_URL=https://gitlab.example.com/g/p/-/releases/2026.09.1\n"},
		{"dry run", Result{Released: true, NextVersion: "2026.09.1", TagName: "2026.09.1"}, true,
			"RELEASE_CREATED=false\nRELEASE_VERSION=2026.09.1\nRELEASE_PREVIOUS_VERSION=\nRELEASE_TAG=2026.09.1\nRELEASE_COMMIT=\nRELEASE_URL=\n"},
		{"nothing to release", Result{LastVersion: "2026.09.0"}, false,
			"RELEASE_CREATED=false\nRELEASE_VERSION=\nRELEASE_PREVIOUS_VERSION=2026.09.0\nRELEASE_TAG=\nRELEASE_COMMIT=\nRELEASE_URL=\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Outputs(test.result, test.dryRun); got != test.want {
				t.Fatalf("Outputs() =\n%s\nwant\n%s", got, test.want)
			}
		})
	}
}
