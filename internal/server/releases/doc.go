// Package releases lists the releases of the project and mirrors the approved
// one for the devices.
//
// Why this package exists: a device of a fleet installs only the version that
// the admin approved, and it gets that version from the server (D28). So the
// server has to fetch the release files, check them, and serve them. The check
// is the part that must not live in a route: each binary is verified against its
// minisign signature with the key in the build, and against the SHA256SUMS file
// of the release. A release is marked as mirrored only when every file passes.
//
// The mirror is a cache and not a source of trust. The device verifies the
// signature again before it swaps a release (D47), so a server that somebody
// broke into cannot make a device run a binary that the project did not sign.
//
// For a closed network the admin uploads a bundle of the same files. The bundle
// path does the identical check, and it extracts with strict name rules: a path
// in an archive is input from outside and must never write outside the release
// directory.
package releases
