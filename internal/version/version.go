package version

import (
	"runtime"
	"strings"
)

// Version is the release name of this build. The build sets it with
// -ldflags "-X github.com/ethanpil/portapixel/internal/version.Version=1.2.3".
// A development build keeps the value "dev".
//
// The value is always the normalized name: the init below strips the letter that
// some projects put in front of a tag. Read this variable and nothing else.
var Version = "dev"

// init makes the build name normal before any other package reads it.
//
// -ldflags writes this variable before init runs, so a build that carries the tag
// "v1.5.0" holds "1.5.0" from here on. Why it happens at the source and not at
// each reader: the daemon names the health marker after this value and the
// updater names the release directory after it. A reader that forgets to
// normalize makes the two names differ, and the gate then rolls a good release
// back and bans it for ever (see Normalize).
func init() {
	Version = Normalize(Version)
}

// Normalize gives the release name that every part of the project uses in a
// directory name, in the pending marker and in the health marker.
//
// A tag can carry the letter that some projects put in front: the tag "v1.5.0"
// builds a release that is "1.5.0". Both names must become one name here.
// Without that the release installs as releases/v1.5.0 and the gate waits for
// health/v1.5.0.ok, while the daemon writes health/1.5.0.ok. The gate then rolls
// a good release back and bans it for ever.
func Normalize(name string) string {
	text := strings.TrimSpace(name)
	if len(text) > 1 && (text[0] == 'v' || text[0] == 'V') && text[1] >= '0' && text[1] <= '9' {
		return text[1:]
	}
	return text
}

// PublicKey is the minisign public key that signs the releases (D47).
//
// The default value is empty, and every signature check fails while it is empty,
// so an unsigned release can never pass the check by accident. The release
// workflow sets the true key with
// -ldflags "-X github.com/ethanpil/portapixel/internal/version.PublicKey=<key>"
// and then keeps the same key for every later release.
//
// It is a var and not a const for that reason: -X cannot write a const, so
// nobody could build a test binary that installs a signed release.
var PublicKey = ""

// Arch gives the release name of the CPU architecture. The releases use the Go
// names "amd64" and "arm64". PortaPixel builds no other architecture (D8).
func Arch() string {
	return runtime.GOARCH
}
