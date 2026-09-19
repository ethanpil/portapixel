// Package admin holds the routes of the admin UI, /api/admin, and the session.
//
// Why this package exists: the browser side of the server needs guards that a
// device does not need, and it changes with the UI while the device API must not
// change at all. So the two live apart.
//
// The guards come from internal/httpguard and are the guards of D46:
//
//   - A Host header allowlist, made from the public URL, the listen address and
//     the loopback names. It stops DNS rebinding.
//   - A session cookie with SameSite=Strict, and the X-PortaPixel header on
//     every request that changes something. Together they stop cross-site
//     request forgery.
//   - A limiter on the login route, so a password guess attack is slow.
//
// This package holds routes only. Each rule about the data lives in
// internal/server/db, the media store or the release mirror.
package admin
