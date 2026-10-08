// Package httpd serves the web UI, the media files and the JSON API of the device.
//
// Why this package exists: every route of the device is in one place, so that the
// hardening rules are applied once and cannot be forgotten on one handler
// (ARCHITECTURE section 6). The package holds routes and nothing else. A handler
// reads the request, calls one function of Deps, and writes JSON. When a handler
// needs a rule, the rule belongs in the package that owns the data.
//
// The three hardening rules, all from D46:
//
//   - The Host header must be in the allowlist. This stops DNS rebinding: a name
//     that points at the device but that we do not know gets 421.
//   - Every request that is not GET or HEAD must carry "X-PortaPixel: 1". A form
//     on another site cannot add a header, so this stops request forgery.
//   - /api/status needs no session, because the login page shows it, but the
//     pairing code in it goes to the device itself only.
//
// Every answer is JSON, including every error. The admin UI reads {error} and
// {error, fields} and never has to parse an HTML error page.
package httpd
