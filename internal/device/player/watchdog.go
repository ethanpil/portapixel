package player

import (
	"fmt"
	"math"
	"time"
)

// The thresholds of the watchdog ladder (plan 3.3). Three of them are the
// DEFAULT values of the [watchdog] table: the ladder is tunable and can be
// switched off in portapixel.toml (D30). The supervisor reads the live values
// through Options.Watchdog at each check, so a save takes effect without a
// restart.
const (
	// heartbeatTimeout is the default of watchdog.heartbeat_timeout. It is the
	// time that mpv may leave a request with no answer, the time that a video may
	// stand still, and the grace after the duration of an image.
	heartbeatTimeout = 30 * time.Second
	// restartWindow and restartsBeforeReboot are the reboot rung: four counted
	// restarts in one hour mean that a restart is not the answer.
	restartWindow        = time.Hour
	restartsBeforeReboot = 4
)

// WatchdogSettings are the live values of the ladder, from the [watchdog] table
// of portapixel.toml (D30).
type WatchdogSettings struct {
	// Enabled is false when the whole ladder is off. An mpv that ends still
	// starts again; an mpv that freezes stays on the screen.
	Enabled bool
	// HeartbeatTimeout is watchdog.heartbeat_timeout: the time that mpv may stay
	// silent or frozen.
	HeartbeatTimeout time.Duration
	// RestartWindow and RestartsBeforeReboot are the reboot rung. A limit of 0
	// means that the device never reboots by itself.
	RestartWindow        time.Duration
	RestartsBeforeReboot int
}

// DefaultWatchdog gives the values of the constants above. A Supervisor with no
// Watchdog function uses it.
func DefaultWatchdog() WatchdogSettings {
	return WatchdogSettings{
		Enabled:              true,
		HeartbeatTimeout:     heartbeatTimeout,
		RestartWindow:        restartWindow,
		RestartsBeforeReboot: restartsBeforeReboot,
	}
}

// ladder counts the restarts of the reboot rung. It has no clock and no side
// effects: each method takes the time from the caller.
type ladder struct {
	restarts []time.Time
}

// restarted records one counted restart. It gives the number of restarts inside
// the window and says if the device must reboot.
//
// Display absence never comes here (D44): a television in standby must not
// reboot the device every hour.
//
// A ladder that is off, and a limit of 0, both mean "never reboot". The count
// still runs: /api/status reports it, and a person who reads it learns that mpv
// restarts again and again.
func (l *ladder) restarted(now time.Time, set WatchdogSettings) (int, bool) {
	cut := now.Add(-set.RestartWindow)
	kept := l.restarts[:0]
	for _, t := range l.restarts {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	l.restarts = append(kept, now)
	if !set.Enabled || set.RestartsBeforeReboot <= 0 {
		return len(l.restarts), false
	}
	return len(l.restarts), len(l.restarts) >= set.RestartsBeforeReboot
}

// rebootDone clears the history. The supervisor calls it after it asks for a
// reboot, so that a device whose reboot function does nothing does not ask again
// at each restart.
func (l *ladder) rebootDone() { l.restarts = nil }

// checkWatchdog looks for the faults that the loop can see on the clock: an IPC
// socket that never came, a request that mpv did not answer, and an image that
// stays too long. A video that stands still is found when the answer about its
// position arrives (onTimePos), because only the answer can say that the
// position did not move.
//
// The order is the order of the ladder. Each fault restarts mpv one time.
func (s *Supervisor) checkWatchdog(now time.Time) {
	set := s.opt.Watchdog()
	if !set.Enabled {
		return
	}
	limit := set.HeartbeatTimeout

	if s.ipc == nil {
		if wait := now.Sub(s.dialSince); wait >= limit {
			s.restart(fmt.Sprintf("mpv gave no IPC socket in %s", wait.Round(time.Second)), true, resumeNext)
		}
		return
	}
	if at, ok := s.oldestRequest(); ok {
		if wait := now.Sub(at); wait >= limit {
			s.restart(fmt.Sprintf("mpv did not answer the IPC for %s", wait.Round(time.Second)), true, resumeNext)
			return
		}
	}
	// An image item that moves is an animation: mpv plays a GIF, PNG or WebP
	// with more than one frame as a video, for its own length, and it ignores
	// image-display-duration. Such an item is not stuck while its position
	// moves. A still image keeps its position, so the rule is the same for it.
	if it, ok := s.current(); ok && it.Kind == kindImage && !s.list.single && it.Duration > 0 {
		on := now.Sub(s.itemStart)
		if on >= imageLimit(it.Duration, limit) && now.Sub(s.lastMove) >= limit {
			s.restart(fmt.Sprintf("the image %s stayed on the screen for %s; its duration is %ds",
				it.Name, on.Round(time.Second), it.Duration), true, resumeNext)
		}
	}
}

// imageLimit gives the longest time that an image may stay: its duration and the
// grace. A duration of some billion seconds is more than a time.Duration holds.
// The sum was then less than zero, and the rule restarted mpv at each image.
func imageLimit(seconds int, grace time.Duration) time.Duration {
	if int64(seconds) >= int64((math.MaxInt64-grace)/time.Second) {
		return math.MaxInt64
	}
	return time.Duration(seconds)*time.Second + grace
}

// onTimePos takes an answer about the position of the item that plays. pos is
// false when mpv answered "property unavailable": a video that never shows a
// frame has no position, and that is a stall too. For an image item the answer
// only records a move (see checkWatchdog). Its first position is not a move.
func (s *Supervisor) onTimePos(value float64, pos bool, now time.Time) {
	it, ok := s.current()
	if !ok {
		return
	}
	if pos && (!s.posKnown || value != s.lastPos) {
		if s.posKnown || it.Kind == kindVideo {
			s.lastMove = now
		}
		s.lastPos, s.posKnown = value, true
		return
	}
	if it.Kind != kindVideo {
		return
	}
	set := s.opt.Watchdog()
	if !set.Enabled {
		return
	}
	if still := now.Sub(s.lastMove); still >= set.HeartbeatTimeout {
		s.restart(fmt.Sprintf("the video %s did not move for %s", it.Name, still.Round(time.Second)), true, resumeNext)
	}
}
