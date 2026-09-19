package version

import "runtime"

// Version is the release name of this build. The build sets it with
// -ldflags "-X github.com/ethanpil/portapixel/internal/version.Version=1.2.3".
// A development build keeps the value "dev".
var Version = "dev"

// PublicKey is the minisign public key that signs the releases (D47).
//
// The value is empty. CI release #1 puts the true key here and then keeps the
// same key for every later release. Signature checks fail while the value is
// empty, so an unsigned release can never pass the check by accident.
const PublicKey = ""

// Arch gives the release name of the CPU architecture. The releases use the Go
// names "amd64" and "arm64". PortaPixel builds no other architecture (D8).
func Arch() string {
	return runtime.GOARCH
}
