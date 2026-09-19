// Package httpjson holds the JSON answer shapes of the two route packages of the
// server and the rules about the caller address.
//
// Why this package exists: internal/server/api and internal/server/admin both
// answer web/shared/api.js. The shapes {"error": "..."} and
// {"error": "...", "fields": [...]} are the contract with that one client, and
// each package used to hold its own copy of the four helpers. Two copies of one
// contract drift apart, and then the client needs two paths for one error.
//
// It also holds ClientIP. Every per-address guard of the server reads it: the
// enroll limiter, the login limiter and the last_ip column of a device row. One
// rule decides what "the address of the caller" is, so a reverse proxy cannot
// make the three disagree.
//
// The package is a leaf. It imports internal/server/db for the field error type
// and nothing else of the server, so neither route package makes a cycle.
package httpjson
