package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ethanpil/portapixel/internal/version"
)

// BinaryInfo is what a binary of this project says about itself. It lives in
// internal/version, which also holds the "version" subcommand that WRITES it, so
// the writer and the reader can never disagree. This name is the one the updater
// and its tests use.
type BinaryInfo = version.BinaryInfo

// maxVersionOutput is the most that a staged binary may print. The JSON line is
// under 100 bytes. A binary that prints without end must not fill the memory of a
// device with 512 MB.
const maxVersionOutput = 4 << 10

// readBinaryInfo runs a staged binary and reads what it says about itself. It
// runs only after the signature check, so the file is a file of the project.
//
// A sideloaded bundle is three files with no version in any name (D52), and the
// only place that holds the version is the binary itself.
//
// The child gets an empty environment and no input. It is a program that this
// device is about to install: it must not see the settings of the daemon, and it
// must not wait for a person to type. The context ends a binary that never stops,
// and the cap ends one that never stops printing.
func readBinaryInfo(path string) (BinaryInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "version", "--json")
	cmd.Env = []string{}
	cmd.Stdin = nil
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return BinaryInfo{}, err
	}
	if err := cmd.Start(); err != nil {
		return BinaryInfo{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, maxVersionOutput+1))
	if len(data) > maxVersionOutput {
		// Stop the process now. Without this it blocks on a pipe that nobody reads
		// and the caller waits for the whole timeout.
		cancel()
		_ = cmd.Wait()
		return BinaryInfo{}, fmt.Errorf("the binary printed more than %d bytes", maxVersionOutput)
	}
	if err := cmd.Wait(); err != nil {
		return BinaryInfo{}, err
	}
	if readErr != nil {
		return BinaryInfo{}, readErr
	}

	var info BinaryInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return BinaryInfo{}, fmt.Errorf("the binary answered %q and not the JSON of \"version --json\": %w",
			shortText(data), err)
	}
	// All three fields are the contract. A missing name is not a detail: verify
	// refuses a binary whose name is not the one this install runs, and a name that
	// the reader leaves empty would make that refusal pass by accident.
	if info.Name == "" || info.Version == "" || info.Arch == "" {
		return BinaryInfo{}, errors.New("the binary named no name, no version or no processor")
	}
	return info, nil
}

// shortText gives the first line of an answer for an error message, cut to a
// length that a log line can hold.
func shortText(data []byte) string {
	text := strings.TrimSpace(string(data))
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	if len(text) > 80 {
		text = text[:80]
	}
	return text
}

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

// NormalizeVersion gives the release name that this device uses in a directory
// name, in the pending marker and in the health marker.
//
// The rule itself lives in internal/version, which applies it to the build name
// at the source. This name is the one the release gate of CI calls
// (tests/ci/tagcheck), so both ends of the release read one function.
func NormalizeVersion(name string) string {
	return version.Normalize(name)
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
