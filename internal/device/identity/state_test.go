package identity

import (
	"testing"
	"time"
)

// Rung 4 of the recovery ladder (plan 3.3): a device that reboots again and again
// must stop every automatic action and show its state.
//
// A reboot clears everything in RAM, so the count has to be on the disk. Nothing in
// Go counted reboots before: the only guard was the per-release boot counter of
// health-gate.sh, which says nothing about a hardware fault that kills the browser
// every ten minutes.
func TestRebootLoop(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	var s State

	if n, loop := s.RebootLoop(now); loop || n != 0 {
		t.Fatalf("a device that never rebooted is in a loop: %d %v", n, loop)
	}

	// Two reboots inside the hour are not a loop: four browser restarts give one
	// reboot, and one reboot that repairs the device is normal.
	s.MarkReboot(now.Add(-50 * time.Minute))
	s.MarkReboot(now.Add(-20 * time.Minute))
	if n, loop := s.RebootLoop(now); loop {
		t.Errorf("two reboots in an hour are a loop (%d)", n)
	}

	// The third one is.
	s.MarkReboot(now.Add(-time.Minute))
	n, loop := s.RebootLoop(now)
	if !loop || n != 3 {
		t.Errorf("three reboots in an hour gave %d, %v", n, loop)
	}

	// An hour later the window is empty again: a device that came up must not stay
	// in the loop state for ever.
	if n, loop := s.RebootLoop(now.Add(2 * time.Hour)); loop || n != 0 {
		t.Errorf("the window did not move: %d %v", n, loop)
	}

	// The list is bounded. A device that reboots for a month must not grow the state
	// file without end.
	for i := 0; i < MaxRebootLog*4; i++ {
		s.MarkReboot(now.Add(time.Duration(i) * time.Minute))
	}
	if len(s.Reboots) > MaxRebootLog {
		t.Errorf("the reboot list holds %d times, want %d at most", len(s.Reboots), MaxRebootLog)
	}
}

// The reboot times survive a write and a read of the state file.
func TestRebootLogRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	var s State
	s.MarkReboot(now)
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	back, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Reboots) != 1 || !back.Reboots[0].Equal(now) {
		t.Fatalf("the reboot times came back as %v", back.Reboots)
	}
}
