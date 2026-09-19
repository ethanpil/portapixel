package scheduler

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// probeTimeout is the time that the clock probe may take. chronyc talks to a
// local socket and answers in milliseconds. A probe that hangs must never hold
// the scheduler, so it gives up and reports "not synchronised".
const probeTimeout = 3 * time.Second

// firstTrueYear is the year from which we believe an unchecked clock. The
// project started in 2026, so a clock that says 2025 or later was either set by
// NTP or restored by swclock from a real shutdown time (D40). A device that
// wakes up in 1970 fails this test, which is the point.
const firstTrueYear = 2025

// ClockSynced reports if the clock is true.
//
// With chrony on the device, the answer is chrony's own answer: "Leap status" is
// "Normal" only after a real synchronisation. Without chrony, the answer is the
// year. This is the documented weaker test, and it exists because the on-box
// install path (D51) does not have to install chrony.
func ClockSynced(now func() time.Time) bool {
	if now == nil {
		now = time.Now
	}
	path, err := exec.LookPath("chronyc")
	if err != nil {
		return now().Year() >= firstTrueYear
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	// -n keeps chronyc from a DNS lookup of the server names, which would make
	// the probe as slow as the network.
	out, err := exec.CommandContext(ctx, path, "-n", "tracking").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Leap status") {
			return strings.Contains(line, "Normal")
		}
	}
	return false
}
