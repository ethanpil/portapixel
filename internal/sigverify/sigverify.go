package sigverify

import (
	"errors"
	"fmt"
	"io"
	"os"

	"aead.dev/minisign"
)

// ErrBadSignature says that the file does not match the signature and the key.
var ErrBadSignature = errors.New("the signature of the file is not valid")

// ErrLegacySignature says that the signature is of the file itself and not of
// the hash of the file. PortaPixel refuses it: see VerifyFile.
var ErrLegacySignature = errors.New("the signature is a legacy signature; PortaPixel needs a prehashed signature")

// VerifyFile checks the file at path against the signature at sigPath and the
// given minisign public key. It gives nil only when the signature is valid.
//
// An empty publicKey is an error. A build with no key must not be able to
// install a release (see internal/version.PublicKey).
//
// The signature must be a prehashed signature, which is what the minisign tool
// makes by default and what the release process always makes. A legacy
// signature is of the file itself, so a check would have to hold the whole
// release binary in memory, and the .minisig file — which nobody has checked
// yet — is what chooses the path. A device has 512 MB, so VerifyFile refuses
// that path instead.
func VerifyFile(path, sigPath, publicKey string) error {
	if publicKey == "" {
		return errors.New("no minisign public key is in this build")
	}
	var key minisign.PublicKey
	if err := key.UnmarshalText([]byte(publicKey)); err != nil {
		return fmt.Errorf("bad minisign public key: %w", err)
	}

	sigText, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("read the signature %s: %w", sigPath, err)
	}
	var sig minisign.Signature
	if err := sig.UnmarshalText(sigText); err != nil {
		return fmt.Errorf("bad signature file %s: %w", sigPath, err)
	}
	// Refuse the legacy algorithm before the file is opened.
	if sig.Algorithm != minisign.HashEdDSA {
		return ErrLegacySignature
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	// The signature is of the hash of the file. Read the file through the
	// minisign reader, which hashes it. A release binary is large, so this path
	// must not hold the whole file in memory.
	r := minisign.NewReader(f)
	if _, err := io.Copy(io.Discard, r); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if !r.Verify(key, sigText) {
		return ErrBadSignature
	}
	return nil
}
