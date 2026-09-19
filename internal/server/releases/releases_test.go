package releases

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aead.dev/minisign"
)

// signer holds a test key pair. The release key of a build is empty
// (internal/version.PublicKey), so a test has to bring its own. Mirror.PublicKey
// is a field for this reason.
type signer struct {
	public  string
	private minisign.PrivateKey
}

func newSigner(t *testing.T) signer {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	text, err := pub.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	return signer{public: string(text), private: priv}
}

// sign gives the prehashed signature of data, which is what the release process
// makes and what sigverify accepts.
func (s signer) sign(t *testing.T, data []byte) []byte {
	t.Helper()
	r := minisign.NewReader(bytes.NewReader(data))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	return r.Sign(s.private)
}

// releaseFiles makes the five files of a release: two binaries, two signatures
// and the checksum file.
func (s signer) releaseFiles(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	var sums strings.Builder
	for _, name := range binaries {
		body := []byte("this is " + name + " of the test release")
		out[name] = body
		out[name+".minisig"] = s.sign(t, body)
		sum := sha256.Sum256(body)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	out[SumsName] = []byte(sums.String())
	return out
}

// fakeGitHub answers the release list and serves the files.
func fakeGitHub(t *testing.T, version string, files map[string][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var base string

	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/download/")
		body, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		var assets []string
		for name := range files {
			assets = append(assets, fmt.Sprintf(`{"name":%q,"browser_download_url":%q}`,
				name, base+"/download/"+name))
		}
		fmt.Fprintf(w, `[{"tag_name":%q,"name":"test","body":"the notes",`+
			`"draft":false,"prerelease":false,"published_at":"2026-08-04T10:00:00Z",`+
			`"assets":[%s]}]`, version, strings.Join(assets, ","))
	})

	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

// newMirror makes a Mirror that talks to a fake GitHub.
func newMirror(t *testing.T, s signer, version string, files map[string][]byte) (*Mirror, []string) {
	t.Helper()
	srv := fakeGitHub(t, version, files)

	var reports []string
	m := NewMirror(t.TempDir(), "owner/name")
	m.PublicKey = s.public
	m.Lister.BaseURL = srv.URL
	m.Report = func(v, state, errText string) {
		reports = append(reports, state)
		if errText != "" {
			t.Logf("report %s %s: %s", v, state, errText)
		}
	}
	return m, reports
}

func TestMirrorOfAGoodRelease(t *testing.T) {
	s := newSigner(t)
	files := s.releaseFiles(t)
	m, _ := newMirror(t, s, "1.5.0", files)

	if err := m.Run(context.Background(), "1.5.0"); err != nil {
		t.Fatalf("the mirror failed: %v", err)
	}
	dir := m.VersionDir("1.5.0")
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s is not in the mirror: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s in the mirror is not the file of the release", name)
		}
	}
	// No part file is left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".part") {
			t.Fatalf("the mirror holds the part file %s", e.Name())
		}
	}
}

func TestMirrorRefusesAWrongSignature(t *testing.T) {
	s := newSigner(t)
	other := newSigner(t)
	files := s.releaseFiles(t)
	// A signature of the right file by the wrong key.
	files[binaries[0]+".minisig"] = other.sign(t, files[binaries[0]])

	m, _ := newMirror(t, s, "1.5.0", files)
	err := m.Run(context.Background(), "1.5.0")
	if err == nil {
		t.Fatal("a release with a wrong signature was mirrored")
	}
	if !strings.Contains(err.Error(), binaries[0]) {
		t.Fatalf("the error does not name the file: %v", err)
	}
}

func TestMirrorRefusesAWrongChecksum(t *testing.T) {
	s := newSigner(t)
	files := s.releaseFiles(t)
	// The signature is right, the checksum file is wrong. Both checks must pass.
	files[SumsName] = []byte(strings.Repeat("0", 64) + "  " + binaries[0] + "\n" +
		strings.Repeat("0", 64) + "  " + binaries[1] + "\n")

	m, _ := newMirror(t, s, "1.5.0", files)
	if err := m.Run(context.Background(), "1.5.0"); err == nil {
		t.Fatal("a release with a wrong checksum was mirrored")
	}
}

