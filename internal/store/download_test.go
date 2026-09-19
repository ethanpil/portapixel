package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// body is the object that the test servers hand out.
var body = bytes.Repeat([]byte("PortaPixel media bytes. "), 500) // 12000 bytes

func bodySHA() string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// recorder keeps the Range header of each request that a server got.
type recorder struct {
	mu     sync.Mutex
	ranges []string
	auth   []string
}

func (r *recorder) add(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ranges = append(r.ranges, req.Header.Get("Range"))
	r.auth = append(r.auth, req.Header.Get("Authorization"))
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ranges...)
}

// rangeServer serves the object and honours Range requests.
func rangeServer(rec *recorder) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec != nil {
			rec.add(r)
		}
		http.ServeContent(w, r, "media.bin", time.Unix(0, 0), bytes.NewReader(body))
	}))
}

func newClient() *http.Client { return &http.Client{Timeout: 10 * time.Second} }

func TestDownloadWholeFile(t *testing.T) {
	rec := &recorder{}
	srv := rangeServer(rec)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "abababab-media.bin")
	if err := Download(context.Background(), newClient(), srv.URL, "tok", dest, bodySHA(), int64(len(body))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	assertFile(t, dest)
	if got := rec.list(); len(got) != 1 || got[0] != "" {
		t.Fatalf("ranges = %v, want one request with no range", got)
	}
	if rec.auth[0] != "Bearer tok" {
		t.Fatalf("Authorization = %q", rec.auth[0])
	}
}

func TestDownloadResumesAPartFile(t *testing.T) {
	rec := &recorder{}
	srv := rangeServer(rec)
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "media.bin")
	// A part file from an earlier attempt holds the first half.
	half := len(body) / 2
	if err := os.WriteFile(dest+".part", body[:half], 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Download(context.Background(), newClient(), srv.URL, "", dest, bodySHA(), int64(len(body))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	assertFile(t, dest)
	want := "bytes=6000-"
	if got := rec.list(); len(got) != 1 || got[0] != want {
		t.Fatalf("ranges = %v, want [%q]", got, want)
	}
}

func TestDownloadResumesAfterADroppedConnection(t *testing.T) {
	// The first server sends half of the object and then drops the connection.
	dropped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // close the connection, no 500 answer
	}))
	defer dropped.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "media.bin")
	part := dest + ".part"

	err := Download(context.Background(), newClient(), dropped.URL, "", dest, bodySHA(), int64(len(body)))
	if err == nil {
		t.Fatal("want an error when the connection drops")
	}
	info, statErr := os.Stat(part)
	if statErr != nil {
		t.Fatalf("the part file must stay after a dropped connection: %v", statErr)
	}
	if info.Size() == 0 || info.Size() >= int64(len(body)) {
		t.Fatalf("the part file holds %d bytes, want part of %d", info.Size(), len(body))
	}

	// The next attempt continues where the first one stopped.
	rec := &recorder{}
	srv := rangeServer(rec)
	defer srv.Close()
	if err := Download(context.Background(), newClient(), srv.URL, "", dest, bodySHA(), int64(len(body))); err != nil {
		t.Fatalf("the resume failed: %v", err)
	}
	assertFile(t, dest)
	got := rec.list()
	if len(got) != 1 || !strings.HasPrefix(got[0], "bytes=") {
		t.Fatalf("ranges = %v, want one Range request", got)
	}
}

func TestDownloadServerIgnoresTheRange(t *testing.T) {
	// This server answers 200 with the whole object, even for a Range request.
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "media.bin")
	if err := os.WriteFile(dest+".part", body[:100], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), newClient(), srv.URL, "", dest, bodySHA(), int64(len(body))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	assertFile(t, dest)
	if got := rec.list(); len(got) != 1 {
		t.Fatalf("ranges = %v, want one request", got)
	}
}

func TestDownloadRangeRefused(t *testing.T) {
	// This server refuses every Range request with 416 and serves the whole
	// object to a plain request.
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "media.bin")
	if err := os.WriteFile(dest+".part", body[:100], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), newClient(), srv.URL, "", dest, bodySHA(), int64(len(body))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	assertFile(t, dest)
	got := rec.list()
	if len(got) != 2 || got[0] == "" || got[1] != "" {
		t.Fatalf("ranges = %v, want a Range request and then a plain request", got)
	}
}

