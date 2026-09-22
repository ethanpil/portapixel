package browser

import (
	"fmt"
	"time"
)

// The thresholds of the watchdog ladder (plan 3.3). They are all here, in one
// block, so that a change is one decision and not a search.
//
// Three of them are the DEFAULT values of the [watchdog] table: the ladder is
// tunable and can be switched off in portapixel.toml (D30). The supervisor reads
// the live values through Options.Watchdog at each check and hands them to the
// methods below, so a save takes effect without a restart.
const (
	// heartbeatTimeout is the silence that means "the player is gone". The SPA
	// sends a heartbeat every 5 seconds, so 30 seconds is six lost messages.
	heartbeatTimeout = 30 * time.Second
	// frameStalls is the number of heartbeats in a row that may carry the same
	// frame counter. The timers of a frozen page still fire, so the frame counter
	// is the only thing that says that the glass is dead (D45). It is not tunable:
	// three is a property of the protocol, not of a site.
	frameStalls = 3
	// restartWindow and restartsBeforeReboot are the second rung: four browser
	// restarts in one hour mean that a restart is not the answer, so the device
	// reboots.
	restartWindow        = time.Hour
	restartsBeforeReboot = 4
)

// WatchdogSettings are the live values of the ladder. They come from the
// [watchdog] table of portapixel.toml (D30).
type WatchdogSettings struct {
	// Enabled is false when the whole ladder is off. A browser that dies still
	// starts again; a page that stops sending heartbeats stays on the screen.
	Enabled bool
	// HeartbeatTimeout is the silence of the player that means "the page is dead".
	HeartbeatTimeout time.Duration
	// RestartWindow and RestartsBeforeReboot are the reboot rung. A limit of 0
	// means that the device never reboots by itself.
	RestartWindow        time.Duration
	RestartsBeforeReboot int
}

// DefaultWatchdog gives the values of the constants above. A Supervisor with no
// Watchdog function uses it, so a test and a tool need no configuration.
func DefaultWatchdog() WatchdogSettings {
	return WatchdogSettings{
		Enabled:              true,
		HeartbeatTimeout:     heartbeatTimeout,
		RestartWindow:        restartWindow,
		RestartsBeforeReboot: restartsBeforeReboot,
	}
}

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
//
// The counters run even while the ladder is off, so that the state is correct at
// the moment that a person switches it on again.
func (w *watchdog) heartbeat(now time.Time, frames int64, set WatchdogSettings) string {
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

	if w.stalls >= frameStalls && set.Enabled {
		w.stalls = 0
		return fmt.Sprintf("the frame counter stopped at %d across %d heartbeats", frames, frameStalls)
	}
	return ""
}

// idle gives the reason to restart the browser when no heartbeat arrives. The
// caller says if a URL window is open: our JavaScript is not on the page then,
// so silence is correct and the liveness poll takes over (plan 3.2).
func (w *watchdog) idle(now time.Time, urlWindow bool, set WatchdogSettings) string {
	if urlWindow || w.lastBeat.IsZero() || !set.Enabled {
		return ""
	}
	if silence := now.Sub(w.lastBeat); silence >= set.HeartbeatTimeout {
		return fmt.Sprintf("no heartbeat for %s after %d heartbeats", silence.Round(time.Second), w.beats)
	}
	return ""
}

// restarted records one browser restart. It gives the number of restarts inside
// the window and says if the device must reboot.
//
// Display absence never comes here (D44): a television in standby must not
// reboot the device every hour.
//
// A ladder that is off, and a limit of 0, both mean "never reboot". The count
// still runs: /api/status reports it, and a person who reads it learns that the
// browser restarts again and again.
func (w *watchdog) restarted(now time.Time, set WatchdogSettings) (int, bool) {
	cut := now.Add(-set.RestartWindow)
	kept := w.restarts[:0]
	for _, t := range w.restarts {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	w.restarts = append(kept, now)
	if !set.Enabled || set.RestartsBeforeReboot <= 0 {
		return len(w.restarts), false
	}
	return len(w.restarts), len(w.restarts) >= set.RestartsBeforeReboot
}

// rebootDone clears the restart history. The supervisor calls it after it asks
// for a reboot, so that a device whose reboot function does nothing does not ask
// again at every tick.
func (w *watchdog) rebootDone() {
	w.restarts = nil
}
