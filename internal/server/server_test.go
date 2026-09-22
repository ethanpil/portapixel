package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWithRecoverAnswersJSON covers the one middleware that stands between a fault of
// our own and the admin UI.
//
// net/http already stops a panic from ending the process, but it drops the connection
// with no answer and no record. web/shared/api.js then says "the answer is not JSON"
// and nothing says what happened. The two secret generators of
// internal/server/db panic when the system gives no random bytes, and both run inside
// the enroll handler.
func TestWithRecoverAnswersJSON(t *testing.T) {
	h := withRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("a fault of our own")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/devices", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a panic answered %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("a panic answered the type %q", got)
	}
	if !strings.HasPrefix(rec.Body.String(), `{"error"`) {
		t.Fatalf("a panic answered %q", rec.Body.String())
	}
}

// TestWithRecoverLetsTheAbortPanicThrough keeps the contract of net/http:
// http.ErrAbortHandler is the deliberate, silent end of a handler.
func TestWithRecoverLetsTheAbortPanicThrough(t *testing.T) {
	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Fatalf("the recovery swallowed %v", p)
		}
	}()
	h := withRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

// TestSecureHeadersChooseThePolicyByPath keeps the two policies apart. The admin UI
// needs its own scripts; a stored object must run nothing at all.
func TestSecureHeadersChooseThePolicyByPath(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	cases := []struct {
		path   string
		object bool
	}{
		{"/", false},
		{"/api/admin/devices", false},
		{"/shared/pp.css", false},
		{"/api/v1/manifest", false},
		{"/api/v1/media/" + strings.Repeat("a", 64), true},
		{"/api/v1/releases/1.5.0/portapixeld-amd64", true},
		{"/api/admin/media/" + strings.Repeat("a", 64) + "/thumb", true},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		secureHeaders(nil, ok).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		csp := rec.Header().Get("Content-Security-Policy")
		corp := rec.Header().Get("Cross-Origin-Resource-Policy")
		switch {
		case c.object && !strings.HasPrefix(csp, "sandbox"):
			t.Errorf("%s carries the policy %q, want the sandbox", c.path, csp)
		case c.object && corp != "same-origin":
			t.Errorf("%s carries the resource policy %q", c.path, corp)
		case !c.object && strings.HasPrefix(csp, "sandbox"):
			t.Errorf("%s carries the sandbox policy", c.path)
		}
		// The device API is not a browser, so it takes no document policy. Everything
		// else on the browser side does.
		if !c.object && strings.HasPrefix(c.path, "/api/v1/") && csp != "" {
			t.Errorf("%s carries the policy %q", c.path, csp)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s carries no nosniff", c.path)
		}
	}
}

// TestStrictTransportOnlyOverTLS keeps the year-long header off a plain HTTP answer.
// A homelab server on a closed network answers plain HTTP by design, and the header
// would tell the browser to refuse the only scheme that it has.
func TestStrictTransportOnlyOverTLS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	plain := httptest.NewRecorder()
	secureHeaders(func(*http.Request) bool { return false }, ok).
		ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := plain.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("a plain HTTP answer carries %q", got)
	}

	secure := httptest.NewRecorder()
	secureHeaders(func(*http.Request) bool { return true }, ok).
		ServeHTTP(secure, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := secure.Header().Get("Strict-Transport-Security"); got != hsts {
		t.Errorf("a TLS answer carries %q, want %q", got, hsts)
	}
}
