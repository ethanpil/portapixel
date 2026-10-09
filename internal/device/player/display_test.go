package player

import (
	"path/filepath"
	"testing"
)

func TestResolveOutput(t *testing.T) {
	tests := []struct {
		name    string
		drivers []string
		setting string
		want    string
	}{
		{"Pi with the KMS overlay", []string{"vc4-drm"}, OutputAuto, OutputGPU},
		{"Pi 4: render card and display card", []string{"v3d", "vc4-drm"}, OutputAuto, OutputGPU},
		{"Intel", []string{"i915"}, OutputAuto, OutputGPU},
		{"AMD", []string{"amdgpu"}, OutputAuto, OutputGPU},
		{"QEMU virtio", []string{"virtio_gpu"}, OutputAuto, OutputDRM},
		{"QEMU std VGA", []string{"bochs-drm"}, OutputAuto, OutputDRM},
		{"firmware framebuffer", []string{"simple-framebuffer"}, OutputAuto, OutputDRM},
		{"no card", nil, OutputAuto, OutputDRM},
		{"forced gpu", []string{"virtio_gpu"}, OutputGPU, OutputGPU},
		{"forced drm", []string{"i915"}, OutputDRM, OutputDRM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for i, d := range tt.drivers {
				writeFile(t, filepath.Join(root, "card"+string(rune('0'+i)), "device", "uevent"),
					"MAJOR=226\nDRIVER="+d+"\nOF_NAME=gpu\n")
			}
			if got := ResolveOutput(tt.setting, root); got != tt.want {
				t.Errorf("ResolveOutput = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDisplayConnected(t *testing.T) {
	root := t.TempDir()
	if !DisplayConnected(root) {
		t.Error("a machine with no connector must not wait")
	}
	if !DisplayConnected(filepath.Join(root, "missing")) {
		t.Error("a machine with no DRM directory must not wait")
	}
	writeFile(t, filepath.Join(root, "card0-HDMI-A-1", "status"), "disconnected\n")
	writeFile(t, filepath.Join(root, "card0-HDMI-A-2", "status"), "unknown\n")
	if DisplayConnected(root) {
		t.Error("no connector is connected, and the answer is true")
	}
	writeFile(t, filepath.Join(root, "card0-HDMI-A-2", "status"), "connected\n")
	if !DisplayConnected(root) {
		t.Error("a connected connector is not seen")
	}
}

func TestDisplaySize(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "card0-HDMI-A-1", "status"), "disconnected\n")
	writeFile(t, filepath.Join(root, "card0-HDMI-A-1", "modes"), "3840x2160\n")
	writeFile(t, filepath.Join(root, "card0-HDMI-A-2", "status"), "connected\n")
	writeFile(t, filepath.Join(root, "card0-HDMI-A-2", "modes"), "1280x720\n1920x1080i\n")

	tests := []struct {
		mode     string
		rotation int
		w, h     int
	}{
		{"", 0, 1280, 720},              // the first mode of the connected connector
		{"1920x1080@60", 0, 1920, 1080}, // video_mode wins
		{"", 90, 720, 1280},             // a turned screen is tall
		{"1024x768", 270, 768, 1024},    // both
		{"not a mode", 180, 1280, 720},  // a bad value falls back to the connector
	}
	for _, tt := range tests {
		if w, h := displaySize(root, tt.mode, tt.rotation); w != tt.w || h != tt.h {
			t.Errorf("displaySize(%q, %d) = %dx%d, want %dx%d", tt.mode, tt.rotation, w, h, tt.w, tt.h)
		}
	}
	if w, h := displaySize(t.TempDir(), "", 0); w != defaultWidth || h != defaultHeight {
		t.Errorf("with no connector the size is %dx%d, want %dx%d", w, h, defaultWidth, defaultHeight)
	}
	// The fallback picture is drawn at this size, so a large mode must not cost
	// gigabytes. A mode that config.Validate refuses still comes here from an
	// older file or a test, so the limit is here too.
	if w, h := displaySize(root, "99999x99999", 0); w != 2160 || h != 2160 {
		t.Errorf("a huge mode gives %dx%d, want 2160x2160", w, h)
	}
}

func TestRenderSize(t *testing.T) {
	tests := []struct{ w, h, wantW, wantH int }{
		{1920, 1080, 1920, 1080},
		{3840, 2160, 3840, 2160},
		{2160, 3840, 2160, 3840}, // a turned 4K screen
		{7680, 4320, 3840, 2160}, // 8K: the same shape at 4K
		{4320, 7680, 2160, 3840},
		{1920, 158, 2916, 240}, // a bar display: fallback.Render needs 240 lines
		{200, 100, 480, 240},
		{8192, 1, 3840, 0}, // no shape fits both limits; the memory limit wins
	}
	for _, tt := range tests {
		if w, h := renderSize(tt.w, tt.h); w != tt.wantW || h != tt.wantH {
			t.Errorf("renderSize(%d, %d) = %dx%d, want %dx%d", tt.w, tt.h, w, h, tt.wantW, tt.wantH)
		}
	}
}
