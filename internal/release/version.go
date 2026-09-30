package release

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type semanticVersion struct {
	Major int
	Minor int
	Patch int
}

// calendarVersion is the value a CalVer layout renders and parses.
// Day is only used by daily layouts such as YYYY.MM.DD.
type calendarVersion struct {
	Year  int
	Month int
	Day   int
	Micro int
}

// DefaultCalVerFormat keeps the historical shape of the project.
const DefaultCalVerFormat = "YYYY.0M.MICRO"

var semverPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

func NextVersionWithCalVer(strategy Versioning, calverFormat, last string, level Level, now time.Time) (string, error) {
	if level == ReleaseNone {
		return last, nil
	}

	switch strategy {
	case VersioningSemVer:
		return NextSemVer(last, level)
	case VersioningCalVer:
		return NextCalVer(calverFormat, last, now)
	default:
		return "", fmt.Errorf("unsupported versioning strategy %q", strategy)
	}
}

func NextSemVer(last string, level Level) (string, error) {
	if last == "" {
		return "1.0.0", nil
	}

	version, err := parseSemVer(last)
	if err != nil {
		return "", err
	}

	switch level {
	case ReleaseMajor:
		version.Major++
		version.Minor = 0
		version.Patch = 0
	case ReleaseMinor:
		version.Minor++
		version.Patch = 0
	case ReleasePatch:
		version.Patch++
	default:
		return version.String(), nil
	}
	return version.String(), nil
}

// NextCalVer computes the next calendar version.
//
// CalVer is kept clean: the release level (patch/minor/major) deliberately does
// not influence the resulting version, because the version encodes the release
// date rather than the shape of the change. A breaking change is communicated
// through the release notes instead.
//
// The version is always strictly greater than last. If the previous release
// carries a timestamp in the future relative to now (clock skew, or a release
// prepared ahead of time), the calendar position is preserved instead of
// rolling back to an older, lower version.
func NextCalVer(layout, last string, now time.Time) (string, error) {
	segments, err := validateCalVerLayout(layout)
	if err != nil {
		return "", err
	}
	usesDay := layoutHasToken(segments, "day", "dayPadded")
	usesMonth := layoutHasToken(segments, "month", "monthPadded")
	current := calendarVersion{Year: now.Year(), Micro: 0}
	// Components the layout does not mention must not take part in any
	// comparison, otherwise two releases in the same month would look like
	// releases in different months.
	if usesMonth {
		current.Month = int(now.Month())
	}
	if usesDay {
		current.Day = now.Day()
	}
	if last == "" {
		return FormatCalVer(layout, current)
	}

	previous, err := parseCalVer(layout, last)
	if err != nil {
		return "", err
	}
	if sameCalendarDay(previous, current) {
		if usesDay && !layoutHasToken(segments, "micro") {
			// A daily layout cannot encode two releases on the same day, and
			// silently reusing the version would collide with the existing tag.
			return "", fmt.Errorf(
				"cannot release twice on %04d-%02d-%02d with calver format %q, which has no MICRO token; add one, for example YYYY.MM.DD.MICRO",
				current.Year, current.Month, current.Day, layout,
			)
		}
		current.Micro = previous.Micro + 1
	} else if previous.After(current) {
		current = previous
		current.Micro = previous.Micro + 1
	}
	return FormatCalVer(layout, current)
}

func sameCalendarDay(left, right calendarVersion) bool {
	return left.Year == right.Year && left.Month == right.Month && left.Day == right.Day
}

func VersionFromTag(tagFormat, tag string) (string, bool) {
	prefix, suffix, ok := tagFormatParts(tagFormat)
	if !ok {
		return "", false
	}
	if !strings.HasPrefix(tag, prefix) || !strings.HasSuffix(tag, suffix) {
		return "", false
	}
	version := strings.TrimSuffix(strings.TrimPrefix(tag, prefix), suffix)
	return version, version != ""
}

func TagName(tagFormat, version string) (string, error) {
	if _, _, ok := tagFormatParts(tagFormat); !ok {
		return "", fmt.Errorf("tag format must contain {{version}}")
	}
	return strings.ReplaceAll(tagFormat, "{{version}}", version), nil
}

