package browser

import (
	"fmt"
	"time"
)

// The thresholds of the watchdog ladder (plan 3.3). They are all here, in one
// block, so that a change is one decision and not a search.
const (
	// heartbeatTimeout is the silence that means "the player is gone". The SPA
	// sends a heartbeat every 5 seconds, so 30 seconds is six lost messages.
	heartbeatTimeout = 30 * time.Second
	// frameStalls is the number of heartbeats in a row that may carry the same
	// frame counter. The timers of a frozen page still fire, so the frame counter
	// is the only thing that says that the glass is dead (D45).
	frameStalls = 3
	// restartWindow and restartsBeforeReboot are the second rung: four browser
	// restarts in one hour mean that a restart is not the answer, so the device
	// reboots.
	restartWindow        = time.Hour
	restartsBeforeReboot = 4
)

// watchdog holds the counters of the ladder. It has no clock and no side
// effects: every method takes the time from the caller and gives back a reason
// or nothing. That makes the whole ladder testable with a clock that a test
// moves by hand.
type watchdog struct {
	lastBeat time.Time
	// beats counts the heartbeats since the browser started. It is in the ops
	// log line of a restart, because "no heartbeat ever" and "the heartbeats
	// stopped" are different faults.
	beats int

	lastFrames int64
	seenFrames bool
	stalls     int

	restarts []time.Time
}

// started resets the counters. The supervisor calls it when the browser starts
// and when a URL window ends, so the silence timer counts from the moment that
// the player could answer.
func (w *watchdog) started(now time.Time) {
	w.lastBeat = now
	w.beats = 0
	w.lastFrames = 0
	w.seenFrames = false
	w.stalls = 0
}

// heartbeat records one heartbeat. It gives the reason to restart the browser, or
// "" when everything is good.
//
// A frame counter that is the same as the one before is a stalled frame clock. A
// smaller counter is a page that loaded again, which is normal, so the counters
// start again.
func (w *watchdog) heartbeat(now time.Time, frames int64) string {
	w.lastBeat = now
	w.beats++

	switch {
	case !w.seenFrames || frames < w.lastFrames:
		w.stalls = 0
	case frames == w.lastFrames:
		w.stalls++
	default:
		w.stalls = 0
	}
	w.lastFrames = frames
	w.seenFrames = true

	if w.stalls >= frameStalls {
		w.stalls = 0
		return fmt.Sprintf("the frame counter stopped at %d across %d heartbeats", frames, frameStalls)
	}
	return ""
}

// idle gives the reason to restart the browser when no heartbeat arrives. The
// caller says if a URL window is open: our JavaScript is not on the page then,
// so silence is correct and the liveness poll takes over (plan 3.2).
func (w *watchdog) idle(now time.Time, urlWindow bool) string {
	if urlWindow || w.lastBeat.IsZero() {
		return ""
	}
	if silence := now.Sub(w.lastBeat); silence >= heartbeatTimeout {
		return fmt.Sprintf("no heartbeat for %s after %d heartbeats", silence.Round(time.Second), w.beats)
	}
	return ""
}

// restarted records one browser restart. It gives the number of restarts in the
// last hour and says if the device must reboot.
//
// Display absence never comes here (D44): a television in standby must not
// reboot the device every hour.
func (w *watchdog) restarted(now time.Time) (int, bool) {
	cut := now.Add(-restartWindow)
	kept := w.restarts[:0]
	for _, t := range w.restarts {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	w.restarts = append(kept, now)
	return len(w.restarts), len(w.restarts) >= restartsBeforeReboot
}

// rebootDone clears the restart history. The supervisor calls it after it asks
// for a reboot, so that a device whose reboot function does nothing does not ask
// again at every tick.
func (w *watchdog) rebootDone() {
	w.restarts = nil
}
