package browser

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// clock is a time that a test moves by hand. Every threshold of the ladder is a
// duration, so the whole ladder can be tested without waiting for anything.
//
// The lock is there because a supervisor test gives now to the loop goroutine and
// moves the time from the test goroutine.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestWatchdogHeartbeatLoss(t *testing.T) {
	c := newClock()
	var w watchdog
	w.started(c.now())

	// A heartbeat every 5 seconds keeps the browser.
	for i := 0; i < 20; i++ {
		c.add(5 * time.Second)
		if reason := w.heartbeat(c.now(), int64(100+i*17), DefaultWatchdog()); reason != "" {
			t.Fatalf("a good heartbeat asked for a restart: %s", reason)
		}
		if reason := w.idle(c.now(), false, DefaultWatchdog()); reason != "" {
			t.Fatalf("idle asked for a restart after a heartbeat: %s", reason)
		}
	}

	// Silence under the timeout is not a fault.
	c.add(heartbeatTimeout - time.Second)
	if reason := w.idle(c.now(), false, DefaultWatchdog()); reason != "" {
		t.Fatalf("idle acted one second early: %s", reason)
	}
	// Silence at the timeout is.
	c.add(time.Second)
	if reason := w.idle(c.now(), false, DefaultWatchdog()); reason == "" {
		t.Fatal("idle did not act at the timeout")
	}
}

func TestWatchdogURLWindowSuspendsExpectations(t *testing.T) {
	c := newClock()
	var w watchdog
	w.started(c.now())
	c.add(10 * time.Minute) // a long URL item

	if reason := w.idle(c.now(), true, DefaultWatchdog()); reason != "" {
		t.Fatalf("idle acted during a URL window: %s", reason)
	}
	// The window ends. The counter starts again, so the player gets its full
	// time to answer.
	w.started(c.now())
	c.add(heartbeatTimeout - time.Second)
	if reason := w.idle(c.now(), false, DefaultWatchdog()); reason != "" {
		t.Fatalf("idle acted too early after the window: %s", reason)
	}
	c.add(time.Second)
	if reason := w.idle(c.now(), false, DefaultWatchdog()); reason == "" {
		t.Fatal("idle did not act after the window")
	}
}

func TestWatchdogFrameStall(t *testing.T) {
	c := newClock()
	var w watchdog
	w.started(c.now())

	c.add(5 * time.Second)
	w.heartbeat(c.now(), 1000, DefaultWatchdog())
	// The same counter, twice: not enough yet. The frames are the same for a
	// moment when a video ends and the next item loads.
	for i := 0; i < frameStalls-1; i++ {
		c.add(5 * time.Second)
		if reason := w.heartbeat(c.now(), 1000, DefaultWatchdog()); reason != "" {
			t.Fatalf("the watchdog acted after %d equal counters: %s", i+1, reason)
		}
	}
	c.add(5 * time.Second)
	if reason := w.heartbeat(c.now(), 1000, DefaultWatchdog()); reason == "" {
		t.Fatalf("the watchdog did not act after %d equal counters", frameStalls)
	}

	// A counter that grows again clears the count.
	c.add(5 * time.Second)
	w.heartbeat(c.now(), 1001, DefaultWatchdog())
	c.add(5 * time.Second)
	if reason := w.heartbeat(c.now(), 1001, DefaultWatchdog()); reason != "" {
		t.Fatalf("the count did not clear: %s", reason)
	}
}

func TestWatchdogFrameCounterRestartIsNotAStall(t *testing.T) {
	c := newClock()
	var w watchdog
	w.started(c.now())
	w.heartbeat(c.now(), 50000, DefaultWatchdog())
	// The page loaded again, so the counter starts at a low number.
	for i := 0; i < frameStalls+2; i++ {
		c.add(5 * time.Second)
		if reason := w.heartbeat(c.now(), int64(10+i), DefaultWatchdog()); reason != "" {
			t.Fatalf("a page that loaded again looks like a stall: %s", reason)
		}
	}
}

