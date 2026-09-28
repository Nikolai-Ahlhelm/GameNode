package selfupdate

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// semverPattern is the same syntax the release workflow enforces for tags
// (.github/workflows/release.yml), with an optional leading "v". Local
// build-release.ps1 builds inject the bare form ("0.4.1") while CI release
// builds inject the tag ("v0.4.1"), so both must be accepted.
var semverPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// tagPattern is the strict shape of a release tag this package will ever
// place into a download URL: a "v" prefix plus semantic version. It contains
// no path separators, dots-only segments, or query characters.
var tagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

const maxVersionLength = 64

var ErrInvalidVersion = errors.New("invalid semantic version")

// Version is a parsed semantic version. Build metadata is retained only for
// display; like the semver specification, it never influences ordering.
type Version struct {
	Major, Minor, Patch uint64
	Prerelease          []string
	Build               string
}

func ParseVersion(text string) (Version, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > maxVersionLength {
		return Version{}, ErrInvalidVersion
	}
	match := semverPattern.FindStringSubmatch(text)
	if match == nil {
		return Version{}, ErrInvalidVersion
	}
	var v Version
	numbers := [3]*uint64{&v.Major, &v.Minor, &v.Patch}
	for i, target := range numbers {
		value, err := strconv.ParseUint(match[i+1], 10, 64)
		if err != nil {
			return Version{}, ErrInvalidVersion
		}
		*target = value
	}
	if match[4] != "" {
		v.Prerelease = strings.Split(match[4], ".")
		for _, identifier := range v.Prerelease {
			// Numeric prerelease identifiers must not carry leading zeros.
			if isNumeric(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return Version{}, ErrInvalidVersion
			}
		}
	}
	v.Build = match[5]
	return v, nil
}

// String renders the canonical form without a "v" prefix.
func (v Version) String() string {
	out := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Prerelease) > 0 {
		out += "-" + strings.Join(v.Prerelease, ".")
	}
	if v.Build != "" {
		out += "+" + v.Build
	}
	return out
}

// Tag renders the release-tag form ("v1.2.3").
func (v Version) Tag() string { return "v" + v.String() }

// Compare orders two versions by semantic-version precedence and returns -1,
// 0, or 1. Build metadata is ignored.
func Compare(a, b Version) int {
	for _, pair := range [3][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Prerelease) == 0 && len(b.Prerelease) == 0:
		return 0
	case len(a.Prerelease) == 0:
		return 1 // a release outranks any of its own prereleases
	case len(b.Prerelease) == 0:
		return -1
	}
	for i := 0; i < len(a.Prerelease) && i < len(b.Prerelease); i++ {
		if c := comparePrereleaseIdentifier(a.Prerelease[i], b.Prerelease[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Prerelease) < len(b.Prerelease):
		return -1
	case len(a.Prerelease) > len(b.Prerelease):
		return 1
	}
	return 0
}

func comparePrereleaseIdentifier(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		// Identifiers are bounded by maxVersionLength, so compare by length
		// then lexically rather than risking integer overflow.
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1 // numeric identifiers sort below alphanumeric ones
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ValidTag reports whether text is a release tag this package is willing to
// interpolate into a download URL.
func ValidTag(text string) bool {
	return len(text) <= maxVersionLength && tagPattern.MatchString(text)
}

// ValidVersionText reports whether text is a semantic version this package can
// compare (with or without a "v" prefix). API layers use it to reject a
// malformed version before it reaches the updater.
func ValidVersionText(text string) bool {
	_, err := ParseVersion(text)
	return err == nil
}

// CanonicalVersion renders a version in its canonical form without a "v"
// prefix, so audit trails and rollback records compare like with like even
// though release builds inject "v1.2.3" and local builds inject "1.2.3". A
// string that is not a semantic version is returned unchanged.
func CanonicalVersion(text string) string {
	if v, err := ParseVersion(text); err == nil {
		return v.String()
	}
	return text
}
