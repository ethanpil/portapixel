package updater

import (
	"strconv"
	"strings"
)

// CompareVersions compares two release names. It gives a value below zero when a
// is older than b, zero when they are the same, and a value above zero when a is
// newer.
//
// The names are tags of the repository: "1.5.0", and "v1.5.0" is the same tag with
// the letter that some projects put in front. A name with no number in front is
// not a version: "dev" is the name of a build that CI did not make. Such a name is
// older than every real version, so a development build sees a release as an
// upgrade and a real release never goes back to "dev".
//
// A suffix such as "-rc1" makes a version older than the same version with no
// suffix. The release check already leaves prereleases out; this rule is here so
// that a fleet server which names one cannot cause a silent downgrade.
func CompareVersions(a, b string) int {
	aParts, aRest, aOK := parseVersion(a)
	bParts, bRest, bOK := parseVersion(b)

	switch {
	case !aOK && !bOK:
		return strings.Compare(a, b)
	case !aOK:
		return -1
	case !bOK:
		return 1
	}

	for i := range max(len(aParts), len(bParts)) {
		x, y := 0, 0
		if i < len(aParts) {
			x = aParts[i]
		}
		if i < len(bParts) {
			y = bParts[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}

	// The numbers are the same. A name with no suffix is the release itself and is
	// newer than any prerelease of it.
	switch {
	case aRest == bRest:
		return 0
	case aRest == "":
		return 1
	case bRest == "":
		return -1
	}
	return strings.Compare(aRest, bRest)
}

// parseVersion splits a release name into its numbers and the rest. It reports
// false when the name does not start with a number.
func parseVersion(name string) (parts []int, rest string, ok bool) {
	text := strings.TrimSpace(name)
	text = strings.TrimPrefix(text, "v")
	if text == "" || text[0] < '0' || text[0] > '9' {
		return nil, "", false
	}
	// The rest starts at the first character that is neither a digit nor a full
	// stop, for example the hyphen of "1.5.0-rc1".
	end := len(text)
	for i := 0; i < len(text); i++ {
		if (text[i] < '0' || text[i] > '9') && text[i] != '.' {
			end = i
			break
		}
	}
	rest = text[end:]
	for _, field := range strings.Split(text[:end], ".") {
		if field == "" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil {
			return nil, "", false
		}
		parts = append(parts, n)
	}
	if len(parts) == 0 {
		return nil, "", false
	}
	return parts, rest, true
}

// ValidVersion reports if a release name is safe as a directory name and in a
// URL. The name becomes a directory under <root>/releases, so a path step or a
// separator in it would put a release somewhere else.
func ValidVersion(name string) bool {
	if name == "" || len(name) > 64 || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r == '.', r == '-', r == '_', r == '+':
		default:
			return false
		}
	}
	// A name that starts with a full stop would hide the directory and would look
	// like the staging directory of the updater.
	return name[0] != '.'
}
