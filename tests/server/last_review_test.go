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
