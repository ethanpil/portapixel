package browser

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"os/exec"

	"github.com/ethanpil/portapixel/internal/opslog"
)

// stopGrace is the time that the browser gets to end after a polite signal.
// After it, the signal is not polite any more.
const stopGrace = 5 * time.Second

// tailBytes is how much of the browser's output we keep. Chromium writes a lot
// and /var/log is a small tmpfs, so we keep the end of the output and write it to
// the ops log only when the process fails. That is the part that says why.
const tailBytes = 2048

// launcher owns the browser process. Both navigation rungs use it, so there is
// one place that starts a process and one place that stops it.
type launcher struct {
	cfg CommandConfig
	log *opslog.Log

	mu     sync.Mutex
	cmd    *exec.Cmd
	url    string
	out    *tailBuffer
	exited chan struct{}
	err    error
}

func newLauncher(cfg CommandConfig, log *opslog.Log) *launcher {
	return &launcher{cfg: cfg, log: log}
}

// start stops any browser that runs and starts a new one on url.
func (l *launcher) start(url string) error {
	l.stop()

	cmd, err := l.cfg.Build(url)
	if err != nil {
		return err
	}
	out := &tailBuffer{max: tailBytes}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the browser: %w", err)
	}

	exited := make(chan struct{})

	l.mu.Lock()
	l.cmd, l.url, l.out, l.exited, l.err = cmd, url, out, exited, nil
	l.mu.Unlock()

	go func() {
		err := cmd.Wait()
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
		close(exited)
	}()
	return nil
}

// stop ends the browser. It asks first and then insists.
func (l *launcher) stop() error {
	l.mu.Lock()
	cmd, exited := l.cmd, l.exited
	l.cmd = nil
	l.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}
	select {
	case <-exited:
		return nil
	default:
	}

	terminate(cmd.Process.Pid, false)
	select {
	case <-exited:
		return nil
	case <-time.After(stopGrace):
	}
	return terminate(cmd.Process.Pid, true)
}

// alive reports if the browser process runs.
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

// lastURL gives the URL that the browser started with.
func (l *launcher) lastURL() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.url
}

// exitReason gives a short sentence about the last exit, with the end of the
// browser's own output. It is for the ops log.
func (l *launcher) exitReason() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	reason := "the browser ended"
	if l.err != nil {
		reason = l.err.Error()
	}
	if l.out != nil {
		if text := l.out.String(); text != "" {
			reason += "; output: " + lastLine(text)
		}
	}
	return reason
}

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

// lastLine gives the last line that holds text. The interesting message of a
// program that fails is at the end of its output.
func lastLine(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			if len(line) > 200 {
				line = line[:200]
			}
			return line
		}
	}
	return ""
}
