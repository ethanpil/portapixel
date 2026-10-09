package player

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// The exit reason is the exit status and the last different lines of mpv.
// Output with no text gives no output part.
func TestExitReason(t *testing.T) {
	tests := []struct {
		name, out string
		err       error
		want      string
	}{
		{name: "no output", want: "mpv ended"},
		{name: "only empty lines", out: "\n \r\n", want: "mpv ended"},
		{name: "the exit status", err: errors.New("exit status 7"), out: "Error opening input files.\n",
			want: "mpv ended: exit status 7; output: Error opening input files."},
		// The real fault comes before the probe lines of the Pi decoder, which come
		// at each video. A list of lines to skip held the lines of the lab VM only,
		// so the reason was a decoder line.
		{name: "a fault before the probe of the Pi decoder", err: errors.New("signal: aborted"),
			out: "[ffmpeg/video] h264_v4l2m2m: Could not find a valid device\n" +
				"[vd] Could not open codec.\n" +
				"[vo/drm/drm] Failed to open card0: Device or resource busy\n" +
				"[ffmpeg/video] h264_v4l2m2m: Could not find a valid device\n" +
				"[vd] Could not open codec.\n",
			want: "mpv ended: signal: aborted; output: [vo/drm/drm] Failed to open card0: Device or resource busy | " +
				"[ffmpeg/video] h264_v4l2m2m: Could not find a valid device | [vd] Could not open codec."},
		{name: "windows line ends", out: "Error opening input files.\r\n\r\n",
			want: "mpv ended; output: Error opening input files."},
	}
	for _, tt := range tests {
		sink := &outputSink{tail: &tailBuffer{max: tailBytes}}
		sink.Write([]byte(tt.out))
		l := &launcher{out: sink, err: tt.err}
		if got := l.exitReason(); got != tt.want {
			t.Errorf("%s: exitReason = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// A long line is cut on a rune boundary. A cut in a character gave the ops log
// a line that is not UTF-8, and the log page showed U+FFFD. The tail buffer can
// also start in a character.
func TestExitReasonIsValidUTF8(t *testing.T) {
	long := "[file] Cannot open file '/media/lobby/x" + strings.Repeat("é", 200) + ".mp4'"
	for _, out := range []string{long + "\n", "\xa9 the start of the tail\n"} {
		sink := &outputSink{tail: &tailBuffer{max: tailBytes}}
		sink.Write([]byte(out))
		l := &launcher{out: sink}
		got := l.exitReason()
		if !utf8.ValidString(got) {
			t.Errorf("exitReason is not valid UTF-8: %q", got)
		}
		if len(got) > 160 { // the ops log keeps 512 bytes of a field
			t.Errorf("exitReason has %d bytes: %q", len(got), got)
		}
	}
}
