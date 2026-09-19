package browser

import (
	"fmt"
	"os"
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

// logCap is the largest browser.log that we write. The file lives on the capped
// tmpfs of the browser (D39), which also holds the profile and the cache. A
// file with no cap would fill that tmpfs and Chromium would then fail in a way
// that looks like a rendering fault. Output after the cap goes nowhere.
const logCap = 1 << 20

// logNotice is the last line of a log that reached the cap. Without it, a person
// cannot tell a full file from a browser that stopped writing.
const logNotice = "\n--- browser.log is full at 1 MiB; more output goes nowhere ---\n"

// launcher owns the browser process. Both navigation rungs use it, so there is
// one place that starts a process and one place that stops it.
type launcher struct {
	cfg CommandConfig
	log *opslog.Log

	mu     sync.Mutex
	cmd    *exec.Cmd
	url    string
	out    *outputSink
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
	out := l.newSink()
	// One value for both streams. os/exec then gives the child one pipe, so the
	// two streams stay in the order in which the browser wrote them.
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		out.close()
		return fmt.Errorf("start the browser: %w", err)
	}

	exited := make(chan struct{})

	l.mu.Lock()
	l.cmd, l.url, l.out, l.exited, l.err = cmd, url, out, exited, nil
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

// newSink makes the destination of the browser's output: the memory tail for the
// ops log, and browser.log for a person who must see the whole start-up.
//
// A file that cannot be opened is not a reason to leave the browser off. The
// tail still works, so the ops log still gets the last line at each exit.
func (l *launcher) newSink() *outputSink {
	sink := &outputSink{tail: &tailBuffer{max: tailBytes}, left: logCap - len(logNotice)}
	path := l.cfg.LogPath()
	if path == "" {
		return sink
	}
	// O_TRUNC: each launch starts a new log. The output of the launch that runs
	// now is the output that a person needs.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		if l.log != nil {
			l.log.Log("browser.log.fail", err.Error())
		}
		return sink
	}
	sink.file = f
	return sink
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

// outputSink takes every byte that cage and Chromium write. It sends the bytes
// to two places:
//
//	tail  the last tailBytes in memory, for the one ops log line at each exit
//	file  browser.log on the capped tmpfs, for a person who reads the whole run
//
// The file stops at logCap. The browser can write megabytes in a minute, and the
// tmpfs that holds the profile must keep its room for the profile.
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
	if s.file == nil {
		return len(p), nil
	}
	if s.left <= 0 {
		return len(p), nil // the file is full; drop the rest
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
	// A full or broken file must never look like a broken pipe to the browser.
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