func TestMirrorRefusesAMissingFile(t *testing.T) {
	s := newSigner(t)
	files := s.releaseFiles(t)
	delete(files, binaries[1])

	m, _ := newMirror(t, s, "1.5.0", files)
	err := m.Run(context.Background(), "1.5.0")
	if err == nil || !strings.Contains(err.Error(), binaries[1]) {
		t.Fatalf("the mirror gave %v, want a complaint about %s", err, binaries[1])
	}
}

func TestMirrorWithNoReleaseKey(t *testing.T) {
	s := newSigner(t)
	m, _ := newMirror(t, s, "1.5.0", s.releaseFiles(t))
	// A development build has no key (internal/version.PublicKey is empty).
	m.PublicKey = ""

	err := m.Run(context.Background(), "1.5.0")
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("the mirror gave %v, want ErrNoKey", err)
	}
}

func TestMirrorReportsItsStates(t *testing.T) {
	s := newSigner(t)
	var states []string
	m, _ := newMirror(t, s, "1.5.0", s.releaseFiles(t))
	m.Report = func(v, state, errText string) { states = append(states, state) }

	if err := m.Run(context.Background(), "1.5.0"); err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0] != stateWorking || states[1] != stateDone {
		t.Fatalf("the states are %v", states)
	}
}

func TestValidVersion(t *testing.T) {
	good := []string{"1.5.0", "v1.5.0", "1.5.0-rc1", "2026.09.18", "a_b"}
	bad := []string{"", "..", ".", "../etc", "1.5.0/x", `1.5.0\x`, "1 5 0", "a;b", strings.Repeat("9", 65)}
	for _, v := range good {
		if !ValidVersion(v) {
			t.Errorf("%q was refused", v)
		}
	}
	for _, v := range bad {
		if ValidVersion(v) {
			t.Errorf("%q was accepted", v)
		}
	}
}

func TestIsMirrorFile(t *testing.T) {
	for _, name := range Files() {
		if !IsMirrorFile(name) {
			t.Errorf("%q is not a mirror file", name)
		}
	}
	for _, name := range []string{"", "server.toml", "../server.toml", "portapixeld", "SHA256SUMS.bak"} {
		if IsMirrorFile(name) {
			t.Errorf("%q was accepted as a mirror file", name)
		}
	}
}

