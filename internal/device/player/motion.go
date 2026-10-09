package player

import (
	"encoding/json"
	"fmt"

	"github.com/ethanpil/portapixel/internal/config"
)

// ResolveMotion reports if the moving crossfade is on for a value of
// playback.motion. arch is runtime.GOARCH, and model is the board name from
// BoardModel. "auto" is on for an x86_64 device and for a Raspberry Pi 5 or
// Compute Module 5, and off for every other board: a Pi before the 5 cannot
// decode and mix two videos in real time.
func ResolveMotion(setting, arch, model string) bool {
	switch setting {
	case config.MotionOn:
		return true
	case config.MotionOff:
		return false
	}
	return arch == "amd64" || isPi5(model)
}

// moved is the report of one moving crossfade, from user-data/pptr/moved
// (transitions.lua). Count makes each report a new value, so mpv sends an
// event for each one.
type moved struct {
	Count   int  `json:"count"`
	Dropped int  `json:"dropped"`
	Frames  int  `json:"frames"`
	Failed  bool `json:"failed"`
}

// parseMoved reads a report. An empty or null value gives false.
func parseMoved(data json.RawMessage) (moved, bool) {
	var m moved
	if json.Unmarshal(data, &m) != nil || m.Count == 0 {
		return moved{}, false
	}
	return m, true
}

// tooSlow is the guard. A moving crossfade that failed, or that dropped more
// than a quarter of its frames, gives the reason to switch the moving
// crossfade off; a good one gives "". On the pp-zero lab VM a crossfade of 1 s
// dropped no frame at full speed, and 22 of 30 frames with the CPU limit of the
// Pi Zero 2 W proxy.
func tooSlow(m moved) string {
	switch {
	case m.Failed:
		return "a moving crossfade failed"
	case m.Frames > 0 && m.Dropped*4 > m.Frames:
		return fmt.Sprintf("a moving crossfade dropped %d of %d frames", m.Dropped, m.Frames)
	}
	return ""
}
