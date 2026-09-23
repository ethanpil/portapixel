// Package nodetest runs the node:test files of the web helpers from go test, so
// that go test ./... runs them too. The two admin UIs each have such a
// directory, and one copy of the rules keeps the two in agreement.
package nodetest

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run runs "node --test" on each *.test.mjs file of the working directory. Each
// such file reads the web module that ships.
//
// A machine with no node, or with a node older than 22.15, skips the test. The
// tests need two things of 22.15: a .js file with import lines loads as a module
// with no package.json, and module.registerHooks exists. In CI, where the CI
// environment variable is set, the same machine FAILS the test: a skip there
// hides a broken page.
//
// go test keeps a result until a file that the test binary read changes. node
// reads the web modules in a process of its own, so Run reads each of them first.
// Without this, a change to a web module gave the old result from the cache.
func Run(t *testing.T) {
	t.Helper()
	stop := t.Skipf
	if os.Getenv("CI") != "" {
		stop = t.Fatalf
	}
	node, err := exec.LookPath("node")
	if err != nil {
		stop("node is not installed")
		return
	}
	out, err := exec.Command(node, "-p", "process.versions.node").Output()
	if err != nil {
		stop("node does not give its version: %v", err)
		return
	}
	var major, minor int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d.%d", &major, &minor); err != nil ||
		major < 22 || (major == 22 && minor < 15) {
		stop("node %s is older than 22.15", strings.TrimSpace(string(out)))
		return
	}

	files, err := filepath.Glob("*.test.mjs")
	if err != nil || len(files) == 0 {
		t.Fatalf("found no *.test.mjs file (%v)", err)
	}
	readInputs(t, files)
	out, err = exec.Command(node, append([]string{"--test"}, files...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("node --test %v failed: %v\n%s", files, err, out)
	}
}

// readInputs reads the test files and every web module, so that go test knows
// them as inputs of this test.
func readInputs(t *testing.T, files []string) {
	t.Helper()
	for _, f := range files {
		if _, err := os.ReadFile(f); err != nil {
			t.Fatal(err)
		}
	}
	err := filepath.WalkDir(filepath.Join("..", "..", "web"), func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || filepath.Ext(path) != ".js" {
			return err
		}
		_, err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatalf("read the web modules: %v", err)
	}
}
