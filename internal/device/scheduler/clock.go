package scheduler

import "time"

// firstTrueYear is the year from which we believe an unchecked clock. The
// project started in 2026, so a clock that says 2025 or later was either set by
// NTP or restored by swclock from a real shutdown time (D40). A device that
// wakes up in 1970 fails this test, which is the point.
const firstTrueYear = 2025

// ClockSynced reports if the clock is true.
//
// On Linux the answer comes from the kernel. chrony clears the STA_UNSYNC flag
// when it takes the clock, and adjtimex reads the flag with one system call.
//
// The probe used to run chronyc and read its words, and /api/status called it on
// every request: nothing in the path of a request may start a process. The
// daemon now samples this on its own loop and hands the answer to the report.
//
// Away from Linux the answer is the year. This is the documented weaker test, and
// it is also what the on-box install path (D51) gets when it has no chrony.
func ClockSynced(now func() time.Time) bool {
	if now == nil {
		now = time.Now
	}
	if synced, ok := kernelSynced(); ok {
		return synced
	}
	return now().Year() >= firstTrueYear
}
