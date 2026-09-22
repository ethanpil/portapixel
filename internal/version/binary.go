package version

import (
	"encoding/json"
	"fmt"
	"io"
)

// BinaryInfo is what a binary of this project says about itself. The subcommand
// "version --json" prints exactly this shape, and the updater reads it before it
// installs a release.
//
// Why a machine-readable form exists: the human line is
// "portapixeld 1.5.0 amd64", and the first reader took the LAST field of it. It
// then compared the processor name with the release name and refused every real
// release with "the release says it is amd64 and the source called it 1.5.0". A
// position in a line for a person is not a contract. This struct is the contract,
// and the writer (Command) and the reader (internal/updater) both use it, so the
// two cannot disagree.
type BinaryInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
}

// JSON gives the one line that "version --json" prints, with the newline.
//
// The three fields are strings, so json.Marshal of this struct cannot fail. It
// therefore returns no error: a caller that had to handle an impossible fault
// wrote code that no test could reach.
func (i BinaryInfo) JSON() []byte {
	data, err := json.Marshal(i)
	if err != nil {
		// Unreachable: a struct of three strings always marshals. Give a body that
		// a reader refuses with a clear message, and never an empty answer.
		return []byte("{}\n")
	}
	return append(data, '\n')
}

// Command runs the "version" subcommand of a binary and gives its exit code.
// Both binaries of the project call this one function, so the two forms of the
// answer can never drift apart.
//
// The line with no flag is for a person and its shape never changes:
// "<name> <version> <arch>". "--json" prints the machine-readable form that
// internal/updater reads before it installs a release.
//
// A write that fails or that is short gives a non-zero code. The updater runs
// this subcommand on a staged release and reads the answer: a half-written line
// would make it blame the release with "unexpected end of JSON input", and the
// health gate would then ban a release that is good.
func Command(name string, args []string, stdout, stderr io.Writer) int {
	// Normalize again. The init of this package already did it for a build, but the
	// answer of this subcommand is what the updater installs a release under, so the
	// one place that must never print a letter in front does the work itself.
	release := Normalize(Version)

	var out []byte
	switch {
	case len(args) == 0:
		out = []byte(fmt.Sprintf("%s %s %s\n", name, release, Arch()))
	case len(args) == 1 && args[0] == "--json":
		out = BinaryInfo{Name: name, Version: release, Arch: Arch()}.JSON()
	default:
		fmt.Fprintf(stderr, "%s: version takes no argument but --json\n", name)
		return 1
	}
	n, err := stdout.Write(out)
	if err != nil || n != len(out) {
		fmt.Fprintf(stderr, "%s: cannot print the version: wrote %d of %d bytes: %v\n",
			name, n, len(out), err)
		return 1
	}
	return 0
}
