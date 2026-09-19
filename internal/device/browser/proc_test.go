package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// floodURL is the sentinel that turns the test binary into a stub browser that
// writes far more output than the cap.
//
// main_test.go already has a stub browser, but that one writes to a file and
// says nothing on its own streams. This stub is the test binary again, with
// -test.run for one helper test. The helper looks for the sentinel in its own
// arguments, so a normal run of the package skips it. The environment cannot
// carry the sentinel: CommandConfig.Build gives the child a fixed environment.
const floodURL = "pp-flood://output"

// floodLines and floodLine give about 2 MiB, which is two times the cap.
const (
	floodLines = 30000
	floodLine  = "the browser says something long enough to fill a log line quickly"
)

// TestStubFloodBrowser is the stub browser, not a test of its own. It writes to
// stdout and to stderr and then ends.
func TestStubFloodBrowser(t *testing.T) {
	found := false
	for _, a := range os.Args {
		if a == floodURL {
			found = true
		}
	}
	if !found {
		t.Skip("this test is the stub browser of TestBrowserLogStopsAtTheCap")
	}
	for i := 0; i < floodLines; i++ {
		fmt.Fprintf(os.Stdout, "%d %s\n", i, floodLine)
	}
	fmt.Fprintln(os.Stderr, "the stub browser ends")
}

// The browser must leave a readable log, and that log must never grow without
// bound: it sits on the same capped tmpfs as the Chromium profile (D39).
func TestBrowserLogStopsAtTheCap(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := CommandConfig{
		CacheDir: dir,
		// Quotation marks, because a temporary path can hold a space.
		Override: fmt.Sprintf("%q -test.run=^TestStubFloodBrowser$ %%u", self),
	}
	l := newLauncher(cfg, testLog(t))
	if err := l.start(floodURL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the stub browser to end", func() bool { return !l.alive() })

	data, err := os.ReadFile(filepath.Join(dir, LogName))
	if err != nil {
		t.Fatalf("there is no browser log: %v", err)
	}
	if len(data) > logCap {
		t.Errorf("the browser log is %d bytes, which is over the cap of %d", len(data), logCap)
	}
	if len(data) < logCap/2 {
		t.Errorf("the browser log is only %d bytes; the output did not reach it", len(data))
	}
	if !strings.HasSuffix(string(data), logNotice) {
		t.Errorf("the full log does not end with the notice; the last 80 bytes are %q",
			string(data[max(0, len(data)-80):]))
	}
	// The start of the output is the part that says why a browser failed early,
	// so the file keeps the beginning and drops the end.
	if !strings.HasPrefix(string(data), "0 "+floodLine) {
		t.Errorf("the log does not start with the first line: %q", string(data[:40]))
	}

	// The memory tail still holds the END of the output, which is what the one
	// ops log line at each exit needs.
	if tail := l.out.String(); !strings.Contains(tail, floodLine) {
		t.Errorf("the memory tail lost the output of the browser: %q", tail)
	}
	if reason := l.exitReason(); !strings.Contains(reason, "output: ") {
		t.Errorf("exitReason gives no output line: %q", reason)
	}
}

// A second launch must not add to the log of the launch before it. A person who
// looks at browser.log wants the run that is on the screen now.
func TestBrowserLogStartsAgainAtEachLaunch(t *testing.T) {
	dir := t.TempDir()
	s := newStub(t)
	l := newLauncher(CommandConfig{CacheDir: dir, Override: s.override}, testLog(t))

	path := filepath.Join(dir, LogName)
	if err := l.start("http://one/"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.start("http://two/"); err != nil {
		t.Fatal(err)
	}
	defer l.stop()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= 4096 {
		t.Errorf("the second launch kept the old log: %d bytes", info.Size())
	}
}

// A development machine has no capped tmpfs. The browser must still start.
func TestBrowserLogIsOptional(t *testing.T) {
	s := newStub(t)
	l := newLauncher(CommandConfig{Override: s.override}, testLog(t))
	if err := l.start("http://x/"); err != nil {
		t.Fatalf("the browser did not start without a cache directory: %v", err)
	}
	defer l.stop()
	waitFor(t, "the stub browser to write its line", func() bool { return len(s.starts(t)) == 1 })
}
