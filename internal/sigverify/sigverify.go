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

// VerifyFile checks the file at path against the signature at sigPath and the
// given minisign public key. It gives nil only when the signature is valid.
//
// An empty publicKey is an error. A build with no key must not be able to
// install a release (see internal/version.PublicKey).
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

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if sig.Algorithm == minisign.HashEdDSA {
		// The signature is of the hash of the file. Read the file through the
		// minisign reader, which hashes it. A release binary is large, so this
		// path must not hold the whole file in memory.
		r := minisign.NewReader(f)
		if _, err := io.Copy(io.Discard, r); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if !r.Verify(key, sigText) {
			return ErrBadSignature
		}
		return nil
	}

	// A legacy signature is of the file itself.
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if !minisign.Verify(key, data, sigText) {
		return ErrBadSignature
	}
	return nil
}
