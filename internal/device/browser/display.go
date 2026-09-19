package browser

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultDRMRoot is where the kernel reports the display connectors.
const DefaultDRMRoot = "/sys/class/drm"

// DisplayConnected reports if any display connector has something plugged in.
//
// It reads /sys/class/drm/*/status. The answer of the kernel is "connected",
// "disconnected" or "unknown".
//
// A directory that we cannot read gives true. That is deliberate: a development
// machine has no DRM directory, and a wrong "no display" answer would hold the
// browser for ever. The wait state must start only when the kernel says clearly
// that nothing is plugged in (D44).
func DisplayConnected(drmRoot string) bool {
	if drmRoot == "" {
		drmRoot = DefaultDRMRoot
	}
	entries, err := os.ReadDir(drmRoot)
	if err != nil {
		return true
	}
	sawStatus := false
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(drmRoot, e.Name(), "status"))
		if err != nil {
			continue
		}
		sawStatus = true
		if strings.TrimSpace(string(data)) == "connected" {
			return true
		}
	}
	// A DRM directory with no connector at all is a machine with no graphics
	// card. There is nothing to wait for, so do not wait.
	return !sawStatus
}
