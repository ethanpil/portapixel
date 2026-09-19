// Package version holds the build identity of the binaries.
//
// Why this package exists: the version string and the minisign public key must
// come from one place. The build sets the version with -ldflags. The updater
// and the About page read the same values, so the two can never disagree.
package version
