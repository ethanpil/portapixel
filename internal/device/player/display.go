package player

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultDRMRoot is where the kernel reports the graphics cards and the display
// connectors.
const DefaultDRMRoot = "/sys/class/drm"

// The size of the fallback picture when the kernel names no mode.
const (
	defaultWidth  = 1920
	defaultHeight = 1080
)

// The limits of the fallback picture. mpv scales the picture to the screen and
// keeps its shape, so the picture need not have the size of the screen. A 4K
// picture takes 33 MB to draw; a picture of 8K or more costs memory that a small
// device does not have. fallback.Render refuses a picture under 320x240.
const (
	renderLong  = 3840 // the longest side
	renderShort = 2160 // the shortest side
	renderMinW  = 320
	renderMinH  = 240
)

// glDrivers are the kernel drivers that have a hardware OpenGL driver in Mesa.
// mpv uses vo=gpu on them. Every other driver gets vo=drm: virtio_gpu, bochs and
// simpledrm have no GPU, and vo=gpu there is llvmpipe, which dropped 40 to 65 %
// of the frames on the Pi Zero 2 W proxy (scratchpad mpv spike). Some drivers
// name themselves with "-drm" at the end, for example vc4-drm.
var glDrivers = []string{"vc4", "v3d", "i915", "xe", "amdgpu", "radeon", "nouveau"}

// ResolveOutput gives the video output for display.video_output: "gpu" and
// "drm" stay as they are, and "auto" looks at the drivers of the graphics cards.
func ResolveOutput(setting, drmRoot string) string {
	switch setting {
	case OutputGPU, OutputDRM:
		return setting
	}
	if drmRoot == "" {
		drmRoot = DefaultDRMRoot
	}
	cards, _ := filepath.Glob(filepath.Join(drmRoot, "card*", "device", "uevent"))
	for _, uevent := range cards {
		driver := ueventDriver(uevent)
		if slices.Contains(glDrivers, strings.TrimSuffix(driver, "-drm")) {
			return OutputGPU
		}
	}
	return OutputDRM
}

// ueventDriver gives the DRIVER= line of a uevent file, or "". The file is easier
// to read than the driver link, and a test can write it on any system.
func ueventDriver(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if value, ok := strings.CutPrefix(sc.Text(), "DRIVER="); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// DisplayConnected reports if any display connector has something plugged in.
//
// It reads /sys/class/drm/*/status. The answer of the kernel is "connected",
// "disconnected" or "unknown".
//
// A directory that we cannot read gives true. That is deliberate: a development
// machine has no DRM directory, and a wrong "no display" answer would hold the
// player for ever. The wait state must start only when the kernel says clearly
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

// displaySize gives the size of the picture that mpv shows, in the orientation of
// the content. The fallback screen is drawn at this size.
//
// display.video_mode wins, because mpv uses it. Else the first mode of the first
// connected connector, which is the mode that the display prefers and the mode
// that mpv takes. Else 1920x1080. A rotation of 90 or 270 degrees turns the
// screen, so the picture is tall. renderSize then keeps the size in limits.
func displaySize(drmRoot, videoMode string, rotation int) (int, int) {
	w, h, ok := parseMode(videoMode)
	if !ok {
		w, h, ok = connectorMode(drmRoot)
	}
	if !ok {
		w, h = defaultWidth, defaultHeight
	}
	if rotation == 90 || rotation == 270 {
		w, h = h, w
	}
	return renderSize(w, h)
}

// renderSize keeps the shape of w x h and scales it into the limits of the
// fallback picture. A very small or a very narrow screen, for example a bar
// display of 1920x158, gets a larger picture: with no fallback picture the
// health marker of an update never comes. A very large mode gets a smaller
// picture. The upper limit is the last step, so it always applies: it protects
// the memory of the device.
func renderSize(w, h int) (int, int) {
	fw, fh := float64(w), float64(h)
	if up := max(renderMinW/fw, renderMinH/fh); up > 1 {
		fw, fh = fw*up, fh*up
	}
	if down := min(renderLong/max(fw, fh), renderShort/min(fw, fh)); down < 1 {
		fw, fh = fw*down, fh*down
	}
	return int(math.Round(fw)), int(math.Round(fh))
}

// connectorMode reads the first mode of the first connected connector.
func connectorMode(drmRoot string) (int, int, bool) {
	if drmRoot == "" {
		drmRoot = DefaultDRMRoot
	}
	entries, err := os.ReadDir(drmRoot)
	if err != nil {
		return 0, 0, false
	}
	for _, e := range entries {
		dir := filepath.Join(drmRoot, e.Name())
		status, err := os.ReadFile(filepath.Join(dir, "status"))
		if err != nil || strings.TrimSpace(string(status)) != "connected" {
			continue
		}
		modes, err := os.ReadFile(filepath.Join(dir, "modes"))
		if err != nil {
			continue
		}
		first, _, _ := strings.Cut(string(modes), "\n")
		if w, h, ok := parseMode(first); ok {
			return w, h, true
		}
	}
	return 0, 0, false
}

// parseMode reads "1920x1080", "1920x1080@60" or "1920x1080i".
func parseMode(text string) (int, int, bool) {
	var w, h int
	if _, err := fmt.Sscanf(strings.TrimSpace(text), "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}