// TestDownloadStopsAtThePromisedSize covers a server that sends more bytes than
// the manifest promises. The extra bytes must never reach the card: they would
// fill the media partition, and the part file that stayed behind would then stop
// every later write.
func TestDownloadStopsAtThePromisedSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
		// The manifest promised len(body). This server goes on.
		w.Write(bytes.Repeat([]byte("x"), 40000))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "media.bin")
	if err := Download(context.Background(), newClient(), srv.URL, "", dest, bodySHA(), int64(len(body))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	assertFile(t, dest)
}

// TestDownloadRefusesARangeFromAnotherOffset covers a cache or a proxy that
// answers 206 from the start although we asked to continue. Those bytes at the
// end of the part file would make an object that is wrong in the middle.
func TestDownloadRefusesARangeFromAnotherOffset(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if r.Header.Get("Range") == "" {
			w.Write(body)
			return
		}
		// The answer says 206 but begins at byte 0.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(body)-1, len(body)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "media.bin")
	if err := os.WriteFile(dest+".part", body[:100], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), newClient(), srv.URL, "", dest, bodySHA(), int64(len(body))); err != nil {
		t.Fatalf("Download: %v", err)
	}
	assertFile(t, dest)
	got := rec.list()
	if len(got) != 2 || got[0] == "" || got[1] != "" {
		t.Fatalf("ranges = %v, want a Range request and then a plain request", got)
	}
}

func TestRangeStart(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int64
		ok    bool
	}{
		{name: "a range from the start", value: "bytes 0-999/1000", want: 0, ok: true},
		{name: "a range that continues", value: "bytes 600-999/1000", want: 600, ok: true},
		{name: "extra spaces", value: "  bytes  600-999/1000 ", want: 600, ok: true},
		{name: "an unknown total", value: "bytes 600-999/*", want: 600, ok: true},
		{name: "no header at all", value: ""},
		{name: "another unit", value: "items 600-999/1000"},
		{name: "no first byte", value: "bytes -999/1000"},
		{name: "not a number", value: "bytes abc-999/1000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := rangeStart(tt.value)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("rangeStart(%q) = %d, %v, want %d, %v", tt.value, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestDownloadFailures(t *testing.T) {
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		wantSHA  string
		wantSize int64
		partLeft bool // the part file must stay for a later resume
	}{
		{
			name:     "the hash does not match",
			handler:  func(w http.ResponseWriter, r *http.Request) { w.Write(body) },
			wantSHA:  strings.Repeat("00", 32),
			wantSize: int64(len(body)),
		},
		{
			name:     "the size does not match",
			handler:  func(w http.ResponseWriter, r *http.Request) { w.Write(body) },
			wantSHA:  bodySHA(),
			wantSize: int64(len(body)) + 1,
			partLeft: true, // fewer bytes than promised: a resume can continue
		},
		{
			name:     "the object is not there",
			handler:  func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
			wantSHA:  bodySHA(),
			wantSize: int64(len(body)),
			partLeft: true, // the part file is empty but harmless
		},
		{
			name:     "the server fails",
			handler:  func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
			wantSHA:  bodySHA(),
			wantSize: int64(len(body)),
			partLeft: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			dest := filepath.Join(t.TempDir(), "media.bin")
			err := Download(context.Background(), newClient(), srv.URL, "", dest, tt.wantSHA, tt.wantSize)
			if err == nil {
				t.Fatal("want an error")
			}
			if _, err := os.Stat(dest); err == nil {
				t.Fatal("the destination file must not exist")
			}
			_, statErr := os.Stat(dest + ".part")
			if tt.partLeft && statErr != nil {
				t.Fatalf("the part file must stay: %v", statErr)
			}
			if !tt.partLeft && statErr == nil {
				t.Fatal("the part file must be deleted when the content is wrong")
			}
		})
	}
}

func TestDownloadNeedsAHash(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "media.bin")
	if err := Download(context.Background(), newClient(), "http://127.0.0.1:1", "", dest, "", 0); err == nil {
		t.Fatal("want an error when the wanted hash is empty")
	}
}

func assertFile(t *testing.T, dest string) {
	t.Helper()
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read the object: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("the object has %d bytes, want %d", len(got), len(body))
	}
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Fatal("the part file must be gone after a good download")
	}
}
