package player

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/opslog"
)

// stopGrace is the time that mpv gets to end after a polite signal. After it,
// the signal is not polite any more. A stopped (SIGSTOP) process does not take
// the polite signal, so this is also the longest wait for a frozen mpv.
const stopGrace = 5 * time.Second

// tailBytes is how much of the output of mpv we keep in memory. The end of the
// output goes in the ops log when the process fails: that is the part that says
// why.
const tailBytes = 2048

// logCap is the largest player.log that we write. The file is on the tmpfs of
// the run directory, which is RAM. Output after the cap goes nowhere.
const logCap = 1 << 20

// logNotice is the last line of a log that reached the cap. Without it, a person
// cannot tell a full file from a player that stopped writing.
const logNotice = "\n--- player.log is full at 1 MiB; more output goes nowhere ---\n"

// launcher owns the mpv process: one place that starts it and one place that
// stops it.
type launcher struct {
	cfg CommandConfig
	log *opslog.Log

	mu     sync.Mutex
	cmd    *exec.Cmd
	out    *outputSink
	exited chan struct{}
	err    error
}

func newLauncher(cfg CommandConfig, log *opslog.Log) *launcher {
	return &launcher{cfg: cfg, log: log}
}

// start stops any mpv that runs and starts a new one.
func (l *launcher) start(launch Launch) error {
	l.stop()

	if err := l.cfg.Prepare(); err != nil {
		return err
	}
	cmd, err := l.cfg.Build(launch)
	if err != nil {
		return err
	}
	out := l.newSink()
	// One value for both streams. os/exec then gives the child one pipe, so the
	// two streams stay in the order in which mpv wrote them.
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		out.close()
		return fmt.Errorf("start the player: %w", err)
	}

	exited := make(chan struct{})
	l.mu.Lock()
	l.cmd, l.out, l.exited, l.err = cmd, out, exited, nil
	l.mu.Unlock()

	go func() {
		err := cmd.Wait()
		out.close()
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
		close(exited)
	}()
	return nil
}

// newSink makes the destination of the output of mpv: the memory tail for the
// ops log, and player.log for a person who must see the whole run.
//
// A file that cannot be opened is not a reason to leave the player off. The
// tail still works, so the ops log still gets the last line at each exit.
func (l *launcher) newSink() *outputSink {
	sink := &outputSink{tail: &tailBuffer{max: tailBytes}, left: logCap - len(logNotice)}
	// O_TRUNC: each start begins a new log. The output of the run that goes on
	// now is the output that a person needs.
	f, err := os.OpenFile(l.cfg.LogPath(), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		if l.log != nil {
			l.log.Log("player.log.fail", err.Error())
		}
		return sink
	}
	sink.file = f
	return sink
}

// stop ends mpv and each stray process of the kiosk account (see killStray). It
// returns when they have ended, so that the caller can give the display to
// somebody else.
func (l *launcher) stop() {
	l.stopOwn()
	// A failed lookup of the account is not reported here: the next start
	// reports it, and a stop runs at each look at the connectors in a display
	// wait.
	if n, err := killStray(l.cfg.KioskUser); n > 0 && l.log != nil {
		text := fmt.Sprintf("%d processes of the kiosk account were still running, for example an mpv of a daemon that "+
			"crashed; they are stopped", n)
		if err != nil {
			text += "; " + err.Error()
		}
		l.log.Log("player.stray", text)
	}
}

// stopOwn ends the mpv that this launcher started. It asks first and then
// insists. It returns when the process has ended.
func (l *launcher) stopOwn() {
	l.mu.Lock()
	cmd, exited := l.cmd, l.exited
	l.cmd = nil
	l.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}
	select {
	case <-exited:
		return
	default:
	}

	terminate(cmd.Process.Pid, false)
	select {
	case <-exited:
		return
	case <-time.After(stopGrace):
	}
	terminate(cmd.Process.Pid, true)
	// SIGKILL cannot be refused, so this wait is short. It ends when the
	// kernel reaped the process and the DRM device is free. The bound is for
	// a process in an uninterruptible wait in the kernel.
	select {
	case <-exited:
	case <-time.After(stopGrace):
		if l.log != nil {
			l.log.Log("player.stop.fail", fmt.Sprintf("mpv (pid %d) did not end after SIGKILL", cmd.Process.Pid))
		}
	}
}

