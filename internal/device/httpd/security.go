package httpd

import (
	"net/http"
	"strings"
)

// The security headers of every answer of the daemon.
//
// Why they are here and not only on the admin UI: the daemon serves the admin UI,
// the player and the content of a removable medium from ONE origin, on plain HTTP
// (D22). A file that a person copied onto the card is therefore same-origin with
// the admin session and with the player secret.
const (
	// AppCSP is the policy of the pages that this binary holds: the admin UI and
	// the player. Everything comes from this device.
	//
	// "img-src" adds data: and blob: for the QR code of the fallback screen and for
	// a preview that the admin UI builds. "style-src" adds 'unsafe-inline' because
	// index.html carries one style attribute and the player sets element styles for
	// its transitions. "script-src 'self'" holds: both pages load one module file
	// and neither has an inline script, an event attribute or eval.
	AppCSP = "default-src 'self'; " +
		"img-src 'self' data: blob:; " +
		"media-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"script-src 'self'; " +
		"connect-src 'self'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'none'; " +
		"form-action 'self'"

	// MediaCSP is the policy of /media/, which serves files that a person wrote onto
	// the card or that a fleet server sent.
	//
	// "sandbox" with no keyword gives the answer an opaque origin and no scripts. An
	// SVG with a <script> in it that somebody OPENS is then inert: it cannot read the
	// admin session, the configuration or the player secret. The same file still
	// draws in an <img> tag and in a CSS background, because a browser does not apply
	// the policy of an answer that it loads as a picture.
	//
	// Before this the media type of an SVG made the file a document of this origin.
	// Anybody who can write the card, and any person with the right to upload media,
	// could plant one.
	MediaCSP = "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:"
)

// mediaPrefix is the one route that serves content from outside this binary.
const mediaPrefix = "/media/"

// securityHeaders puts the headers on every answer. It is the outermost wrapper,
// so a refusal of the Host allowlist or of the header check carries them as well.
//
// "nosniff" stops a browser from guessing a type for a file whose extension this
// device does not know. "no-referrer" stops the player URL, which carries the
// player secret in ?k=, from reaching an external page in a Referer header.
// "DENY" plus "frame-ancestors 'none'" stops a page on another site from putting
// the admin UI in a frame and stealing a click.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, mediaPrefix) {
			h.Set("Content-Security-Policy", MediaCSP)
			// The file belongs to this device. Another site must not read it, and a
			// browser must not put it in a shared process with one.
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
		} else {
			h.Set("Content-Security-Policy", AppCSP)
		}
		next.ServeHTTP(w, r)
	})
}
