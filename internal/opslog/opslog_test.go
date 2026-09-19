package opslog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
