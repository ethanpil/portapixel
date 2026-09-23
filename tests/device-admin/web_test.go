package webtest

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWebHelpers runs the node tests of this directory, so that go test ./...
// runs them too. Each *.test.mjs file reads the web module that ships.
//
// A machine with no node, or with a node older than 22.15, skips the test. The
// tests need two things of 22.15: a .js file with import lines loads as a module
// with no package.json, and module.registerHooks exists.
func TestWebHelpers(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "-p", "process.versions.node").Output()
	var major, minor int
	if err != nil {
		t.Skipf("node does not give its version: %v", err)
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d.%d", &major, &minor); err != nil ||
		major < 22 || (major == 22 && minor < 15) {
		t.Skipf("node %s is older than 22.15", strings.TrimSpace(string(out)))
	}
	files, err := filepath.Glob("*.test.mjs")
	if err != nil || len(files) == 0 {
		t.Fatalf("found no *.test.mjs file (%v)", err)
	}
	out, err = exec.Command(node, append([]string{"--test"}, files...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("node --test %v failed: %v\n%s", files, err, out)
	}
}