func CompareVersionsWithCalVer(strategy Versioning, calverFormat, left, right string) (int, error) {
	switch strategy {
	case VersioningSemVer:
		leftVersion, err := parseSemVer(left)
		if err != nil {
			return 0, err
		}
		rightVersion, err := parseSemVer(right)
		if err != nil {
			return 0, err
		}
		return compareInts(
			[]int{leftVersion.Major, leftVersion.Minor, leftVersion.Patch},
			[]int{rightVersion.Major, rightVersion.Minor, rightVersion.Patch},
		), nil
	case VersioningCalVer:
		leftVersion, err := parseCalVer(calverFormat, left)
		if err != nil {
			return 0, err
		}
		rightVersion, err := parseCalVer(calverFormat, right)
		if err != nil {
			return 0, err
		}
		return compareInts(
			[]int{leftVersion.Year, leftVersion.Month, leftVersion.Day, leftVersion.Micro},
			[]int{rightVersion.Year, rightVersion.Month, rightVersion.Day, rightVersion.Micro},
		), nil
	default:
		return 0, fmt.Errorf("unsupported versioning strategy %q", strategy)
	}
}

func ValidVersionWithCalVer(strategy Versioning, calverFormat, value string) bool {
	switch strategy {
	case VersioningSemVer:
		_, err := parseSemVer(value)
		return err == nil
	case VersioningCalVer:
		_, err := parseCalVer(calverFormat, value)
		return err == nil
	default:
		return false
	}
}

func parseSemVer(value string) (semanticVersion, error) {
	matches := semverPattern.FindStringSubmatch(value)
	if matches == nil {
		return semanticVersion{}, fmt.Errorf("invalid semver %q", value)
	}
	major, _ := strconv.Atoi(matches[1])
	minor, _ := strconv.Atoi(matches[2])
	patch, _ := strconv.Atoi(matches[3])
	return semanticVersion{Major: major, Minor: minor, Patch: patch}, nil
}

func (version semanticVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", version.Major, version.Minor, version.Patch)
}

// After reports whether version is positioned later in calendar time than other.
func (version calendarVersion) After(other calendarVersion) bool {
	return compareInts(
		[]int{version.Year, version.Month, version.Day},
		[]int{other.Year, other.Month, other.Day},
	) > 0
}

// calverToken is one component of a CalVer layout.
type calverToken struct {
	text  string
	field string
}

// calverTokens maps every supported layout token to its meaning. The order
// matters: the first match wins, so MICRO and PATCH are tried before M and D,
// and YYYY before YY.
var calverTokens = []calverToken{
	{text: "YYYY", field: "year"},
	{text: "YY", field: "yearShort"},
	{text: "MICRO", field: "micro"},
	{text: "PATCH", field: "micro"},
	{text: "0M", field: "monthPadded"},
	{text: "MM", field: "monthPadded"},
	{text: "0D", field: "dayPadded"},
	{text: "DD", field: "dayPadded"},
	{text: "M", field: "month"},
	{text: "D", field: "day"},
}

// calverSegment is either a literal separator or a token in a split layout.
type calverSegment struct {
	literal string
	token   calverToken
	isToken bool
}

// splitCalVerLayout walks the layout once and separates tokens from the
// literal text between them. Unknown words are kept as literals rather than
// rejected, so a format like "release-YYYY.MM" still renders.
func splitCalVerLayout(layout string) []calverSegment {
	var segments []calverSegment
	index := 0
	for index < len(layout) {
		if token, width := matchCalVerToken(layout[index:]); width > 0 {
			segments = append(segments, calverSegment{token: token, isToken: true})
			index += width
			continue
		}
		start := index
		for index < len(layout) {
			if _, width := matchCalVerToken(layout[index:]); width > 0 {
				break
			}
			index++
		}
		segments = append(segments, calverSegment{literal: layout[start:index]})
	}
	return segments
}

func matchCalVerToken(input string) (calverToken, int) {
	for _, token := range calverTokens {
		if strings.HasPrefix(input, token.text) {
			return token, len(token.text)
		}
	}
	return calverToken{}, 0
}

func layoutHasToken(segments []calverSegment, fields ...string) bool {
	for _, segment := range segments {
		if !segment.isToken {
			continue
		}
		for _, field := range fields {
			if segment.token.field == field {
				return true
			}
		}
	}
	return false
}

