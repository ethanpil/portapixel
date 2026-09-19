package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The stub browser.
//
// The relaunch rung can only be tested against a real process, so the test
// binary is the stub browser: TestMain looks at the first argument and, when it
// is stubFlag, writes the URL into a file and waits until somebody stops it. The
// same trick works on Windows and on Linux, and it needs no compiler and no
// external program.
const stubFlag = "--pp-stub-browser"

// stubLife is how long the stub waits before it ends by itself. A test that
// fails must not leave a process for ever, and a process that lives on takes the
// processor from the tests that come after.
const stubLife = 20 * time.Second

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == stubFlag {
		stubBrowser(os.Args[2], os.Args[3:])
		return
	}
	os.Exit(m.Run())
}

// stubBrowser writes one line for each start: the URL that it received.
func stubBrowser(outPath string, args []string) {
	url := ""
	if len(args) > 0 {
		url = args[len(args)-1]
	}
	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err == nil {
		fmt.Fprintf(f, "%d\t%s\n", os.Getpid(), url)
		f.Close()
	}
	time.Sleep(stubLife)
}

// stub holds the state of a stub browser for one test.
type stub struct {
	out      string
	override string
}

// newStub makes the override command line that starts the stub browser.
func newStub(t *testing.T) *stub {
	t.Helper()
	out := filepath.Join(t.TempDir(), "starts.txt")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Quotation marks, because a temporary path can hold a space.
	return &stub{
		out:      out,
		override: fmt.Sprintf("%q %s %q %%u", self, stubFlag, out),
	}
}

// starts gives the URLs that the stub received, oldest first.
func (s *stub) starts(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(s.out)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			out = append(out, parts[1])
		}
	}
	return out
}

// waitFor waits until check is true. It keeps the tests short without a sleep of
// a fixed length.
//
// The deadline is generous because `go test ./...` runs the packages together and
// each of these tests starts a process. A test that is right must never fail
// because the machine was busy.
func waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
