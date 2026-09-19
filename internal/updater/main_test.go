package updater

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The test binary is also a release binary.
//
// readBinaryInfo is the production path: it starts a program and reads the JSON of
// "version --json". A test double for it proves nothing about that path, and that
// is how the first fault reached a device. Every unit test gave Options a
// BinaryVersion of its own, so the real reader ran for the first time on real
// hardware, where it took the last field of a line for a person and compared the
// processor name with the release name.
//
// TestMain answers the same two arguments that portapixeld answers, with the same
// writer, so the real reader has a real program to read. The same trick works on
// Windows and on Linux and it needs no compiler (internal/device/browser does it
// for the stub browser).
const (
	selfName    = testBinary
	selfVersion = "1.5.0"
	selfArch    = testArch
)

// selfInfo is the answer of a test double that stands for this binary: the name
// and the processor of the fixture, with a version that the test chooses. A double
// must never leave the processor empty, because the apply path refuses a release
// for another processor.
func selfInfo(version string) BinaryInfo {
	return BinaryInfo{Name: selfName, Version: version, Arch: selfArch}
}

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "version" && os.Args[2] == "--json" {
		data, err := BinaryInfo{Name: selfName, Version: selfVersion, Arch: selfArch}.JSON()
		if err != nil {
			os.Exit(1)
		}
		os.Stdout.Write(data)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The production reader against a real program. It runs on Windows and on Linux.
func TestReadBinaryInfoReadsARealBinary(t *testing.T) {
	info, err := readBinaryInfo(os.Args[0])
	if err != nil {
		t.Fatalf("readBinaryInfo: %v", err)
	}
	if info.Name != selfName {
		t.Errorf("name = %q, want %q", info.Name, selfName)
	}
	if info.Version != selfVersion {
		t.Errorf("version = %q, want %q", info.Version, selfVersion)
	}
	if info.Arch != selfArch {
		t.Errorf("arch = %q, want %q", info.Arch, selfArch)
	}
}

// A file that is not a program of this project gives an error and no version. The
// reader must never make a version name out of any other output.
func TestReadBinaryInfoRefusesAnotherAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notabinary")
	mustWrite(t, path, []byte("portapixeld 1.5.0 amd64\n"))
	if _, err := readBinaryInfo(path); err == nil {
		t.Fatal("readBinaryInfo took a file that is not a program of this project")
	}
}

// The whole apply path with the PRODUCTION version reader: Options.BinaryVersion
// is nil, so the updater runs the staged binary and reads what it says. The staged
// binary is this test binary, which answers "version --json" above.
func TestApplyWithTheProductionVersionReader(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The release asset is "portapixeld-arm64", with no extension. Windows runs
		// no such file, and the device is Linux. The reader itself is proved above
		// on both systems.
		t.Skip("Windows runs only a file with an extension it knows")
	}
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	w := newWorld(t, func(o *Options) { o.BinaryVersion = nil })
	b := w.newBundle(string(self), false, false)
	_, rel := b.serve(t, selfVersion)

	if err := w.man.Apply(context.Background(), rel); err != nil {
		t.Fatalf("Apply with the production version reader: %v", err)
	}
	if got, want := w.current(), ReleasesDir+"/"+selfVersion; got != want {
		t.Errorf("current = %q, want %q", got, want)
	}
	if got := w.pending(); got != selfVersion {
		t.Errorf("the pending marker holds %q, want %q", got, selfVersion)
	}
}
