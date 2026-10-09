package server_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ethanpil/portapixel/internal/server/httpjson"
)

// TestADatabaseFaultIsNotARevokedToken covers the one 401 that ends a pairing.
//
// The device route answered token-revoked for each error of the token lookup. A busy
// or damaged database then told each screen that polled to drop its pairing, and a
// screen that paired by code needed the admin again.
func TestADatabaseFaultIsNotARevokedToken(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.pairDevice("px-dbfault1")

	f.db.Close()
	res := f.device(http.MethodGet, "/api/v1/manifest", token, nil)
	if res.status == http.StatusUnauthorized || strings.Contains(string(res.body), httpjson.TokenRevokedCode) {
		t.Fatalf("a database fault answered %d %s: the device would drop its pairing", res.status, res.body)
	}
	if res.status != http.StatusInternalServerError {
		t.Fatalf("a database fault answered %d, want 500: %s", res.status, res.body)
	}
}

// TestAProxyLineAfterTheClientLineWins covers a proxy that adds its own
// X-Forwarded-For line, as HAProxy does with "option forwardfor".
//
// The server read the first line only. That line came from the client, so a client
// that sent a new address each time got a new limiter bucket each time: no limit on
// password guesses and no limit on pending rows.
func TestAProxyLineAfterTheClientLineWins(t *testing.T) {
	f := newFleet(t)
	f.trustProxies([]string{"127.0.0.1", "::1"})

	res := f.call(http.MethodPost, "/api/v1/enroll", map[string]any{
		"device_id": "px-twoline1", "hardware_id": "hw-twoline",
	}, func(r *http.Request) {
		r.Header.Add("X-Forwarded-For", "203.0.113.66")
		r.Header.Add("X-Forwarded-For", "198.51.100.7")
	})
	f.mustOK(res, "an enroll with two forwarding lines")
	if got := f.pendingByDevice(t, "px-twoline1").IP; got != "198.51.100.7" {
		t.Fatalf("the server counted %q, want the address that the proxy added", got)
	}
}

// TestAPendingModeTokenCannotFillTheList covers the cap on new pending rows.
//
// The limiter counted the requests with no token only. A pending-mode token is in
// clear text on each card, and a request with it and a new device ID each time made
// a new row each time. The list then reached its cap of 200, and each real screen
// that paired by code was refused for up to a day.
func TestAPendingModeTokenCannotFillTheList(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.makeToken("pending", 0, "")

	limited := 0
	for i := 0; i < 8; i++ {
		_, res := f.enroll(fmt.Sprintf("px-slow%04d", i), token)
		switch res.status {
		case http.StatusOK:
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("attempt %d answered %d: %s", i, res.status, res.body)
		}
	}
	if limited == 0 {
		t.Fatal("eight new screens with a pending-mode token from one address were never limited")
	}
	if n, err := f.db.CountPending(); err != nil || n > 5 {
		t.Fatalf("one address made %d waiting rows (%v)", n, err)
	}
}

// TestARefusedNewScreenDoesNotBlockAGoodToken covers the two limiters of the enroll
// route together.
//
// A request that the pending limiter refused did not end the attempt that the
// wrong-token limiter had opened. Five such requests in a minute filled that limiter
// for the address, and a card with a good auto token behind the same router then got
// "too many enroll attempts".
func TestARefusedNewScreenDoesNotBlockAGoodToken(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.makeToken("auto", 0, "")

	for i := 0; i < 10; i++ {
		f.enroll(fmt.Sprintf("px-batch%03d", i), "")
	}
	out, res := f.enroll("px-goodcard", token)
	f.mustOK(res, "an enroll with a good auto token after the pending limit")
	if out.Status != "paired" {
		t.Fatalf("the card with a good token got %+v", out)
	}
}

// TestAPlaylistTakesOnlyWhatAScreenShows covers the kind of an item.
//
// The library took any file, and the playlist took any object of the library. A PDF
// or an SVG then went to each screen as an item, and each screen skipped it with no
// word on the server. The upload refuses such a file now, and the playlist refuses an
// object of an older library that is not an image or a video.
func TestAPlaylistTakesOnlyWhatAScreenShows(t *testing.T) {
	f := newFleet(t)
	f.login()

	if res := f.upload("/api/admin/media", "notes.pdf", []byte("%PDF-1.4")); res.status != http.StatusUnprocessableEntity {
		t.Fatalf("the upload of a PDF answered %d, want 422: %s", res.status, res.body)
	}

	old := addObject(t, f, "notes.pdf", "%PDF-1.4")
	res := f.adminCall(http.MethodPost, "/api/admin/playlists", map[string]any{
		"title": "Wrong kind",
		"items": []map[string]any{{"sha256": old, "name": "notes.png", "duration": 10}},
	})
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("a playlist with a PDF answered %d, want 422: %s", res.status, res.body)
	}
	fields := res.fields(t)
	if len(fields) != 1 || fields[0].Field != "items[0].sha256" {
		t.Fatalf("the answer names %+v", fields)
	}
}

// TestALongUploadNameKeepsItsExtension covers the cut of a name to 255 bytes.
//
// The cut took the first 255 bytes. That dropped the extension, which says image or
// video, and it could split a character in two. The playlist then took the object as
// "unknown", and each screen skipped it.
func TestALongUploadNameKeepsItsExtension(t *testing.T) {
	f := newFleet(t)
	f.login()

	name := strings.Repeat("é", 150) + ".png"
	res := f.mustOK(f.upload("/api/admin/media", name, imageBytes(t, 8, 8)), "a long name")
	var out struct {
		Media struct {
			OrigName string `json:"orig_name"`
		} `json:"media"`
	}
	res.json(t, &out)
	got := out.Media.OrigName
	if !strings.HasSuffix(got, ".png") || len(got) > 255 || !utf8.ValidString(got) {
		t.Fatalf("the name was stored as %q (%d bytes)", got, len(got))
	}
}

// TestAFailedDeleteDoesNotRejectTheRequestInstead covers the delete of a screen.
//
// The route read every error of the delete as "there is no such screen" and then
// rejected a pending request of the same device ID. A screen that another machine
// asked to be then stayed paired, and the admin read that it was gone.
func TestAFailedDeleteDoesNotRejectTheRequestInstead(t *testing.T) {
	f := newFleet(t)
	f.login()
	f.pairDevice("px-del00001")
	if _, res := f.enrollAs("px-del00001", hardwareOf("px-another"), ""); res.status != http.StatusOK {
		t.Fatalf("the second machine answered %d: %s", res.status, res.body)
	}

	// A fault of the write, and only of the write.
	raw, err := sql.Open("sqlite", f.dir+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER fail_delete BEFORE DELETE ON devices
		BEGIN SELECT RAISE(ABORT, 'an injected fault'); END`); err != nil {
		t.Fatal(err)
	}

	res := f.adminCall(http.MethodDelete, "/api/admin/devices/px-del00001", nil)
	if res.status == http.StatusOK {
		t.Fatalf("a delete that failed answered 200: %s", res.body)
	}
	if _, err := f.db.PendingByDevice("px-del00001"); err != nil {
		t.Fatalf("the failed delete rejected the waiting request: %v", err)
	}
}
