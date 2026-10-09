package player

import (
	"strings"
	"testing"
)

// Output that holds only noise gives "mpv ended" with no output part, and output
// with a real line gives that line.
func TestExitReasonHasNoEmptyOutputPart(t *testing.T) {
	tests := []struct {
		name, out, want string
	}{
		{"no output", "", "mpv ended"},
		{"only noise", "[vaapi] libva: init failed\n[ffmpeg] VDPAU: Cannot open the X11 display .\n", "mpv ended"},
		{"a real line", "[vaapi] libva: init failed\nError opening input files.\n",
			"mpv ended; output: Error opening input files."},
	}
	for _, tt := range tests {
		sink := &outputSink{tail: &tailBuffer{max: tailBytes}}
		sink.Write([]byte(tt.out))
		l := &launcher{out: sink}
		got := l.exitReason()
		if got != tt.want {
			t.Errorf("%s: exitReason = %q, want %q", tt.name, got, tt.want)
		}
		if strings.HasSuffix(got, "output: ") {
			t.Errorf("%s: exitReason ends with an empty output part: %q", tt.name, got)
		}
	}
}

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
