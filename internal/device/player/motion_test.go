package player

import (
	"os"
	"path/filepath"
	"testing"
)

// auto is on for x86_64 and for a Raspberry Pi 5 or Compute Module 5, off for
// every other board. The model comes from a fake device tree file.
func TestResolveMotion(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		setting, arch, model string
		want                 bool
	}{
		{MotionAuto, "amd64", "", true},
		{MotionAuto, "arm64", "Raspberry Pi 5 Model B Rev 1.0\x00", true},
		{MotionAuto, "arm64", "Raspberry Pi Compute Module 5 Rev 1.0\x00", true},
		{MotionAuto, "arm64", "Raspberry Pi 4 Model B Rev 1.5\x00", false},
		{MotionAuto, "arm64", "Raspberry Pi Zero 2 W Rev 1.0\x00", false},
		{MotionAuto, "arm64", "Raspberry Pi Compute Module 4 Rev 1.0\x00", false},
		{MotionAuto, "arm64", "", false},
		{MotionAuto, "arm", "Raspberry Pi 3 Model B Plus Rev 1.3\x00", false},
		{MotionOn, "arm64", "Raspberry Pi Zero 2 W Rev 1.0\x00", true},
		{MotionOff, "amd64", "", false},
	}
	for i, tt := range tests {
		path := filepath.Join(dir, "none")
		if tt.model != "" {
			path = filepath.Join(dir, string(rune('a'+i)))
			if err := os.WriteFile(path, []byte(tt.model), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := ResolveMotion(tt.setting, tt.arch, BoardModel(path)); got != tt.want {
			t.Errorf("ResolveMotion(%q, %q, %q) = %v, want %v", tt.setting, tt.arch, tt.model, got, tt.want)
		}
	}
}

func TestTooSlow(t *testing.T) {
	tests := []struct {
		m    moved
		slow bool
	}{
		{moved{Count: 1, Dropped: 0, Frames: 25}, false},
		{moved{Count: 1, Dropped: 6, Frames: 25}, false},
		{moved{Count: 1, Dropped: 7, Frames: 25}, true},
		{moved{Count: 1, Dropped: 22, Frames: 30}, true},
		{moved{Count: 1, Failed: true}, true},
		{moved{Count: 1}, false},
	}
	for _, tt := range tests {
		if got := tooSlow(tt.m) != ""; got != tt.slow {
			t.Errorf("tooSlow(%+v) = %v, want %v", tt.m, got, tt.slow)
		}
	}
}

// motion gives the value of user-data/pptr/motion in the fake mpv.
func (h *harness) motionValue() string {
	h.t.Helper()
	v, _ := h.dump().Props["user-data/pptr/motion"].(string)
	return v
}

// The daemon tells the script the value of playback.motion before the list
// starts, and again at a playlist change.
func TestMotionFollowsTheSetting(t *testing.T) {
	h := newHarness(t, videos(), nil)
	h.waitPlaying(0)
	if got := h.motionValue(); got != "yes" {
		t.Fatalf("motion = %q with the setting on, want yes", got)
	}
	h.mu.Lock()
	h.motion = MotionOff
	h.mu.Unlock()
	h.sup.PlaylistChanged()
	waitFor(t, "motion no", func() bool { return h.motionValue() == "no" })
}

// A moving crossfade that drops too many frames switches the moving crossfade
// off for the rest of the boot, with one ops log line. A playlist change and a
// new mpv keep it off.
func TestTheGuardSwitchesMotionOff(t *testing.T) {
	h := newHarness(t, videos(), nil)
	h.waitPlaying(0)

	report := func(count, dropped, frames int, failed bool) {
		h.ctl("set_property", "user-data/pptr/moved",
			map[string]any{"count": count, "dropped": dropped, "frames": frames, "failed": failed})
	}
	report(1, 2, 25, false)
	h.settle()
	if got := h.motionValue(); got != "yes" || h.countEvent("player.motion.off") != 0 {
		t.Fatalf("a good crossfade gave motion %q and the log:\n%s", got, h.events())
	}

	report(2, 20, 25, false)
	waitFor(t, "motion no", func() bool { return h.motionValue() == "no" })
	report(3, 0, 0, true)
	h.settle()
	if n := h.countEvent("player.motion.off"); n != 1 || !h.eventWith("player.motion.off", "dropped 20 of 25") {
		t.Fatalf("the guard wrote %d lines:\n%s", n, h.events())
	}

	h.sup.PlaylistChanged()
	h.settle()
	if got := h.motionValue(); got != "no" {
		t.Errorf("a playlist change gave motion %q", got)
	}
	first := h.sup.proc.pid()
	if err := h.sup.Restart("the admin asked"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the restart", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	h.waitPlaying(0)
	if got := h.motionValue(); got != "no" {
		t.Errorf("the new mpv got motion %q, want no until the daemon starts again", got)
	}
}
