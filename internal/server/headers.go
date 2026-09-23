package server

import (
	"net/http"
	"strings"

	"github.com/ethanpil/portapixel/internal/server/api"
)

// The headers below are the browser rules of the whole server. They are here, in
// one middleware of the one route stack, and not in the handlers: a handler that
// somebody adds next year cannot forget them, and a test that removes this
// middleware fails.
//
// The device end of the product sets the same values, so that one answer holds for
// both (ARCHITECTURE section 6).
const (
	// noSniff stops a browser from guessing a media type. A stored object whose
	// bytes look like HTML must never be treated as HTML.
	noSniff = "nosniff"
	// referrerPolicy keeps our addresses out of the Referer header of an outside
	// page. An admin URL holds device IDs and object hashes.
	referrerPolicy = "no-referrer"
	// frameOptions stops a page of another site from putting ours in a frame. The
	// CSP below says the same thing for a browser that reads it; this header is for
	// the one that does not.
	frameOptions = "DENY"
	// hsts is one year. It goes out only over TLS: on plain HTTP it would tell the
	// browser to refuse the only scheme that this server answers.
	hsts = "max-age=31536000"
)

// uiCSP is the policy of the admin UI and of its API.
//
// Every script, style and font of the UI comes from this origin (there is no CDN
// and no Google Fonts, ARCHITECTURE section 8). 'unsafe-inline' is for style only,
// because index.html carries one style attribute and the components set element
// styles; no script is inline, so script-src stays strict. data: and blob: are for
// img-src, because util.js makes a download link from a Blob.
const uiCSP = "default-src 'self'; " +
	"img-src 'self' data: blob:; " +
	"media-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"script-src 'self'; " +
	"connect-src 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'"

// objectCSP makes a stored object inert.
//
// A person uploads the bytes, and internal/server/media gives an SVG the type
// image/svg+xml, which a browser runs scripts from. An admin who opens such an
// object on this origin would run that script with the admin session. "sandbox"
// with no token puts the answer in an opaque origin with no scripts, so the script
// cannot run and it could reach nothing if it did. An SVG in an <img> tag still
// draws: a policy in a response does not apply to an image load.
const objectCSP = "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:"

// secureHeaders adds the browser rules to every answer.
//
// https reports if the caller reached us over TLS, directly or through a proxy that
// we trust. Only that answer takes the strict transport header.
func secureHeaders(https func(*http.Request) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", noSniff)
		h.Set("Referrer-Policy", referrerPolicy)
		h.Set("X-Frame-Options", frameOptions)
		if https != nil && https(r) {
			h.Set("Strict-Transport-Security", hsts)
		}
		switch {
		case isObjectPath(r.URL.Path):
			h.Set("Content-Security-Policy", objectCSP)
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
		case !strings.HasPrefix(r.URL.Path, "/api/v1/"):
			// The browser side: the admin UI, its assets and its API.
			h.Set("Content-Security-Policy", uiCSP)
		}
		next.ServeHTTP(w, r)
	})
}

// isObjectPath reports if a path serves bytes that a person uploaded or that came
// from a release archive.
//
// The four routes are named here and not in their handlers, so that one reader can
// see the whole list of paths that serve foreign bytes.
func isObjectPath(path string) bool {
	switch {
	case strings.HasPrefix(path, api.MediaBase), strings.HasPrefix(path, api.ReleaseBase):
		return true
	case strings.HasPrefix(path, "/api/admin/media/") &&
		(strings.HasSuffix(path, "/thumb") || strings.HasSuffix(path, "/file")):
		return true
	}
	return false
}
