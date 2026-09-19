package sigverify

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"aead.dev/minisign"
)

const content = "a release binary"

type fixture struct {
	dir     string
	path    string
	sigPath string
	key     string // public key text
}

// newFixture makes a key pair, a file, and a signature of that file. When
// hashed is true it makes the prehashed signature, which is what the minisign
// command line tool makes by default.
func newFixture(t *testing.T, hashed bool) fixture {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyText, err := pub.MarshalText()
	if err != nil {
		t.Fatal(err)
	}

	f := fixture{dir: t.TempDir(), key: string(keyText)}
	f.path = filepath.Join(f.dir, "portapixeld")
	f.sigPath = f.path + ".minisig"
	if err := os.WriteFile(f.path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var sig []byte
	if hashed {
		r := minisign.NewReader(bytes.NewReader([]byte(content)))
		if _, err := io.Copy(io.Discard, r); err != nil {
			t.Fatal(err)
		}
		sig = r.Sign(priv)
	} else {
		sig = minisign.Sign(priv, []byte(content))
	}
	if err := os.WriteFile(f.sigPath, sig, 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func otherKeyText(t *testing.T) string {
	t.Helper()
	pub, _, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	text, err := pub.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

func TestVerifyFile(t *testing.T) {
	tests := []struct {
		name string
		// change gives the arguments of the call. It can also change the files.
		change  func(t *testing.T, f *fixture)
		wantErr bool
	}{
		{
			name:   "a good signature passes",
			change: func(t *testing.T, f *fixture) {},
		},
		{
			name: "a changed file fails",
			change: func(t *testing.T, f *fixture) {
				write(t, f.path, "a different binary")
			},
			wantErr: true,
		},
		{
			name: "one more byte fails",
			change: func(t *testing.T, f *fixture) {
				write(t, f.path, content+"x")
			},
			wantErr: true,
		},
		{
			name: "an empty file fails",
			change: func(t *testing.T, f *fixture) {
				write(t, f.path, "")
			},
			wantErr: true,
		},
		{
			name: "the wrong key fails",
			change: func(t *testing.T, f *fixture) {
				f.key = otherKeyText(t)
			},
			wantErr: true,
		},
		{
			name: "an empty key fails",
			change: func(t *testing.T, f *fixture) {
				f.key = ""
			},
			wantErr: true,
		},
		{
			name: "a key that is not a key fails",
			change: func(t *testing.T, f *fixture) {
				f.key = "not-a-key"
			},
			wantErr: true,
		},
		{
			name: "a damaged signature file fails",
			change: func(t *testing.T, f *fixture) {
				write(t, f.sigPath, "junk\njunk\n")
			},
			wantErr: true,
		},
		{
			name: "a missing signature file fails",
			change: func(t *testing.T, f *fixture) {
				f.sigPath = filepath.Join(f.dir, "missing.minisig")
			},
			wantErr: true,
		},
		{
			name: "a missing file fails",
			change: func(t *testing.T, f *fixture) {
				f.path = filepath.Join(f.dir, "missing")
			},
			wantErr: true,
		},
	}

	for _, hashed := range []bool{false, true} {
		name := "legacy signature"
		if hashed {
			name = "prehashed signature"
		}
		t.Run(name, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					f := newFixture(t, hashed)
					tt.change(t, &f)
					err := VerifyFile(f.path, f.sigPath, f.key)
					// A legacy signature never passes, whatever else is right.
					wantErr := tt.wantErr || !hashed
					if wantErr && err == nil {
						t.Fatal("want an error, got nil")
					}
					if !wantErr && err != nil {
						t.Fatalf("want no error, got %v", err)
					}
				})
			}
		})
	}
}

// TestVerifyFileRefusesALegacySignature makes sure the refusal is a refusal of
// the algorithm and not an accident of a broken fixture. A legacy signature is
// of the file itself, so the check would read a whole release binary into the
// memory of a 512 MB device on a path that the unchecked .minisig chooses.
func TestVerifyFileRefusesALegacySignature(t *testing.T) {
	f := newFixture(t, false)
	err := VerifyFile(f.path, f.sigPath, f.key)
	if !errors.Is(err, ErrLegacySignature) {
		t.Fatalf("a good legacy signature gave %v, want ErrLegacySignature", err)
	}

	// The prehashed signature of the same file still passes.
	g := newFixture(t, true)
	if err := VerifyFile(g.path, g.sigPath, g.key); err != nil {
		t.Fatalf("a prehashed signature must pass: %v", err)
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
