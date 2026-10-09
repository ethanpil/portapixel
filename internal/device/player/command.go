package player

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// DisableCommand is the override value that stops the player. A development
// machine with no mpv uses it, and the tests of other packages use it.
const DisableCommand = "none"

// The files of the player in the run directory. The run directory is a tmpfs,
// so nothing here touches the flash (D2).
const (
	// SocketDirName is the one directory that the kiosk account may write. mpv
	// makes its IPC socket there, and it is HOME of mpv.
	SocketDirName = "player"
	SocketName    = "mpv.sock"
	// ScriptName is the transition script. The daemon writes it at each start.
	ScriptName = "transitions.lua"
	// FallbackName is the picture of the fallback screen (D18).
	FallbackName = "fallback.png"
	// LogName takes the output of mpv, with a limit (see proc.go).
	LogName = "player.log"
)

// ModelPath is where the kernel names the board. A Raspberry Pi has the file
// (it comes from the device tree); a PC does not.
const ModelPath = "/proc/device-tree/model"

// The words of display.video_output. Output gives "gpu" or "drm"; "auto" is only
// a setting.
const (
	OutputAuto = "auto"
	OutputGPU  = "gpu"
	OutputDRM  = "drm"
)

// CommandConfig says how to start mpv. It is the only place in the program that
// knows the command line (ARCHITECTURE section 7).
type CommandConfig struct {
	// Override replaces the program. It comes from --player-cmd or from
	// PORTAPIXEL_PLAYER_CMD. The value "none" stops the player.
	Override string
	// KioskUser is the unprivileged account that runs mpv (D43). An empty name
	// runs mpv as the daemon's own user, which is for development only.
	KioskUser string
	// RunDir is the run directory of the daemon, a tmpfs.
	RunDir string
}

// Launch holds the values that a start of mpv takes from the configuration and
// from the hardware.
type Launch struct {
	// Output is "gpu" or "drm", never "auto".
	Output    string
	Rotation  int
	VideoMode string
	// Model is the board name from ModelPath, or "" (a PC).
	Model string
}

// BoardModel gives the board name in the file at path, or "" when there is no
// such file. The device tree ends the name with a NUL byte.
func BoardModel(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimRight(string(data), "\x00"))
}

// IsRaspberryPi reports if a board name from BoardModel is a Raspberry Pi.
func IsRaspberryPi(model string) bool { return strings.HasPrefix(model, "Raspberry Pi") }

// isPi5 reports a Raspberry Pi 5 or Compute Module 5. It has no H.264
// decoder, and it is fast enough for the moving crossfade.
func isPi5(model string) bool {
	return strings.HasPrefix(model, "Raspberry Pi 5") || strings.HasPrefix(model, "Raspberry Pi Compute Module 5")
}

// Hwdec gives the value of --hwdec for a board. See Args.
func Hwdec(model string) string {
	if IsRaspberryPi(model) {
		return "v4l2m2m-copy"
	}
	return "auto-safe"
}

// DecoderOptions gives the decoder options (vd-lavc-o) of a video item on a
// board, or "". The H.264 decoder of a Pi Zero 2 W, 3 and 4 (h264_v4l2m2m)
// takes its capture buffers from the CMA area: 20 by default, about 3.1 MB each
// at 1080p, and its 16 input buffers take about 1.5 MB each. A 512 MB Pi has a
// CMA area of 128 MB (dtparam=cma-128), and the GPU needs it too. 8 capture
// buffers save about 37 MB at 1080p. A Pi 5 has no such decoder.
//
// The option goes on the video items only, not on the command line: mpv gives
// vd-lavc-o to each decoder, and a decoder that does not know the option (an
// image, or a video that plays in software) writes an error line.
func DecoderOptions(model string) string {
	if IsRaspberryPi(model) && !isPi5(model) {
		return "num_capture_buffers=8"
	}
	return ""
}

// Disabled reports if the player is switched off.
func (c CommandConfig) Disabled() bool {
	return strings.TrimSpace(c.Override) == DisableCommand
}

// SocketPath gives the IPC socket of mpv.
func (c CommandConfig) SocketPath() string {
	return filepath.Join(c.RunDir, SocketDirName, SocketName)
}

// ScriptPath gives the transition script.
func (c CommandConfig) ScriptPath() string { return filepath.Join(c.RunDir, ScriptName) }

// FallbackPath gives the picture of the fallback screen.
func (c CommandConfig) FallbackPath() string { return filepath.Join(c.RunDir, FallbackName) }

// LogPath gives the output file of mpv.
func (c CommandConfig) LogPath() string { return filepath.Join(c.RunDir, LogName) }

