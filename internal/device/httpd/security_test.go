package httpd

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every answer of the daemon carries the three headers. The admin UI, the player
// and the content of the card share ONE origin on plain HTTP, so a header that
// only some answers carry is a header that the interesting answer does not.
func TestEveryAnswerCarriesTheSecurityHeaders(t *testing.T) {
	f := newFx(t)
	if err := os.MkdirAll(filepath.Join(f.media, "lobby"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.media, "lobby", "a.jpg"), []byte("picture"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		method string
		target string
		opts   []func(*request)
	}{
		{name: "the admin UI", method: http.MethodGet, target: "/"},
		{name: "the player page", method: http.MethodGet, target: "/player"},
		{name: "the status", method: http.MethodGet, target: "/api/status"},
		{name: "a media file", method: http.MethodGet, target: "/media/lobby/a.jpg"},
		{name: "the licence list", method: http.MethodGet, target: "/licenses"},
		{name: "a route that is not there", method: http.MethodGet, target: "/api/nothing"},
		{
			name: "a refused Host", method: http.MethodGet, target: "/api/status",
			opts: []func(*request){func(r *request) { r.host = "evil.example.com" }},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			w := f.do(tt.method, tt.target, nil, tt.opts...)
			h := w.Header()
			if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q", got)
			}
			if got := h.Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy = %q", got)
			}
			if got := h.Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options = %q", got)
			}
			if h.Get("Content-Security-Policy") == "" {
				t.Error("there is no Content-Security-Policy")
			}
		})
	}
}

// A file from the card is INERT. An SVG is a document with a script in it, and
// /media/ needs no session by design, so an SVG that somebody planted would run on
// the origin of the admin UI and of the player. The sandbox policy gives it an
// opaque origin and no scripts.
//
// The header must be on a 200, on a 206 of a Range request and on a 304, because a
// browser uses the headers of the answer that it gets.
func TestMediaFilesGetTheSandboxPolicy(t *testing.T) {
	f := newFx(t)
	dir := filepath.Join(f.media, "lobby")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	evil := `<svg xmlns="http://www.w3.org/2000/svg"><script>fetch('/api/config')</script></svg>`
	if err := os.WriteFile(filepath.Join(dir, "evil.svg"), []byte(evil), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	check := func(t *testing.T, w http.Header, what string) {
		t.Helper()
		if got := w.Get("Content-Security-Policy"); got != MediaCSP {
			t.Errorf("%s: the policy is %q, want the sandbox policy", what, got)
		}
		if got := w.Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
			t.Errorf("%s: Cross-Origin-Resource-Policy = %q", what, got)
		}
		if got := w.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q", what, got)
		}
	}

	// 200: the whole file. The body is still the SVG, and the media type is still
	// the true one, because a slide that IS an SVG has to draw.
	w := f.do(http.MethodGet, "/media/lobby/evil.svg", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("the SVG gave %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "image/svg+xml") {
		t.Errorf("Content-Type = %q", w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), "<script>") {
		t.Error("the test file holds no script, so it proves nothing")
	}
	check(t, w.Header(), "the whole file")

	// 206: a Range request, which is how a browser seeks in a video.
	w = f.do(http.MethodGet, "/media/lobby/clip.mp4", nil, func(r *request) { r.rangeBytes = "bytes=0-3" })
	if w.Code != http.StatusPartialContent {
		t.Fatalf("the Range request gave %d", w.Code)
	}
	check(t, w.Header(), "a part of a file")

	// 304: the browser has the file already.
	w = f.do(http.MethodGet, "/media/lobby/evil.svg", nil, func(r *request) {
		r.headers = map[string]string{"If-Modified-Since": time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)}
	})
	if w.Code != http.StatusNotModified {
		t.Fatalf("the conditional request gave %d", w.Code)
	}
	check(t, w.Header(), "an answer with no body")
}

// The pages of this binary get a policy that permits only this device. A page of
// the admin UI holds no inline script, so 'self' is enough for the scripts, and a
// planted file can never become one of its sources.
func TestTheAppPolicyIsOnThePages(t *testing.T) {
	f := newFx(t)
	for _, target := range []string{"/", "/player"} {
		got := f.do(http.MethodGet, target, nil).Header().Get("Content-Security-Policy")
		if got != AppCSP {
			t.Errorf("%s: the policy is %q, want the application policy", target, got)
		}
	}
	for _, want := range []string{
		"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'",
		"base-uri 'none'", "media-src 'self'",
	} {
		if !strings.Contains(AppCSP, want) {
			t.Errorf("the application policy has no %q", want)
		}
	}
	// 'unsafe-eval' and a wildcard source would give a planted file a way back in.
	for _, never := range []string{"'unsafe-eval'", "*"} {
		if strings.Contains(AppCSP, never) {
			t.Errorf("the application policy holds %q", never)
		}
	}
}

// /api/status needs no session by design, so the route must say WHO is asking.
//
// The device itself (the fallback screen) and an admin with a session get the whole
// report. Anybody else on the LAN gets it with the secrets taken out
// (health.redact). The pairing code is narrower still: the device itself only (D46).
func TestStatusTellsTheRouteWhoIsAsking(t *testing.T) {
	f := newFx(t)

	// The device itself.
	w := f.do(http.MethodGet, "/api/status", nil, func(r *request) { r.remote = "127.0.0.1:52000" })
	body := w.Body.String()
	if !strings.Contains(body, "H7K2QX") {
		t.Errorf("the device itself did not get the pairing code: %s", body)
	}
	if !strings.Contains(body, "fleet.example.com") {
		t.Errorf("the device itself did not get the whole report: %s", body)
	}

	// A peer on the LAN with no session.
	w = f.do(http.MethodGet, "/api/status", nil, func(r *request) {
		r.remote = "192.168.1.55:41000"
		r.host = "lobby.local"
		r.noCookie = true
	})
	body = w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("the status route answered %d to a LAN peer", w.Code)
	}
	if strings.Contains(body, "H7K2QX") {
		t.Errorf("the pairing code went to the LAN: %s", body)
	}
	if strings.Contains(body, "fleet.example.com") {
		t.Errorf("a LAN peer got the whole report: %s", body)
	}

	// The same peer WITH an admin session gets the whole report again, because the
	// dashboard shows the change-me nags and the fleet fields.
	f.login()
	w = f.do(http.MethodGet, "/api/status", nil, func(r *request) {
		r.remote = "192.168.1.55:41000"
		r.host = "lobby.local"
	})
	body = w.Body.String()
	if !strings.Contains(body, "fleet.example.com") {
		t.Errorf("an admin with a session did not get the whole report: %s", body)
	}
	if strings.Contains(body, "H7K2QX") {
		t.Errorf("the pairing code went to a caller that is not the device: %s", body)
	}
}
