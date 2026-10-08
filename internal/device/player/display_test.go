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
}