// Args gives the arguments of mpv, in this order:
//
//	--no-config                        no mpv.conf, no input.conf, no resume files
//	--profile=fast                     the built-in profile for slow GPUs. Without
//	                                   it vo=gpu did not play in real time on the
//	                                   Pi Zero 2 W proxy (scratchpad mpv spike).
//	--idle --force-window --keep-open  mpv stays up and keeps a picture with no
//	                                   file, and between two files
//	--loop-playlist=inf                mpv loops the playlist by itself, so the
//	                                   picture goes on when the daemon is busy
//	--prefetch-playlist=yes            open the next file before the item ends
//	--input-ipc-server                 the control socket of the daemon
//	--script                           the transitions (transitions.lua)
//	--input-default-bindings=no        a keyboard that a person plugs in does
//	                                   nothing
//	--osc=no --ytdl=no --load-*=no     the built-in scripts take memory and
//	                                   start time, and a screen with no person at
//	                                   it uses none of them
//	--hwdec=auto-safe | v4l2m2m-copy   see below
//	--ao=alsa                          D11: direct ALSA. /etc/asound.conf names
//	                                   the card (internal/device/audio).
//	--msg-level=all=warn               player.log holds the faults only
//	--vo=gpu --gpu-context=drm | --vo=drm
//	--video-rotate, --drm-mode         display.rotation and display.video_mode
//
// About --hwdec. auto-safe (the same as auto in mpv 0.40) tries only the
// decoders in the whitelist of video/decode/vd_lavc.c: d3d11va, dxva2-copy,
// nvdec, vaapi, vulkan, vdpau-copy, drm, drm-copy, mediacodec-copy and
// videotoolbox, with their -copy forms. v4l2m2m is NOT in the list, and the H.264
// decoder of a Raspberry Pi (Zero 2 W, 3, 4) is a V4L2 memory-to-memory device.
// So on a Raspberry Pi (IsRaspberryPi) the value is v4l2m2m-copy: the decoder
// h264_v4l2m2m of FFmpeg, which gives the frames back in memory. That form
// works with vo=drm, vo=gpu and the filters of the moving crossfade. A Pi 5 has
// no H.264 decoder, and a file that the decoder does not take plays in software:
// mpv falls back by itself when the hardware decoder fails. Every other board
// uses auto-safe (vaapi on an Intel or AMD PC).
func (c CommandConfig) Args(l Launch) []string {
	args := []string{
		"--no-config",
		"--profile=fast",
		"--idle=yes",
		"--force-window=yes",
		"--keep-open=yes",
		"--loop-playlist=inf",
		"--prefetch-playlist=yes",
		"--input-ipc-server=" + c.SocketPath(),
		"--script=" + c.ScriptPath(),
		"--input-default-bindings=no",
		"--osc=no",
		"--ytdl=no",
		"--load-stats-overlay=no",
		"--load-console=no",
		"--load-auto-profiles=no",
		"--load-select=no",
		"--load-commands=no",
		"--load-positioning=no",
		"--hwdec=" + Hwdec(l.Model),
		"--ao=alsa",
		"--msg-level=all=warn",
	}
	if l.Output == OutputGPU {
		args = append(args, "--vo=gpu", "--gpu-context=drm")
	} else {
		args = append(args, "--vo=drm")
	}
	if l.Rotation != 0 {
		args = append(args, "--video-rotate="+strconv.Itoa(l.Rotation))
	}
	if l.VideoMode != "" {
		args = append(args, "--drm-mode="+l.VideoMode)
	}
	return args
}

// Build makes the command that starts mpv.
//
// An override replaces the program. Its own arguments come AFTER the arguments
// of the daemon, and mpv uses the last value of an option, so a developer can
// change the output: --player-cmd "mpv --vo=gpu --gpu-context=x11egl".
func (c CommandConfig) Build(l Launch) (*exec.Cmd, error) {
	if c.Disabled() {
		return nil, errors.New("the player is switched off")
	}
	name, args := "mpv", c.Args(l)
	override := strings.TrimSpace(c.Override) != ""
	if override {
		words := splitArgs(c.Override)
		if len(words) == 0 || words[0] == "" {
			return nil, fmt.Errorf("the player command %q holds no program name", c.Override)
		}
		name, args = words[0], append(args, words[1:]...)
	}
	cmd := exec.Command(name, args...)
	if !override {
		// A minimal environment. The kiosk account has no login shell, so it gets
		// no profile and no PATH of its own. HOME is on the tmpfs: mpv must never
		// write to the flash. An override is a development command, which needs
		// the environment of the desktop (DISPLAY or WAYLAND_DISPLAY).
		cmd.Env = []string{
			"PATH=/usr/local/bin:/usr/bin:/bin",
			"HOME=" + filepath.Join(c.RunDir, SocketDirName),
		}
	}
	if err := applyCredential(cmd, c.KioskUser); err != nil {
		return nil, err
	}
	return cmd, nil
}

// Prepare makes the run directory ready for a start: the socket directory of
// the kiosk account and the transition script. It removes a socket that a
// player before this one left.
//
// The run directory itself belongs to root. The kiosk account gets one
// directory of its own and nothing more, so mpv cannot change the script or
// the fallback picture.
func (c CommandConfig) Prepare() error {
	dir := filepath.Join(c.RunDir, SocketDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	if err := ownBy(dir, c.KioskUser); err != nil {
		return err
	}
	if err := os.Remove(c.SocketPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the old socket: %w", err)
	}
	if err := fsutil.WriteFileAtomic(c.ScriptPath(), transitionScript, 0o644); err != nil {
		return fmt.Errorf("write the transition script: %w", err)
	}
	return nil
}

// splitArgs cuts a command line into arguments. It gives space and the two
// quotation marks their usual meaning, so a path with a space in it works on a
// Windows development machine. It is not a shell: there is no expansion, no
// pipe and no redirection, and that is deliberate.
func splitArgs(line string) []string {
	var (
		out   []string
		cur   strings.Builder
		quote rune
		have  bool
	)
	flush := func() {
		if have {
			out = append(out, cur.String())
			cur.Reset()
			have = false
		}
	}
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			have = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	flush()
	return out
}
