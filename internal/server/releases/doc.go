// Package releases lists the releases of the project and mirrors the approved
// one for the devices.
//
// Why this package exists: a device of a fleet installs only the version that the
// admin approved, and it gets that version from the server (D28). So the server
// fetches the release files, checks them, and serves them.
//
// The check must not live in a route. This package verifies each binary two ways.
// It verifies the minisign signature against the key of the build. It also
// verifies the checksum against the SHA256SUMS file of the release. The mirror
// state says "done" only after every file passes.
//
// The mirror is a cache and not a source of trust. The device verifies the
// signature again before it swaps a release (D47), so a server that somebody
// broke into cannot make a device run a binary that the project did not sign.
//
// For a closed network the admin uploads a bundle of the same files. The bundle
// path does the identical check. It also extracts with strict name rules. A path
// in an archive is input from outside, and it must never write outside the release
// directory.
package releases
