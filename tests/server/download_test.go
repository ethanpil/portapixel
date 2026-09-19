package server_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethanpil/portapixel/internal/store"
)

// bigFile makes a body that is worth a Range request.
func bigFile(t *testing.T, size int) []byte {
	t.Helper()
	body := make([]byte, size)
	if _, err := rand.Read(body); err != nil {
		t.Fatal(err)
	}
	return body
}

// TestDownloadWithTheRealClient proves that the two ends of the protocol agree.
//
// The client is internal/store.Download, which is the code that the device runs.
// The server is the media route. A test that used its own HTTP client would prove
// that the server answers something; this one proves that the device gets its
// file (D24).
func TestDownloadWithTheRealClient(t *testing.T) {
	f := newFleet(t)
	f.login()

	body := bigFile(t, 300_000)
	sha := f.uploadMedia("promo.mp4", body)
	token := f.pairDevice("px-dl000001")

	dest := filepath.Join(t.TempDir(), "promo.mp4")
	url := f.srv.URL + "/api/v1/media/" + sha

	if err := store.Download(context.Background(), http.DefaultClient, url, token,
		dest, sha, int64(len(body))); err != nil {
		t.Fatalf("the download failed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("the file that arrived is not the file that went up")
	}
	// The part file is gone: the rename is the commit.
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Fatal("the part file is still there")
	}
}

// TestDownloadResumesFromTheMiddle is the D24 case: a dropped connection leaves a
// part file, and the retry asks for the rest with a Range header.
func TestDownloadResumesFromTheMiddle(t *testing.T) {
	f := newFleet(t)
	f.login()

	body := bigFile(t, 300_000)
	sha := f.uploadMedia("promo.mp4", body)
	token := f.pairDevice("px-dl000002")

	dest := filepath.Join(t.TempDir(), "promo.mp4")
	// A part file that holds the first half, as a dropped download would leave.
	half := len(body) / 2
	if err := os.WriteFile(dest+".part", body[:half], 0o644); err != nil {
		t.Fatal(err)
	}

	url := f.srv.URL + "/api/v1/media/" + sha
	if err := store.Download(context.Background(), http.DefaultClient, url, token,
		dest, sha, int64(len(body))); err != nil {
		t.Fatalf("the resumed download failed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatal("the resumed file is not the file that went up")
	}
}

// TestDownloadPartFileWithWrongBytes proves that a resume cannot keep bad bytes:
// the hash of the whole file is what decides.
func TestDownloadRefusesAPartFileOfAnotherObject(t *testing.T) {
	f := newFleet(t)
	f.login()

	body := bigFile(t, 100_000)
	sha := f.uploadMedia("a.mp4", body)
	token := f.pairDevice("px-dl000003")

	dest := filepath.Join(t.TempDir(), "a.mp4")
	// The part file holds the first half of another file.
	other := bigFile(t, 50_000)
	if err := os.WriteFile(dest+".part", other, 0o644); err != nil {
		t.Fatal(err)
	}

	url := f.srv.URL + "/api/v1/media/" + sha
	err := store.Download(context.Background(), http.DefaultClient, url, token,
		dest, sha, int64(len(body)))
	if err == nil {
		t.Fatal("a part file of another object gave a good download")
	}
	// The part file is removed, so the next try starts again.
	if _, statErr := os.Stat(dest + ".part"); statErr == nil {
		t.Fatal("the bad part file was kept")
	}
}

func TestMediaRouteHeaders(t *testing.T) {
	f := newFleet(t)
	f.login()

	body := bigFile(t, 10_000)
	sha := f.uploadMedia("a.mp4", body)
	token := f.pairDevice("px-dl000004")
	path := "/api/v1/media/" + sha

	// The whole file.
	res := f.mustOK(f.device(http.MethodGet, path, token, nil), "the download")
	if len(res.body) != len(body) {
		t.Fatalf("the answer holds %d bytes, want %d", len(res.body), len(body))
	}
	if res.header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("the route does not announce Range support: %q", res.header.Get("Accept-Ranges"))
	}
	if etag := res.header.Get("ETag"); etag != `"`+sha+`"` {
		t.Fatalf("the ETag is %q, want the hash", etag)
	}

	// One range of the middle.
	ranged := f.call(http.MethodGet, path, nil, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Range", "bytes=1000-1999")
	})
	if ranged.status != http.StatusPartialContent {
		t.Fatalf("the range request answered %d", ranged.status)
	}
	if !bytes.Equal(ranged.body, body[1000:2000]) {
		t.Fatal("the range holds the wrong bytes")
	}
	if got := ranged.header.Get("Content-Range"); got != "bytes 1000-1999/10000" {
		t.Fatalf("the Content-Range is %q", got)
	}

	// A hash that the library does not hold.
	missing := sha256.Sum256([]byte("nothing"))
	res = f.device(http.MethodGet, "/api/v1/media/"+hex.EncodeToString(missing[:]), token, nil)
	if res.status != http.StatusNotFound {
		t.Fatalf("a missing object answered %d", res.status)
	}

	// A hash that is not a hash. The value becomes a file path, so the shape is
	// checked before anything opens a file.
	for _, bad := range []string{"not-a-hash", strings.Repeat("z", 64), strings.Repeat("A", 64)} {
		res = f.device(http.MethodGet, "/api/v1/media/"+bad, token, nil)
		if res.status != http.StatusBadRequest {
			t.Errorf("the hash %q answered %d", bad, res.status)
		}
	}
}