func validateCalVerLayout(layout string) ([]calverSegment, error) {
	if strings.TrimSpace(layout) == "" {
		return nil, fmt.Errorf("calver format must not be empty")
	}
	segments := splitCalVerLayout(layout)
	if !layoutHasToken(segments, "year", "yearShort") {
		return nil, fmt.Errorf("calver format %q must contain a year token (YYYY or YY)", layout)
	}
	if !layoutHasToken(segments, "month", "monthPadded", "day", "dayPadded", "micro") {
		return nil, fmt.Errorf("calver format %q must contain a month, day or MICRO token", layout)
	}
	return segments, nil
}

// FormatCalVer renders a calendar version according to a layout.
func FormatCalVer(layout string, version calendarVersion) (string, error) {
	segments, err := validateCalVerLayout(layout)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	for _, segment := range segments {
		if !segment.isToken {
			builder.WriteString(segment.literal)
			continue
		}
		builder.WriteString(renderCalVerToken(segment.token, version))
	}
	return builder.String(), nil
}

func renderCalVerToken(token calverToken, version calendarVersion) string {
	switch token.field {
	case "year":
		return fmt.Sprintf("%04d", version.Year)
	case "yearShort":
		return fmt.Sprintf("%02d", version.Year%100)
	case "monthPadded":
		return fmt.Sprintf("%02d", version.Month)
	case "dayPadded":
		return fmt.Sprintf("%02d", version.Day)
	case "month":
		return strconv.Itoa(version.Month)
	case "day":
		return strconv.Itoa(version.Day)
	case "micro":
		return strconv.Itoa(version.Micro)
	default:
		return ""
	}
}

// parseCalVer reads a calendar version written with the given layout.
func parseCalVer(layout, value string) (calendarVersion, error) {
	segments, err := validateCalVerLayout(layout)
	if err != nil {
		return calendarVersion{}, err
	}
	pattern, fields := calVerPattern(segments)
	matches := pattern.FindStringSubmatch(value)
	if matches == nil {
		return calendarVersion{}, fmt.Errorf("invalid calver %q for format %q", value, layout)
	}

	version := calendarVersion{}
	for index, field := range fields {
		number, convErr := strconv.Atoi(matches[index+1])
		if convErr != nil {
			return calendarVersion{}, fmt.Errorf("invalid calver %q for format %q", value, layout)
		}
		switch field {
		case "year":
			version.Year = number
		case "yearShort":
			version.Year = 2000 + number
		case "month", "monthPadded":
			version.Month = number
		case "day", "dayPadded":
			version.Day = number
		case "micro":
			version.Micro = number
		}
	}

	// A component that the layout does not mention stays zero and must not be
	// range checked: a YYYY.PATCH layout has no month at all.
	if layoutHasToken(segments, "month", "monthPadded") && (version.Month < 1 || version.Month > 12) {
		return calendarVersion{}, fmt.Errorf("invalid calver %q: month %d is out of range", value, version.Month)
	}
	if layoutHasToken(segments, "day", "dayPadded") && (version.Day < 1 || version.Day > 31) {
		return calendarVersion{}, fmt.Errorf("invalid calver %q: day %d is out of range", value, version.Day)
	}
	return version, nil
}

func calVerPattern(segments []calverSegment) (*regexp.Regexp, []string) {
	var builder strings.Builder
	var fields []string
	builder.WriteString("^")
	for _, segment := range segments {
		if !segment.isToken {
			builder.WriteString(regexp.QuoteMeta(segment.literal))
			continue
		}
		switch segment.token.field {
		case "year":
			builder.WriteString(`(\d{4})`)
		case "yearShort":
			builder.WriteString(`(\d{2})`)
		case "monthPadded", "dayPadded":
			builder.WriteString(`(\d{2})`)
		case "month", "day":
			builder.WriteString(`(\d{1,2})`)
		case "micro":
			builder.WriteString(`(\d+)`)
		}
		fields = append(fields, segment.token.field)
	}
	builder.WriteString("$")
	return regexp.MustCompile(builder.String()), fields
}

func tagFormatParts(format string) (string, string, bool) {
	const placeholder = "{{version}}"
	before, after, ok := strings.Cut(format, placeholder)
	if !ok {
		return "", "", false
	}
	return before, after, true
}

func compareInts(left, right []int) int {
	for index := range left {
		switch {
		case left[index] > right[index]:
			return 1
		case left[index] < right[index]:
			return -1
		}
	}
	return 0
}
