package opslog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func newTestLog(t *testing.T) *Log {
	t.Helper()
	l := New(filepath.Join(t.TempDir(), "ops.log"))
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return at }
	return l
}

func TestLogLineFormat(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		details string
		want    string
	}{
		{
			name:    "plain line",
			event:   "boot",
			details: "version dev",
			want:    "2026-09-18T12:00:00Z\tboot\tversion dev\n",
		},
		{
			name:    "empty details",
			event:   "rescan",
			details: "",
			want:    "2026-09-18T12:00:00Z\trescan\t\n",
		},
		{
			name:    "tabs and newlines become spaces",
			event:   "sync\tfail",
			details: "line one\nline two",
			want:    "2026-09-18T12:00:00Z\tsync fail\tline one line two\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newTestLog(t)
			l.Log(tt.event, tt.details)
			data, err := os.ReadFile(l.path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tt.want {
				t.Fatalf("line = %q, want %q", data, tt.want)
			}
		})
	}
}

// TestLogAfterATornLine covers the log that a power cut stopped in the middle of
// a line. The next line must stay a line of its own: a log that joins the two
// loses the old event and the new one.
func TestLogAfterATornLine(t *testing.T) {
	tests := []struct {
		name string
		// torn is the content that the power cut left in the file.
		torn      string
		wantLines int
	}{
		{
			name:      "the last line has no newline",
			torn:      "2026-09-18T10:00:00Z\tsync\tstarted\n2026-09-18T11:00:00Z\tsync\tpar",
			wantLines: 3,
		},
		{
			name:      "the file holds one torn line only",
			torn:      "2026-09-18T11:00:00Z\tsync\tpar",
			wantLines: 2,
		},
		{
			name:      "a whole file goes on as before",
			torn:      "2026-09-18T10:00:00Z\tsync\tstarted\n",
			wantLines: 2,
		},
		{name: "an empty file", wantLines: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ops.log")
			if tt.torn != "" {
				if err := os.WriteFile(path, []byte(tt.torn), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			l := New(path)
			at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
			l.now = func() time.Time { return at }

			l.Log("boot", "version dev")

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(data), "\n"); got != tt.wantLines {
				t.Fatalf("the file holds %d whole lines, want %d: %q", got, tt.wantLines, data)
			}
			// The new event must be the newest entry that Tail can read.
			last := l.Tail(1)
			if len(last) != 1 || last[0].Event != "boot" || last[0].Details != "version dev" {
				t.Fatalf("Tail lost the new event: %+v", last)
			}
		})
	}
}

func TestTail(t *testing.T) {
	l := newTestLog(t)
	for i := range 5 {
		l.Log("event", fmt.Sprintf("%d", i))
	}

	tests := []struct {
		n    int
		want []string
	}{
		{n: 0, want: nil},
		{n: 1, want: []string{"4"}},
		{n: 3, want: []string{"2", "3", "4"}},
		{n: 99, want: []string{"0", "1", "2", "3", "4"}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("n=%d", tt.n), func(t *testing.T) {
			got := l.Tail(tt.n)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d entries, want %d", len(got), len(tt.want))
			}
			for i, e := range got {
				if e.Details != tt.want[i] {
					t.Fatalf("entry %d details = %q, want %q", i, e.Details, tt.want[i])
				}
				if e.Event != "event" {
					t.Fatalf("entry %d event = %q", i, e.Event)
				}
				if e.Time.IsZero() {
					t.Fatalf("entry %d has no time", i)
				}
			}
		})
	}
}

func TestTailOnMissingFile(t *testing.T) {
	l := New(filepath.Join(t.TempDir(), "ops.log"))
	if got := l.Tail(10); got != nil {
		t.Fatalf("Tail on a missing file = %v, want nil", got)
	}
}

func TestTailSkipsBadLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops.log")
	data := "junk\n" +
		"2026-09-18T12:00:00Z\tboot\tok\n" +
		"not-a-time\tboot\tok\n" +
		"\n" +
		"2026-09-18T12:00:01Z\tsync\tdone\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	got := New(path).Tail(10)
	if len(got) != 2 {
		t.Fatalf("got %d good entries, want 2: %v", len(got), got)
	}
	if got[0].Event != "boot" || got[1].Event != "sync" {
		t.Fatalf("entries = %v", got)
	}
}