// alive reports if mpv runs.
func (l *launcher) alive() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.cmd == nil || l.exited == nil {
		return false
	}
	select {
	case <-l.exited:
		return false
	default:
		return true
	}
}

// pid gives the process ID of mpv, or 0.
func (l *launcher) pid() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cmd == nil || l.cmd.Process == nil {
		return 0
	}
	return l.cmd.Process.Pid
}

// exitReason gives a short sentence about the last exit, with the end of the
// output of mpv. It is for the ops log.
func (l *launcher) exitReason() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	reason := "mpv ended"
	if l.err != nil {
		reason = "mpv ended: " + l.err.Error()
	}
	if l.out != nil {
		// Output that holds only noise gives no part: "output: " with nothing
		// after it says nothing.
		if line := lastLine(l.out.String()); line != "" {
			reason += "; output: " + line
		}
	}
	return reason
}

// lastOutput gives the last line of the output of mpv, or "".
func (l *launcher) lastOutput() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.out == nil {
		return ""
	}
	return lastLine(l.out.String())
}

// outputSink takes every byte that mpv writes. It sends the bytes to two places:
//
//	tail  the last tailBytes in memory, for the one ops log line at each exit
//	file  player.log on the tmpfs, for a person who reads the whole run
type outputSink struct {
	tail *tailBuffer

	mu   sync.Mutex
	file *os.File
	left int // bytes that the file still accepts
}

func (s *outputSink) Write(p []byte) (int, error) {
	s.tail.Write(p)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil || s.left <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > s.left {
		q = q[:s.left]
	}
	n, _ := s.file.Write(q)
	s.left -= n
	if s.left <= 0 {
		s.file.WriteString(logNotice)
	}
	// A full or broken file must never look like a broken pipe to mpv.
	return len(p), nil
}

// close shuts the file. The tail stays, because exitReason reads it after the
// process ended.
func (s *outputSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}
}

func (s *outputSink) String() string { return s.tail.String() }

// tailBuffer keeps the last max bytes that are written to it.
type tailBuffer struct {
	mu   sync.Mutex
	max  int
	data []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.data = append(t.data, p...)
	if len(t.data) > t.max {
		t.data = t.data[len(t.data)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.data)
}

// noise holds parts of the lines that mpv writes in normal work on a device that
// lacks something. Such a line never says why mpv ended, so lastLine skips it:
//   - auto-safe tries each hardware decoder of its list at each video, and each
//     decoder that the device does not have writes an error (seen on pp-zero:
//     vaapi, Vulkan and VDPAU, three lines at each video);
//   - the DRM output writes two lines at each start when there is no TTY;
//   - a decoder that does not know the option of the Pi decoder says so (see
//     DecoderOptions).
var noise = []string{
	"[vaapi] ", "[vdpau] ", "[vulkan] ", "[cuda] ",
	"[ffmpeg] VAAPI", "[ffmpeg] VDPAU", "[ffmpeg] Vulkan", "[ffmpeg] CUDA",
	"Can't open TTY for VT control", "Failed to set up VT switcher",
	"AVOption 'num_capture_buffers' not found",
}

// lastLine gives the last line that holds text and is not noise. The interesting
// message of a program that fails is at the end of its output.
func lastLine(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if slices.ContainsFunc(noise, func(n string) bool { return strings.Contains(line, n) }) {
			continue
		}
		if line != "" {
			if len(line) > 200 {
				line = line[:200]
			}
			return line
		}
	}
	return ""
}
