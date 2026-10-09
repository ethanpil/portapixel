//go:build linux

package player

import (
	"os"
	"slices"
	"testing"
)

// statusOwner reads the real user ID from /proc/<pid>/status. A zombie holds no
// file any more, so it is not a stray that holds the DRM master.
func TestStatusOwner(t *testing.T) {
	tests := []struct {
		name   string
		status string
		uid    int
		live   bool
	}{
		{"running", "Name:\tmpv\nState:\tS (sleeping)\nUid:\t1001\t1001\t1001\t1001\nGid:\t1001\n", 1001, true},
		{"zombie", "Name:\tmpv\nState:\tZ (zombie)\nUid:\t1001\t1001\t1001\t1001\n", 1001, false},
		{"dead", "State:\tX (dead)\nUid:\t1001\t1001\t1001\t1001\n", 1001, false},
		{"real ID first", "State:\tR (running)\nUid:\t0\t1001\t1001\t1001\n", 0, true},
		{"no Uid line", "Name:\tmpv\nState:\tS (sleeping)\n", -1, false},
		{"empty", "", -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid, live := statusOwner(tt.status)
			if live != tt.live || (tt.live && uid != tt.uid) {
				t.Errorf("statusOwner = %d, %v; want %d, %v", uid, live, tt.uid, tt.live)
			}
		})
	}
}

// processesOf finds this test process among the processes of its own user.
func TestProcessesOfFindsThisProcess(t *testing.T) {
	if !slices.Contains(processesOf(os.Getuid()), os.Getpid()) {
		t.Errorf("the process %d of the user %d is not in the list", os.Getpid(), os.Getuid())
	}
}

// With no kiosk account there is nothing to stop: a development machine runs
// mpv as its own user, and that user runs everything else too.
func TestKillStrayWithNoKioskAccount(t *testing.T) {
	if n, err := killStray(""); n != 0 || err != nil {
		t.Errorf("killStray(\"\") = %d, %v", n, err)
	}
}

// root and the account of the daemon are never swept: they run more than mpv,
// and the daemon is one of their processes. The test asks the rule and does not
// call killStray, which would stop real processes if the rule were wrong.
func TestTheAccountOfTheDaemonIsNeverSwept(t *testing.T) {
	if sweepable(0) {
		t.Error("root is sweepable")
	}
	if sweepable(os.Getuid()) {
		t.Error("the account of this process is sweepable")
	}
	if !sweepable(os.Getuid() + 12345) {
		t.Error("another account is not sweepable")
	}
}
