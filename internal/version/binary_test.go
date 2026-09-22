package version

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The normal form of a build name. A tag with the letter in front and the same tag
// without it are one release. Two names would make the daemon write
// health/v1.5.0.ok while the gate waits for health/1.5.0.ok, and the gate would
// then roll a GOOD release back and ban it for ever (final review 19).
func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"1.5.0":      "1.5.0",
		"v1.5.0":     "1.5.0",
		"V1.5.0":     "1.5.0",
		" v1.5.0 ":   "1.5.0",
		"v1.5.0-rc1": "1.5.0-rc1",
		"dev":        "dev",
		// Not a version with a letter in front: the letter belongs to the word.
		"vNext": "vNext",
		"v":     "v",
		"":      "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// The build name is already normal when any other package reads it. The init of
// this package does that, so no reader has to remember the rule.
func TestVersionIsAlreadyNormal(t *testing.T) {
	if got := Normalize(Version); got != Version {
		t.Fatalf("Version is %q and its normal form is %q; the init of this package must strip the letter", Version, got)
	}
}

// The line for a person. Its shape is a contract: the release workflow asserts
// "<name> <version> <arch>" for each binary it builds.
func TestCommandPrintsTheHumanLine(t *testing.T) {
	var out, errOut strings.Builder
	if code := Command("portapixeld", nil, &out, &errOut); code != 0 {
		t.Fatalf("Command() = %d, want 0 (%s)", code, errOut.String())
	}
	want := "portapixeld " + Version + " " + Arch() + "\n"
	if out.String() != want {
		t.Errorf("the line is %q, want %q", out.String(), want)
	}
}

// The machine-readable form. A build that carries a tag with the letter in front
// must still print the normal name, because the updater installs the release under
// the name that this answer gives.
func TestCommandPrintsTheJSONWithANormalVersion(t *testing.T) {
	saved := Version
	Version = "v1.2.3"
	defer func() { Version = saved }()

	var out, errOut strings.Builder
	if code := Command("portapixeld", []string{"--json"}, &out, &errOut); code != 0 {
		t.Fatalf("Command() = %d, want 0 (%s)", code, errOut.String())
	}
	var info BinaryInfo
	if err := json.Unmarshal([]byte(out.String()), &info); err != nil {
		t.Fatalf("the answer %q is not JSON: %v", out.String(), err)
	}
	if info.Name != "portapixeld" || info.Arch != Arch() {
		t.Errorf("the answer is %+v", info)
	}
	if info.Version != "1.2.3" {
		t.Errorf("version = %q, want %q: a letter from the build flags reached a consumer", info.Version, "1.2.3")
	}
	if !strings.HasSuffix(out.String(), "\n") {
		t.Error("the answer has no newline at the end")
	}
}

// An argument that this subcommand does not know is a fault and not a version.
func TestCommandRefusesAnotherArgument(t *testing.T) {
	var out, errOut strings.Builder
	if code := Command("portapixeld", []string{"--wrong"}, &out, &errOut); code == 0 {
		t.Fatal("Command() took an argument that it does not know")
	}
	if out.Len() != 0 {
		t.Errorf("it printed %q on the output for a program", out.String())
	}
}

// shortWriter stops after a few bytes, the way a pipe that closed does.
type shortWriter struct{ n int }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		return w.n, errors.New("broken pipe")
	}
	return len(p), nil
}

// A write that does not finish must give a non-zero code. The updater runs this
// subcommand on a staged release: a half-written line would make it blame the
// release with "unexpected end of JSON input", and the gate would then ban a
// release that is good.
func TestCommandReportsAShortWrite(t *testing.T) {
	var errOut strings.Builder
	if code := Command("portapixeld", []string{"--json"}, &shortWriter{n: 4}, &errOut); code == 0 {
		t.Fatal("Command() answered 0 after a write that did not finish")
	}
	if !strings.Contains(errOut.String(), "cannot print the version") {
		t.Errorf("the message for the operator is %q", errOut.String())
	}
}
