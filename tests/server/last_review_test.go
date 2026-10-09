package server_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

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
