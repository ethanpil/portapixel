package server_test

import (
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
