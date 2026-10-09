package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ethanpil/portapixel/internal/server/db"
)

// busyDataDir makes a data directory as a server leaves it while a mirror runs: a
// row that says "working" and a staging directory with a part of a download.
func busyDataDir(t *testing.T) (dir, staging string) {
	t.Helper()
	dir = t.TempDir()
	// A password hash is there, so that the open does not print a first-run password.
	cfg := defaults()
	cfg.AdminPasswordHash = "$2a$10$abcdefghijklmnopqrstuv"
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	database, err := openDatabase(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetMirrorState("1.5.0", db.MirrorWorking, ""); err != nil {
		t.Fatal(err)
	}
	database.Close()

	staging = filepath.Join(dir, "releases", ".staging-1.5.0-123")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, staging
}

// mirrorState reads the state of the mirror of 1.5.0 from the data directory.
func mirrorState(t *testing.T, dir string) string {
	t.Helper()
	database, err := openDatabase(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rel, err := database.Release("1.5.0")
	if err != nil {
		t.Fatal(err)
	}
	return rel.MirrorState
}

// TestSelftestLeavesAMirrorThatRuns covers "selftest" on the data directory of a
// server that runs, for example with "docker exec". The check used to mark the mirror
// of that server as failed and to delete its staging directory.
func TestSelftestLeavesAMirrorThatRuns(t *testing.T) {
	dir, staging := busyDataDir(t)

	if code := selftestCommand([]string{"--data", dir}); code != 0 {
		t.Fatalf("the selftest gave exit code %d", code)
	}

	if got := mirrorState(t, dir); got != db.MirrorWorking {
		t.Fatalf("the selftest changed the mirror state to %q", got)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("the selftest removed the staging directory: %v", err)
	}
}

// TestTheServerClearsWhatAStoppedProcessLeft keeps the other half. The recovery
// moved out of the shared open for the selftest, and "run" must still do it.
func TestTheServerClearsWhatAStoppedProcessLeft(t *testing.T) {
	dir, staging := busyDataDir(t)

	srv, err := newServer(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()

	if got := mirrorState(t, dir); got != db.MirrorFailed {
		t.Fatalf("the mirror state after a start is %q, want %q", got, db.MirrorFailed)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("the staging directory is still there: %v", err)
	}
}