func TestWatchdogFourRestartsInAnHourReboot(t *testing.T) {
	c := newClock()
	var w watchdog

	for i := 1; i < restartsBeforeReboot; i++ {
		c.add(5 * time.Minute)
		count, reboot := w.restarted(c.now(), DefaultWatchdog())
		if count != i || reboot {
			t.Fatalf("restart %d gave count=%d reboot=%v", i, count, reboot)
		}
	}
	c.add(5 * time.Minute)
	count, reboot := w.restarted(c.now(), DefaultWatchdog())
	if count != restartsBeforeReboot || !reboot {
		t.Fatalf("restart %d gave count=%d reboot=%v", restartsBeforeReboot, count, reboot)
	}
}

func TestWatchdogRestartsOutsideTheWindowDoNotCount(t *testing.T) {
	c := newClock()
	var w watchdog

	for i := 0; i < 10; i++ {
		c.add(restartWindow + time.Minute) // one restart every hour and a minute
		count, reboot := w.restarted(c.now(), DefaultWatchdog())
		if count != 1 || reboot {
			t.Fatalf("restart %d gave count=%d reboot=%v", i, count, reboot)
		}
	}
}

func TestWatchdogRebootDoneClearsTheHistory(t *testing.T) {
	c := newClock()
	var w watchdog
	for i := 0; i < restartsBeforeReboot; i++ {
		w.restarted(c.now(), DefaultWatchdog())
		c.add(time.Minute)
	}
	w.rebootDone()
	if count, reboot := w.restarted(c.now(), DefaultWatchdog()); count != 1 || reboot {
		t.Fatalf("count=%d reboot=%v after rebootDone", count, reboot)
	}
}

