// Package httpguard holds the HTTP defences of the local API (D46).
//
// Why this package exists: the device UI has no HTTPS in v1, and the browser on
// the device shows pages from the internet. Three attacks are therefore real:
// DNS rebinding, which the Host allowlist stops; cross-site request forgery,
// which the SameSite cookie and the necessary custom header stop; and password
// guessing, which the login limiter slows down. The fleet server needs the same
// defences, so both programs use this one package and the tests are release
// gates.
package httpguard
