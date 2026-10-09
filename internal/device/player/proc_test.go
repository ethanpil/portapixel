package player

import "testing"

// The exit reason is the last line of mpv that says something. The lines of the
// hardware decoder probe and of the VT switcher come at each video and at each
// start, so they never are the reason.
func TestLastLineSkipsNoise(t *testing.T) {
	tests := []struct {
		name, out, want string
	}{
		{"a real fault after the probe", "[vaapi] libva: /usr/lib/dri/virtio_gpu_drv_video.so init failed\n" +
			"[vo/drm/drm] Failed to open card0: Device or resource busy\n" +
			"[ffmpeg] VDPAU: Cannot open the X11 display .\n" +
			"[ffmpeg] Vulkan: Instance creation failure: VK_ERROR_INCOMPATIBLE_DRIVER\n",
			"[vo/drm/drm] Failed to open card0: Device or resource busy"},
		{"only noise", "[vo/drm/drm] Can't open TTY for VT control: No such device or address\n" +
			"[vo/drm/drm] Failed to set up VT switcher. Terminal switching will be unavailable.\n" +
			"AVOption 'num_capture_buffers' not found.\n", ""},
		{"windows line ends", "Error opening input files.\r\n\r\n", "Error opening input files."},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		if got := lastLine(tt.out); got != tt.want {
			t.Errorf("%s: lastLine = %q, want %q", tt.name, got, tt.want)
		}
	}
}