func TestTrim(t *testing.T) {
	l := newTestLog(t)
	for i := range maxLines {
		l.Log("event", fmt.Sprintf("%d", i))
	}
	if l.lines != maxLines {
		t.Fatalf("lines = %d, want %d", l.lines, maxLines)
	}

	// One more line passes the limit and starts the trim.
	l.Log("event", "last")
	if l.lines != keepLines {
		t.Fatalf("lines after the trim = %d, want %d", l.lines, keepLines)
	}
	data, err := os.ReadFile(l.path)
	if err != nil {
		t.Fatal(err)
	}
	lines := splitLines(data)
	if len(lines) != keepLines {
		t.Fatalf("file has %d lines, want %d", len(lines), keepLines)
	}
	if !strings.HasSuffix(lines[len(lines)-1], "\tlast") {
		t.Fatalf("the newest line was lost: %q", lines[len(lines)-1])
	}
	// The oldest kept line must be the one that keepLines from the end demands.
	wantFirst := fmt.Sprintf("\t%d", maxLines+1-keepLines)
	if !strings.HasSuffix(lines[0], wantFirst) {
		t.Fatalf("oldest kept line = %q, want the one that ends with %q", lines[0], wantFirst)
	}
}

func TestNewCountsExistingLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops.log")
	var b strings.Builder
	for i := range maxLines {
		fmt.Fprintf(&b, "2026-09-18T12:00:00Z\tevent\t%d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	l := New(path)
	if l.lines != maxLines {
		t.Fatalf("counted %d lines, want %d", l.lines, maxLines)
	}
	l.Log("event", "one more")
	if l.lines != keepLines {
		t.Fatalf("lines after the trim = %d, want %d", l.lines, keepLines)
	}
}

func TestConcurrentLog(t *testing.T) {
	l := New(filepath.Join(t.TempDir(), "ops.log"))
	const writers = 8
	const each = 50
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range each {
				l.Log("event", fmt.Sprintf("writer %d line %d", w, i))
			}
		}(w)
	}
	wg.Wait()

	got := l.Tail(writers * each)
	if len(got) != writers*each {
		t.Fatalf("got %d entries, want %d", len(got), writers*each)
	}
}

// The trim counts LINES, so a line with no bound gives the file no bound in bytes.
// A sync error that names a whole URL, or an error of a program that printed a page,
// made lines of any length on a flash card that holds the ops log for ever (D35).
func TestALongLineIsCut(t *testing.T) {
	dir := t.TempDir()
	l := New(filepath.Join(dir, "ops.log"))
	l.Log(strings.Repeat("e", 4000), strings.Repeat("d", 40000))

	entries := l.Tail(1)
	if len(entries) != 1 {
		t.Fatalf("the log holds %d entries", len(entries))
	}
	if len(entries[0].Event) > maxField+3 {
		t.Errorf("the event is %d bytes long", len(entries[0].Event))
	}
	if len(entries[0].Details) > maxField+3 {
		t.Errorf("the details are %d bytes long", len(entries[0].Details))
	}
	if !strings.HasSuffix(entries[0].Details, "...") {
		t.Error("a line that was cut does not say so")
	}

	// The whole file stays inside a bound that the flash and the admin UI can hold.
	info, err := os.Stat(filepath.Join(dir, "ops.log"))
	if err != nil {
		t.Fatal(err)
	}
	// The time, two tab characters, two fields and the newline.
	if want := int64(2*(maxField+3) + 32); info.Size() > want {
		t.Errorf("one line made a file of %d bytes, want %d at most", info.Size(), want)
	}

	// A line that holds a character of more than one byte is still valid UTF-8.
	l.Log("unicode", strings.Repeat("é", maxField))
	last := l.Tail(1)[0]
	if !utf8.ValidString(last.Details) {
		t.Error("the cut broke a character")
	}
}