// bundle makes a .tar.gz of the given members.
func tarGz(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range members {
		head := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(head); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// zipOf makes a .zip of the given members.
func zipOf(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range members {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	return buf.Bytes()
}

func TestBundleTarGz(t *testing.T) {
	s := newSigner(t)
	files := s.releaseFiles(t)
	m := NewMirror(t.TempDir(), "owner/name")
	m.PublicKey = s.public
	m.Report = func(string, string, string) {}

	// A bundle with a directory in front of the names and one extra file.
	members := map[string][]byte{"portapixel-1.5.0/README.md": []byte("read me")}
	for name, body := range files {
		members["portapixel-1.5.0/"+name] = body
	}

	if err := m.InstallBundle("1.5.0", bytes.NewReader(tarGz(t, members)), "bundle.tar.gz"); err != nil {
		t.Fatalf("a good bundle was refused: %v", err)
	}
	for name := range files {
		if _, err := os.Stat(filepath.Join(m.VersionDir("1.5.0"), name)); err != nil {
			t.Fatalf("%s is not in the mirror: %v", name, err)
		}
	}
	// The extra file did not land: only the files of a release are extracted.
	if _, err := os.Stat(filepath.Join(m.VersionDir("1.5.0"), "README.md")); err == nil {
		t.Fatal("a file that is not part of a release was extracted")
	}
}

func TestBundleZip(t *testing.T) {
	s := newSigner(t)
	m := NewMirror(t.TempDir(), "owner/name")
	m.PublicKey = s.public
	m.Report = func(string, string, string) {}

	if err := m.InstallBundle("1.5.0", bytes.NewReader(zipOf(t, s.releaseFiles(t))), "b.zip"); err != nil {
		t.Fatalf("a good zip bundle was refused: %v", err)
	}
}

func TestBundleRefusesTraversal(t *testing.T) {
	s := newSigner(t)
	outside := filepath.Join(t.TempDir(), "stolen")

	cases := []struct {
		name   string
		member string
	}{
		{"a parent step", "../../" + binaries[0]},
		{"a deeper parent step", "a/b/../../../" + SumsName},
		{"an absolute path", "/etc/" + SumsName},
		{"a windows path", `..\..\` + SumsName},
		{"a drive letter", `C:\windows\` + SumsName},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := NewMirror(t.TempDir(), "owner/name")
			m.PublicKey = s.public
			m.Report = func(string, string, string) {}

			members := map[string][]byte{c.member: []byte("evil")}
			for _, kind := range []struct {
				file string
				body []byte
			}{
				{"b.tar.gz", tarGz(t, members)},
				{"b.zip", zipOf(t, members)},
			} {
				err := m.InstallBundle("1.5.0", bytes.NewReader(kind.body), kind.file)
				if !errors.Is(err, ErrBadBundle) {
					t.Fatalf("%s gave %v, want ErrBadBundle", kind.file, err)
				}
				if _, statErr := os.Stat(outside); statErr == nil {
					t.Fatalf("%s wrote a file outside the mirror", kind.file)
				}
			}
		})
	}
}

func TestBundleRefusesAnIncompleteArchive(t *testing.T) {
	s := newSigner(t)
	files := s.releaseFiles(t)
	delete(files, SumsName)

	m := NewMirror(t.TempDir(), "owner/name")
	m.PublicKey = s.public
	m.Report = func(string, string, string) {}

	err := m.InstallBundle("1.5.0", bytes.NewReader(tarGz(t, files)), "b.tar.gz")
	if !errors.Is(err, ErrBadBundle) {
		t.Fatalf("the bundle gave %v, want ErrBadBundle", err)
	}
}

func TestBundleRefusesAnUnknownFormat(t *testing.T) {
	m := NewMirror(t.TempDir(), "owner/name")
	m.PublicKey = "not empty"
	m.Report = func(string, string, string) {}

	err := m.InstallBundle("1.5.0", strings.NewReader("hello"), "release.rar")
	if !errors.Is(err, ErrBadBundle) {
		t.Fatalf("the bundle gave %v, want ErrBadBundle", err)
	}
}

func TestBundleRefusesALinkWithAReleaseName(t *testing.T) {
	s := newSigner(t)
	m := NewMirror(t.TempDir(), "owner/name")
	m.PublicKey = s.public
	m.Report = func(string, string, string) {}

	// A symbolic link named like a release file would make the checks read a file
	// of the host.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	head := &tar.Header{Name: SumsName, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777}
	if err := tw.WriteHeader(head); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()

	err := m.InstallBundle("1.5.0", bytes.NewReader(buf.Bytes()), "b.tar.gz")
	if !errors.Is(err, ErrBadBundle) {
		t.Fatalf("the bundle gave %v, want ErrBadBundle", err)
	}
}

func TestListerCachesAndToleratesFailure(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 1 {
			// The second real request fails. The cache must still answer.
			http.Error(w, "rate limited", http.StatusForbidden)
			return
		}
		fmt.Fprint(w, `[{"tag_name":"1.5.0","body":"notes","published_at":"2026-08-04T10:00:00Z","assets":[]},`+
			`{"tag_name":"draft","draft":true,"assets":[]}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	l := NewLister("owner/name")
	l.BaseURL = srv.URL

	list, errText := l.List(context.Background(), false)
	if errText != "" {
		t.Fatalf("the first list gave %q", errText)
	}
	if len(list) != 1 || list[0].Version != "1.5.0" || list[0].Notes != "notes" {
		t.Fatalf("the list is %+v", list)
	}

	// A second call inside the cache life asks nothing.
	if _, errText := l.List(context.Background(), false); errText != "" {
		t.Fatalf("the cached list gave %q", errText)
	}
	if calls != 1 {
		t.Fatalf("the lister asked %d times, want 1", calls)
	}

	// A forced call fails, and the old list is still there.
	list, errText = l.List(context.Background(), true)
	if errText == "" {
		t.Fatal("the failed list reported no error")
	}
	if len(list) != 1 || list[0].Version != "1.5.0" {
		t.Fatalf("the failed list lost the cache: %+v", list)
	}
}
