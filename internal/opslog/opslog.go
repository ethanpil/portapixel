package opslog

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// Limits of the log. The log trims to keepLines after it passes maxLines.
const (
	maxLines  = 1200
	keepLines = 1000
)

// timeFormat is the time format of a log line: UTC, to the second.
const timeFormat = "2006-01-02T15:04:05Z"

// Entry is one parsed line of the log.
type Entry struct {
	Time    time.Time `json:"time"`
	Event   string    `json:"event"`
	Details string    `json:"details"`
}

// Log is the append-only event log at one path. It is safe for use by more than
// one goroutine.
type Log struct {
	mu    sync.Mutex
	path  string
	lines int // number of lines in the file
	now   func() time.Time
}

// New makes a Log for the file at path. It counts the lines that are already in
// the file, so that the trim limit counts the whole file. A missing file is not
// an error: the first write makes it.
func New(path string) *Log {
	l := &Log{path: path, now: time.Now}
	if data, err := os.ReadFile(path); err == nil {
		l.lines = countLines(data)
	}
	return l
}

// Log appends one line: time, event, details, separated by tab characters.
//
// Log gives no error. A log write must never stop the work that it reports, and
// the caller has no useful answer to a failed log write. The log does not fsync
// each line either: this is a log, and one lost line after a power cut costs
// nothing. The trim, which rewrites the whole file, does use the safe write.
func (l *Log) Log(event, details string) {
	line := l.now().UTC().Format(timeFormat) + "\t" + clean(event) + "\t" + clean(details) + "\n"

	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return
	}
	f.Close()

	l.lines++
	if l.lines > maxLines {
		l.trim()
	}
}

// Tail gives the newest n entries, oldest first. A line that has the wrong
// shape is skipped: a damaged log must never stop the admin UI.
func (l *Log) Tail(n int) []Entry {
	if n <= 0 {
		return nil
	}

	l.mu.Lock()
	data, err := os.ReadFile(l.path)
	l.mu.Unlock()
	if err != nil {
		return nil
	}

	lines := splitLines(data)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]Entry, 0, len(lines))
	for _, line := range lines {
		if e, ok := parse(line); ok {
			out = append(out, e)
		}
	}
	return out
}

// trim rewrites the file with the newest keepLines lines. The caller holds the
// lock. A trim failure leaves the old file in place, because the write is
// staged and committed with a rename.
func (l *Log) trim() {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	lines := splitLines(data)
	if len(lines) <= keepLines {
		l.lines = len(lines)
		return
	}
	lines = lines[len(lines)-keepLines:]
	out := []byte(strings.Join(lines, "\n") + "\n")
	if err := fsutil.WriteFileAtomic(l.path, out, 0o644); err != nil {
		return
	}
	l.lines = len(lines)
}

// parse reads one line of the log.
func parse(line string) (Entry, bool) {
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) != 3 {
		return Entry{}, false
	}
	ts, err := time.Parse(timeFormat, parts[0])
	if err != nil {
		return Entry{}, false
	}
	return Entry{Time: ts, Event: parts[1], Details: parts[2]}, true
}

// clean removes the characters that would break the line format.
func clean(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// splitLines gives the non-empty lines of data.
func splitLines(data []byte) []string {
	raw := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func countLines(data []byte) int {
	n := bytes.Count(data, []byte("\n"))
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		n++
	}
	return n
}