// The whole ladder is tunable in portapixel.toml, and it can be switched off
// (D30). The supervisor reads the values at each check and hands them over, so
// every threshold is a value that a test moves.
func TestWatchdogTimeoutIsTunable(t *testing.T) {
	tests := []struct {
		name    string
		set     WatchdogSettings
		silence time.Duration
		want    bool
	}{
		{name: "the default acts at 30 seconds", set: DefaultWatchdog(), silence: 30 * time.Second, want: true},
		{name: "the default holds at 29 seconds", set: DefaultWatchdog(), silence: 29 * time.Second},
		{
			name:    "a longer timeout holds",
			set:     WatchdogSettings{Enabled: true, HeartbeatTimeout: 2 * time.Minute},
			silence: 90 * time.Second,
		},
		{
			name:    "a longer timeout acts at its own value",
			set:     WatchdogSettings{Enabled: true, HeartbeatTimeout: 2 * time.Minute},
			silence: 2 * time.Minute,
			want:    true,
		},
		{
			name:    "a shorter timeout acts earlier",
			set:     WatchdogSettings{Enabled: true, HeartbeatTimeout: 10 * time.Second},
			silence: 10 * time.Second,
			want:    true,
		},
		{
			name:    "a ladder that is off never acts",
			set:     WatchdogSettings{HeartbeatTimeout: 10 * time.Second},
			silence: time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClock()
			var w watchdog
			w.started(c.now())
			c.add(tt.silence)
			if got := w.idle(c.now(), false, tt.set) != ""; got != tt.want {
				t.Fatalf("idle asked for a restart: %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWatchdogRebootLimitIsTunable(t *testing.T) {
	hour := WatchdogSettings{Enabled: true, RestartWindow: time.Hour}
	tests := []struct {
		name string
		set  WatchdogSettings
		// restarts is how many restarts happen, one every step.
		restarts int
		step     time.Duration
		want     bool
	}{
		{
			name:     "a limit of two reboots at the second restart",
			set:      func() WatchdogSettings { s := hour; s.RestartsBeforeReboot = 2; return s }(),
			restarts: 2, step: time.Minute, want: true,
		},
		{
			name:     "a limit of two holds at the first restart",
			set:      func() WatchdogSettings { s := hour; s.RestartsBeforeReboot = 2; return s }(),
			restarts: 1, step: time.Minute,
		},
		{
			name:     "a limit of zero never reboots",
			set:      func() WatchdogSettings { s := hour; s.RestartsBeforeReboot = 0; return s }(),
			restarts: 10, step: time.Minute,
		},
		{
			name:     "a ladder that is off never reboots",
			set:      WatchdogSettings{RestartWindow: time.Hour, RestartsBeforeReboot: 2},
			restarts: 10, step: time.Minute,
		},
		{
			name: "a short window forgets the restarts before it",
			set: WatchdogSettings{
				Enabled: true, RestartWindow: 5 * time.Minute, RestartsBeforeReboot: 2,
			},
			restarts: 10, step: 6 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClock()
			var w watchdog
			reboot := false
			for i := 0; i < tt.restarts; i++ {
				c.add(tt.step)
				if _, ask := w.restarted(c.now(), tt.set); ask {
					reboot = true
				}
			}
			if reboot != tt.want {
				t.Fatalf("the reboot is %v, want %v", reboot, tt.want)
			}
		})
	}
}

// A frame stall is the other step of the first rung, so a ladder that is off must
// not act on it either (D45, D30).
func TestWatchdogFrameStallHoldsWhileTheLadderIsOff(t *testing.T) {
	c := newClock()
	off := WatchdogSettings{HeartbeatTimeout: 30 * time.Second}
	var w watchdog
	w.started(c.now())
	for i := 0; i < frameStalls+2; i++ {
		c.add(5 * time.Second)
		if reason := w.heartbeat(c.now(), 1000, off); reason != "" {
			t.Fatalf("a ladder that is off asked for a restart: %s", reason)
		}
	}
	// The counter runs all the time, so the first heartbeat after a person switches
	// the ladder on again sees the stall that is already there.
	c.add(5 * time.Second)
	if reason := w.heartbeat(c.now(), 1000, DefaultWatchdog()); reason == "" {
		t.Fatal("the ladder did not act after it was switched on again")
	}
}

func TestDisplayConnected(t *testing.T) {
	// A connector that reports "connected".
	root := t.TempDir()
	writeStatus(t, root, "card0-HDMI-A-1", "disconnected")
	writeStatus(t, root, "card0-HDMI-A-2", "connected\n")
	if !DisplayConnected(root) {
		t.Error("a connected display was not found")
	}

	// Nothing plugged in.
	empty := t.TempDir()
	writeStatus(t, empty, "card0-HDMI-A-1", "disconnected")
	writeStatus(t, empty, "card0-DP-1", "unknown")
	if DisplayConnected(empty) {
		t.Error("a device with nothing plugged in reports a display")
	}

	// A machine with no DRM directory must never wait for ever.
	if !DisplayConnected(filepath.Join(t.TempDir(), "missing")) {
		t.Error("a missing DRM directory must report a display")
	}
	// A DRM directory with no connector is a machine with no graphics card.
	if !DisplayConnected(t.TempDir()) {
		t.Error("a DRM directory with no connector must report a display")
	}
}

func writeStatus(t *testing.T, root, name, status string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A page reload, a return from a URL item and a browser restart all start the
// frame counter at 0 again. A lower value is a new baseline, never a stall
// (ARCHITECTURE 7a).
func TestWatchdogFrameCounterResetIsNotAStall(t *testing.T) {
	c := newClock()
	var w watchdog
	w.started(c.now())

	c.add(5 * time.Second)
	w.heartbeat(c.now(), 18211, DefaultWatchdog())

	// The page loaded again: the counter starts from zero.
	for _, frames := range []int64{0, 0, 1, 2, 3} {
		c.add(5 * time.Second)
		if reason := w.heartbeat(c.now(), frames, DefaultWatchdog()); reason != "" && frames > 0 {
			t.Fatalf("a counter that started again asked for a restart at %d: %s", frames, reason)
		}
	}
	// Only the same value, three times in a row, is a stall.
	c.add(5 * time.Second)
	w.heartbeat(c.now(), 3, DefaultWatchdog())
	c.add(5 * time.Second)
	w.heartbeat(c.now(), 3, DefaultWatchdog())
	c.add(5 * time.Second)
	if reason := w.heartbeat(c.now(), 3, DefaultWatchdog()); reason == "" {
		t.Fatal("three equal counters did not ask for a restart")
	}
}
