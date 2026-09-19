package updater

import (
	"archive/tar"
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

// ---------------------------------------------------------------- the fixture

// signer holds a key pair of a test. The release gate of plan section 18 needs a
// real signature and a real key, so nothing here is a stub of minisign.
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

// sign makes the prehashed signature that the release process always makes.
func (s signer) sign(t *testing.T, body []byte) []byte {
	t.Helper()
	r := minisign.NewReader(bytes.NewReader(body))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	return r.Sign(s.private)
}

// world is a release root, a manager and everything that a test changes.
type world struct {
	t        *testing.T
	root     string
	media    string
	sign     signer
	man      *Manager
	bad      map[string]bool
	restarts int
}

const (
	testArch    = "arm64"
	testBinary  = "portapixeld"
	testAsset   = testBinary + "-" + testArch
	testRunning = "1.4.0"
)

// newWorld makes a release root that looks like a device: releases/1.4.0 with the
// running binary in it, current pointing at it, and an empty health directory.
func newWorld(t *testing.T, change func(*Options)) *world {
	t.Helper()
	w := &world{t: t, root: t.TempDir(), media: t.TempDir(), sign: newSigner(t), bad: map[string]bool{}}

	mustMkdir(t, filepath.Join(w.root, ReleasesDir, testRunning))
	mustMkdir(t, filepath.Join(w.root, HealthDir))
	mustWrite(t, filepath.Join(w.root, ReleasesDir, testRunning, testBinary), []byte("the old binary"))
	mustMkdir(t, filepath.Join(w.media, "_update"))

	opt := Options{
		Root:           w.root,
		BinaryName:     testBinary,
		Arch:           testArch,
		Running:        testRunning,
		PublicKey:      w.sign.public,
		SideloadDir:    filepath.Join(w.media, "_update"),
		IsBadRelease:   func(v string) bool { return w.bad[v] },
		MarkBadRelease: func(v string) { w.bad[v] = true },
		Restart:        func() error { w.restarts++; return nil },
		Flip:           fileFlip,
		BinaryVersion:  func(string) (string, error) { return "1.5.0", nil },
		FreeBytes:      func(string) (uint64, error) { return 1 << 30, nil },
		Log:            func(string, string) {},
	}
	if change != nil {
		change(&opt)
	}
	w.man = New(opt)
	// The starting state of the links, in the shape that fileFlip writes.
	if err := fileFlip(filepath.Join(w.root, CurrentLink), ReleasesDir+"/"+testRunning); err != nil {
		t.Fatal(err)
	}
	return w
}

// fileFlip stands in for FlipSymlink. A Windows developer machine cannot make a
// symlink without rights that nobody has by default, so the tests give the
// manager a Flip that writes the target into a small file. Every other step is the
// same code as the device runs, and TestFlipSymlink covers the real one where the
// system permits it.
func fileFlip(link, target string) error {
	return os.WriteFile(link, []byte(target+"\n"), 0o644)
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// bundle is the three files of a release, as bytes.
type bundle struct {
	binary []byte
	sig    []byte
	sums   []byte
}

// newBundle makes a good bundle. The two switches make the two bundles that the
// release gate must refuse.
func (w *world) newBundle(body string, unsigned, wrongSum bool) bundle {
	binary := []byte(body)
	out := bundle{binary: binary}
	if !unsigned {
		out.sig = w.sign.sign(w.t, binary)
	} else {
		// A signature of other bytes, made with another key. This is what an
		// unsigned or a wrongly signed release looks like on the wire.
		other := newSigner(w.t)
		out.sig = other.sign(w.t, binary)
	}
	sum := sha(binary)
	if wrongSum {
		sum = strings.Repeat("0", 64)
	}
	out.sums = []byte(sum + "  " + testAsset + "\n")
	return out
}

// writeDir puts a bundle in a directory.
func (b bundle) writeDir(t *testing.T, dir string) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, testAsset), b.binary)
	mustWrite(t, filepath.Join(dir, testAsset+SigSuffix), b.sig)
	mustWrite(t, filepath.Join(dir, SumsName), b.sums)
}

// serve gives an HTTP server that answers the three files, and the release that
// names them.
func (b bundle) serve(t *testing.T, version string) (*httptest.Server, Release) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+testAsset, func(w http.ResponseWriter, r *http.Request) { w.Write(b.binary) })
	mux.HandleFunc("/"+testAsset+SigSuffix, func(w http.ResponseWriter, r *http.Request) { w.Write(b.sig) })
	mux.HandleFunc("/"+SumsName, func(w http.ResponseWriter, r *http.Request) { w.Write(b.sums) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server, Release{
		Version:   version,
		Source:    "github",
		BinaryURL: server.URL + "/" + testAsset,
		SigURL:    server.URL + "/" + testAsset + SigSuffix,
		SumsURL:   server.URL + "/" + SumsName,
	}
}

// installed reads the binary of a release directory.
func (w *world) installed(version string) (string, error) {
	data, err := os.ReadFile(filepath.Join(w.root, ReleasesDir, version, testBinary))
	return string(data), err
}

// current gives the target of the current link.
func (w *world) current() string { return w.man.linkTarget(CurrentLink) }

// pending gives the version in the pending marker, or "".
func (w *world) pending() string {
	data, err := os.ReadFile(filepath.Join(w.root, PendingFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ------------------------------------------------------------------ the tests

// The release gate of plan section 18: an unsigned release, a wrongly signed
// release and a release whose checksum does not match must all be refused BEFORE
// the symlink flip. A device that flipped first and checked second is a device that
// a bad file can take away.
func TestApplyRefusesBeforeTheFlip(t *testing.T) {
	tests := []struct {
		name     string
		unsigned bool
		wrongSum bool
		version  string
		badList  string
		key      string
		free     uint64
		want     error
		wantText string
	}{
		{name: "a good release", version: "1.5.0"},
		{
			name: "a release signed with another key", unsigned: true, version: "1.5.0",
			wantText: "the signature",
		},
		{
			name: "a checksum that does not match", wrongSum: true, version: "1.5.0",
			wantText: "SHA-256",
		},
		{
			name: "a release that failed its health gate", version: "1.5.0", badList: "1.5.0",
			want: ErrBadRelease,
		},
		{name: "a release that is older", version: "1.3.0", want: ErrDowngrade},
		{name: "a version name with a letter in front", version: "v1.5.0"},
		{name: "the release that runs", version: testRunning, want: ErrDowngrade},
		{name: "a build with no public key", version: "1.5.0", key: "none", want: ErrNoKey},
		{name: "a partition that is nearly full", version: "1.5.0", free: 1 << 20, want: ErrNoSpace},
		{name: "a version name with a path in it", version: "../escape", wantText: "not a release name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t, func(o *Options) {
				if tt.key == "none" {
					o.PublicKey = ""
				}
				if tt.free != 0 {
					free := tt.free
					o.FreeBytes = func(string) (uint64, error) { return free, nil }
				}
			})
			if tt.badList != "" {
				w.bad[tt.badList] = true
			}
			b := w.newBundle("the new binary", tt.unsigned, tt.wrongSum)
			_, rel := b.serve(t, tt.version)

			err := w.man.Apply(context.Background(), rel)
			good := tt.want == nil && tt.wantText == ""

			switch {
			case good && err != nil:
				t.Fatalf("Apply() = %v, want no error", err)
			case !good && err == nil:
				t.Fatal("Apply() gave no error")
			case tt.want != nil && !errors.Is(err, tt.want):
				t.Fatalf("Apply() = %v, want %v", err, tt.want)
			case tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText):
				t.Fatalf("Apply() = %v, want a message with %q in it", err, tt.wantText)
			}

			if good {
				if got := w.current(); got != ReleasesDir+"/"+tt.version {
					t.Errorf("current = %q, want the new release", got)
				}
				if got := w.pending(); got != tt.version {
					t.Errorf("the pending marker = %q, want %q", got, tt.version)
				}
				if body, err := w.installed(tt.version); err != nil || body != "the new binary" {
					t.Errorf("the installed binary = %q, %v", body, err)
				}
				if w.restarts != 1 {
					t.Errorf("restarts = %d, want 1", w.restarts)
				}
				return
			}

			// Every refusal leaves the device on the release that runs.
			if got := w.current(); got != ReleasesDir+"/"+testRunning {
				t.Errorf("current = %q; a refusal moved the link", got)
			}
			if got := w.pending(); got != "" {
				t.Errorf("the pending marker = %q; a refusal wrote it", got)
			}
			if w.restarts != 0 {
				t.Errorf("restarts = %d; a refusal restarted the service", w.restarts)
			}
			if tt.version != testRunning {
				if _, err := os.Stat(filepath.Join(w.root, ReleasesDir, tt.version)); err == nil {
					t.Errorf("a refusal left the release directory %s behind", tt.version)
				}
			}
		})
	}
}

// A release that is applied must put previous at the release that ran. Without it
// the health gate has nothing to go back to.
func TestApplyMovesPrevious(t *testing.T) {
	w := newWorld(t, nil)
	b := w.newBundle("the new binary", false, false)
	_, rel := b.serve(t, "1.5.0")

	if err := w.man.Apply(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	if got := w.man.linkTarget(PreviousLink); got != ReleasesDir+"/"+testRunning {
		t.Errorf("previous = %q, want the release that ran", got)
	}
}

// The updater keeps current and previous and nothing else. A device has 3.5 GB for
// the whole system.
func TestPrune(t *testing.T) {
	w := newWorld(t, nil)
	mustWrite(t, filepath.Join(w.root, ReleasesDir, "1.0.0", testBinary), []byte("very old"))
	mustWrite(t, filepath.Join(w.root, ReleasesDir, "1.2.0", testBinary), []byte("old"))

	b := w.newBundle("the new binary", false, false)
	_, rel := b.serve(t, "1.5.0")
	if err := w.man.Apply(context.Background(), rel); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(w.root, ReleasesDir))
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := []string{testRunning, "1.5.0"}
	if strings.Join(left, ",") != strings.Join(want, ",") {
		t.Errorf("the releases left are %v, want %v", left, want)
	}
}

// A sideloaded bundle enters the same flow at the verify step (D52). A good bundle
// is applied and the bundle goes away; a bundle that somebody changed is refused
// and the bundle still goes away, so a reboot does not try it again.
func TestSideload(t *testing.T) {
	tests := []struct {
		name     string
		shape    string // "loose" | "subdir" | "archive"
		unsigned bool
		wrongSum bool
		wantOK   bool
	}{
		{name: "three files in the directory", shape: "loose", wantOK: true},
		{name: "three files in a directory under it", shape: "subdir", wantOK: true},
		{name: "a tar.gz bundle", shape: "archive", wantOK: true},
		{name: "a binary that somebody changed", shape: "loose", unsigned: true},
		{name: "a checksum that does not match", shape: "loose", wrongSum: true},
		{name: "a tar.gz with a changed binary", shape: "archive", unsigned: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t, nil)
			updateDir := filepath.Join(w.media, "_update")
			b := w.newBundle("the new binary", tt.unsigned, tt.wrongSum)

			switch tt.shape {
			case "loose":
				b.writeDir(t, updateDir)
			case "subdir":
				b.writeDir(t, filepath.Join(updateDir, "portapixel-1.5.0"))
			case "archive":
				writeArchive(t, filepath.Join(updateDir, "portapixel-1.5.0.tar.gz"), b)
			}

			err := w.man.Sideload(context.Background())
			if tt.wantOK && err != nil {
				t.Fatalf("Sideload() = %v, want no error", err)
			}
			if !tt.wantOK && err == nil {
				t.Fatal("Sideload() gave no error")
			}

			// The bundle is gone either way.
			entries, readErr := os.ReadDir(updateDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("the sideload directory still holds %v", names)
			}

			if tt.wantOK {
				if got := w.current(); got != ReleasesDir+"/1.5.0" {
					t.Errorf("current = %q, want the sideloaded release", got)
				}
				if got := w.pending(); got != "1.5.0" {
					t.Errorf("the pending marker = %q, want 1.5.0", got)
				}
				return
			}
			if got := w.current(); got != ReleasesDir+"/"+testRunning {
				t.Errorf("current = %q; a refused bundle moved the link", got)
			}
		})
	}
}

// A sideload of a release that is already refused must not even reach the verify
// step. The bad list holds a release that failed its health gate once.
func TestSideloadRefusesABadRelease(t *testing.T) {
	w := newWorld(t, nil)
	w.bad["1.5.0"] = true
	b := w.newBundle("the new binary", false, false)
	b.writeDir(t, filepath.Join(w.media, "_update"))

	err := w.man.Sideload(context.Background())
	if !errors.Is(err, ErrBadRelease) {
		t.Fatalf("Sideload() = %v, want %v", err, ErrBadRelease)
	}
	if got := w.current(); got != ReleasesDir+"/"+testRunning {
		t.Errorf("current = %q; a bad release moved the link", got)
	}
}

// A bundle for the other processor must be left alone, not refused: a person with
// two device kinds copies both bundles onto one stick.
func TestSideloadIgnoresAnotherArchitecture(t *testing.T) {
	w := newWorld(t, nil)
	updateDir := filepath.Join(w.media, "_update")
	mustWrite(t, filepath.Join(updateDir, "portapixeld-amd64"), []byte("x"))
	mustWrite(t, filepath.Join(updateDir, "portapixeld-amd64"+SigSuffix), []byte("x"))
	mustWrite(t, filepath.Join(updateDir, SumsName), []byte("x  portapixeld-amd64\n"))

	if err := w.man.Sideload(context.Background()); err != nil {
		t.Fatalf("Sideload() = %v, want no error", err)
	}
	if _, err := os.Stat(filepath.Join(updateDir, "portapixeld-amd64")); err != nil {
		t.Error("the bundle of the other architecture was removed")
	}
}

// The health gate writes <version>.bad after a rollback. The daemon must find it at
// its next start, record the release and say so in the status.
func TestCheckRollback(t *testing.T) {
	w := newWorld(t, nil)
	mustWrite(t, filepath.Join(w.root, HealthDir, "1.5.0"+BadSuffix), nil)

	version, found := w.man.CheckRollback()
	if !found || version != "1.5.0" {
		t.Fatalf("CheckRollback() = %q, %v", version, found)
	}
	if !w.bad["1.5.0"] {
		t.Error("the release is not in the bad list")
	}
	state := w.man.State()
	if state.State != "rolled-back" {
		t.Errorf("state = %q, want rolled-back", state.State)
	}
	if state.Current != testRunning {
		t.Errorf("current = %q, want %q", state.Current, testRunning)
	}

	// No marker means no rollback.
	clean := newWorld(t, nil)
	if _, found := clean.man.CheckRollback(); found {
		t.Error("CheckRollback() found a rollback in a clean release root")
	}
}

// The check must read the GitHub answer, refuse a prerelease and a version that is
// not newer, and say in words what a rate limit is.
func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		key      string
		want     string
		wantErr  error
		wantText string
	}{
		{
			name:   "a newer release",
			status: http.StatusOK,
			body:   releaseJSON("1.5.0", false, true),
			want:   "1.5.0",
		},
		{
			name:    "the release that runs",
			status:  http.StatusOK,
			body:    releaseJSON(testRunning, false, true),
			wantErr: ErrNoRelease,
		},
		{
			name:    "an older release",
			status:  http.StatusOK,
			body:    releaseJSON("1.3.0", false, true),
			wantErr: ErrNoRelease,
		},
		{
			name:    "a prerelease",
			status:  http.StatusOK,
			body:    releaseJSON("1.6.0", true, true),
			wantErr: ErrNoRelease,
		},
		{
			name:    "a release with no binary for this processor",
			status:  http.StatusOK,
			body:    releaseJSON("1.5.0", false, false),
			wantErr: ErrArch,
		},
		{
			name:     "a rate limit",
			status:   http.StatusForbidden,
			body:     "{}",
			wantText: "asked too often",
		},
		{
			name:     "no releases at all",
			status:   http.StatusNotFound,
			body:     "{}",
			wantText: "no releases",
		},
		{
			name:    "a development build",
			status:  http.StatusOK,
			body:    releaseJSON("1.5.0", false, true),
			key:     "none",
			wantErr: ErrNoKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			w := newWorld(t, func(o *Options) {
				if tt.key == "none" {
					o.PublicKey = ""
				}
			})
			rel, err := w.man.Check(context.Background(),
				Source{Repo: "ethanpil/portapixel", APIRoot: server.URL})

			switch {
			case tt.want != "":
				if err != nil {
					t.Fatalf("Check() = %v", err)
				}
				if rel.Version != tt.want {
					t.Errorf("version = %q, want %q", rel.Version, tt.want)
				}
				if got := w.man.State(); got.Available != tt.want || got.State != "available" {
					t.Errorf("state = %+v", got)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Check() = %v, want %v", err, tt.wantErr)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), tt.wantText) {
					t.Fatalf("Check() = %v, want a message with %q in it", err, tt.wantText)
				}
			}
		})
	}
}

// releaseJSON builds the answer of the GitHub API.
func releaseJSON(version string, prerelease, withAsset bool) string {
	assets := "[]"
	if withAsset {
		assets = fmt.Sprintf(`[
			{"name": %q, "browser_download_url": "http://x/%s", "size": 100},
			{"name": %q, "browser_download_url": "http://x/sig", "size": 200},
			{"name": %q, "browser_download_url": "http://x/sums", "size": 90}
		]`, testAsset, testAsset, testAsset+SigSuffix, SumsName)
	}
	return fmt.Sprintf(`{"tag_name": %q, "body": "notes", "draft": false,
		"prerelease": %t, "published_at": "2026-01-01T00:00:00Z", "assets": %s}`,
		version, prerelease, assets)
}

// Two updates at once must not both stage in one release root.
func TestApplyRefusesASecondUpdate(t *testing.T) {
	w := newWorld(t, nil)
	if !w.man.take() {
		t.Fatal("take() gave false on an idle manager")
	}
	b := w.newBundle("the new binary", false, false)
	_, rel := b.serve(t, "1.5.0")
	if err := w.man.Apply(context.Background(), rel); !errors.Is(err, ErrBusy) {
		t.Fatalf("Apply() = %v, want %v", err, ErrBusy)
	}
}

// The real symlink flip, where the system permits it. Windows without the
// developer mode refuses os.Symlink, and the tests then use fileFlip.
func TestFlipSymlink(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, CurrentLink)
	if err := os.Symlink(ReleasesDir+"/1.4.0", link); err != nil {
		t.Skip("this system does not permit a symlink: " + err.Error())
	}

	if err := FlipSymlink(link, ReleasesDir+"/1.5.0"); err != nil {
		t.Fatalf("FlipSymlink() = %v", err)
	}
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.ToSlash(got) != ReleasesDir+"/1.5.0" {
		t.Errorf("the link points at %q", got)
	}
	// The staging link must not be left behind.
	if _, err := os.Lstat(link + ".new"); err == nil {
		t.Error("the staging link is still there")
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.5.0", "1.4.0", 1},
		{"1.4.0", "1.5.0", -1},
		{"1.4.0", "1.4.0", 0},
		{"v1.5.0", "1.5.0", 0},
		{"1.10.0", "1.9.0", 1},
		{"2.0", "1.9.9", 1},
		{"1.5.0", "1.5", 0},
		{"1.5.0", "1.5.0-rc1", 1},
		{"1.5.0-rc1", "1.5.0-rc2", -1},
		{"1.5.0", "dev", 1},
		{"dev", "1.5.0", -1},
		{"dev", "dev", 0},
	}
	for _, tt := range tests {
		t.Run(tt.a+" against "+tt.b, func(t *testing.T) {
			if got := CompareVersions(tt.a, tt.b); got != tt.want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestValidVersion(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"1.5.0", true},
		{"v1.5.0", true},
		{"1.5.0-rc1", true},
		{"", false},
		{".hidden", false},
		{"..", false},
		{"1.5.0/x", false},
		{`1.5.0\x`, false},
		{"1.5.0 x", false},
		{strings.Repeat("1", 65), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidVersion(tt.name); got != tt.want {
				t.Errorf("ValidVersion(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestReadSums(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SumsName)
	mustWrite(t, path, []byte(
		"aaaa  portapixeld-arm64\r\n"+
			"bbbb *portapixeld-amd64\n"+
			"cccc  sub/dir/portapixeld-arm64\n"+
			"\n"+
			"oneFieldOnThisLine\n"))

	sums, err := readSums(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"portapixeld-arm64": "aaaa", "portapixeld-amd64": "bbbb"}
	if len(sums) != len(want) {
		t.Fatalf("sums = %v, want %v", sums, want)
	}
	for name, sum := range want {
		if sums[name] != sum {
			t.Errorf("sums[%q] = %q, want %q", name, sums[name], sum)
		}
	}
}

// writeArchive packs a bundle into a .tar.gz.
func writeArchive(t *testing.T, path string, b bundle) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		body []byte
	}{
		{testAsset, b.binary},
		{testAsset + SigSuffix, b.sig},
		{SumsName, b.sums},
	} {
		header := &tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// An archive with a path step or a link in it must be refused. The archive comes
// from removable storage that any laptop can write.
func TestUnpackBundleRefusesABadArchive(t *testing.T) {
	tests := []struct {
		name    string
		entries []*tar.Header
	}{
		{
			name: "a name with a path step",
			entries: []*tar.Header{
				{Name: "../" + testAsset, Mode: 0o644, Typeflag: tar.TypeReg},
			},
		},
		{
			name: "a name in a directory",
			entries: []*tar.Header{
				{Name: "sub/" + testAsset, Mode: 0o644, Typeflag: tar.TypeReg},
			},
		},
		{
			name: "a symbolic link with one of the three names",
			entries: []*tar.Header{
				{Name: SumsName, Linkname: "/etc/shadow", Mode: 0o777, Typeflag: tar.TypeSymlink},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "bundle.tar.gz")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			for _, h := range tt.entries {
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
			}
			tw.Close()
			gz.Close()
			f.Close()

			out := filepath.Join(dir, "out")
			if err := unpackBundle(path, out, testAsset); err == nil {
				t.Fatal("unpackBundle() gave no error")
			}
			if _, err := os.Stat(filepath.Join(out, "..", testAsset)); err == nil {
				t.Error("a file landed outside the output directory")
			}
		})
	}
}

// ------------------------------------------------------ the fleet release path

// The release mirror of a fleet server needs the device token on all three files.
// Without it the server answers 401 and the update never happens.
func TestFleetReleaseSendsTheDeviceToken(t *testing.T) {
	const token = "a-device-token"
	w := newWorld(t, nil)
	b := w.newBundle("the new binary", false, false)

	var seen []string
	mux := http.NewServeMux()
	serve := func(body []byte) http.HandlerFunc {
		return func(rw http.ResponseWriter, r *http.Request) {
			seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization"))
			if r.Header.Get("Authorization") != "Bearer "+token {
				rw.WriteHeader(http.StatusUnauthorized)
				return
			}
			rw.Write(body)
		}
	}
	mux.HandleFunc("/api/v1/releases/1.5.0/"+testAsset, serve(b.binary))
	mux.HandleFunc("/api/v1/releases/1.5.0/"+testAsset+SigSuffix, serve(b.sig))
	mux.HandleFunc("/api/v1/releases/1.5.0/"+SumsName, serve(b.sums))
	server := httptest.NewServer(mux)
	defer server.Close()

	// The manifest of the server gives a path, not a whole address.
	rel, err := w.man.Check(context.Background(), Source{
		BaseURL:   "/api/v1/releases/1.5.0",
		ServerURL: server.URL,
		Bearer:    token,
		Version:   "1.5.0",
	})
	if err != nil {
		t.Fatalf("Check() = %v", err)
	}
	if rel.Source != "fleet" {
		t.Errorf("source = %q, want fleet", rel.Source)
	}
	if err := w.man.Apply(context.Background(), rel); err != nil {
		t.Fatalf("Apply() = %v; the calls were %v", err, seen)
	}
	if len(seen) != 3 {
		t.Fatalf("the device made %d calls: %v", len(seen), seen)
	}
	for _, call := range seen {
		if !strings.HasSuffix(call, "Bearer "+token) {
			t.Errorf("a call went out with no token: %q", call)
		}
	}
	if got := w.current(); got != ReleasesDir+"/1.5.0" {
		t.Errorf("current = %q, want the fleet release", got)
	}
}

// A mirror that needs the token and gets none must fail, and the device must stay
// on the release that runs.
func TestFleetReleaseWithoutATokenFails(t *testing.T) {
	w := newWorld(t, nil)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		rw.Write([]byte("x"))
	}))
	defer server.Close()

	rel, err := w.man.Check(context.Background(), Source{
		BaseURL: server.URL + "/api/v1/releases/1.5.0", Version: "1.5.0",
	})
	if err != nil {
		t.Fatalf("Check() = %v", err)
	}
	if err := w.man.Apply(context.Background(), rel); err == nil {
		t.Fatal("Apply() gave no error for a mirror that answered 401")
	}
	if got := w.current(); got != ReleasesDir+"/"+testRunning {
		t.Errorf("current = %q; a failed download moved the link", got)
	}
}

// The device token goes to the fleet host and to no other. A GitHub release and a
// redirect to another host both get no header.
func TestBearerGoesToTheFleetHostOnly(t *testing.T) {
	tests := []struct {
		name    string
		rel     Release
		address string
		want    string
	}{
		{
			name:    "the fleet host",
			rel:     Release{Bearer: "t", BearerHost: "signage.example.com"},
			address: "https://signage.example.com/api/v1/releases/1.5.0/portapixeld-arm64",
			want:    "t",
		},
		{
			name:    "another host",
			rel:     Release{Bearer: "t", BearerHost: "signage.example.com"},
			address: "https://cdn.example.net/portapixeld-arm64",
			want:    "",
		},
		{
			name:    "a subdomain of the fleet host",
			rel:     Release{Bearer: "t", BearerHost: "signage.example.com"},
			address: "https://files.signage.example.com/portapixeld-arm64",
			want:    "",
		},
		{
			name:    "a GitHub release",
			rel:     Release{Source: "github"},
			address: "https://github.com/x/y/releases/download/1.5.0/portapixeld-arm64",
			want:    "",
		},
		{
			name:    "a token with no host",
			rel:     Release{Bearer: "t"},
			address: "https://signage.example.com/x",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bearerFor(tt.rel, tt.address)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("bearerFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A redirect that leaves the host must not carry the token with it.
func TestRedirectDropsTheToken(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://cdn.example.net/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer t")
	first, err := http.NewRequest(http.MethodGet, "https://signage.example.com/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dropBearerOnAnotherHost(req, []*http.Request{first}); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("the token followed the redirect to another host")
	}

	// A redirect on the same host keeps it: the server can move a path.
	same, err := http.NewRequest(http.MethodGet, "https://signage.example.com/other", nil)
	if err != nil {
		t.Fatal(err)
	}
	same.Header.Set("Authorization", "Bearer t")
	if err := dropBearerOnAnotherHost(same, []*http.Request{first}); err != nil {
		t.Fatal(err)
	}
	if same.Header.Get("Authorization") == "" {
		t.Error("the token was dropped on a redirect inside the same host")
	}
}

func TestResolveBase(t *testing.T) {
	tests := []struct {
		name      string
		serverURL string
		baseURL   string
		want      string
		wantErr   bool
	}{
		{
			name: "a path against a server address", serverURL: "https://s.example.com/",
			baseURL: "/api/v1/releases/1.5.0", want: "https://s.example.com/api/v1/releases/1.5.0",
		},
		{
			name: "a path with no leading slash", serverURL: "https://s.example.com",
			baseURL: "api/v1/releases/1.5.0", want: "https://s.example.com/api/v1/releases/1.5.0",
		},
		{
			name: "a whole address wins", serverURL: "https://s.example.com",
			baseURL: "https://mirror.example.net/r/1.5.0", want: "https://mirror.example.net/r/1.5.0",
		},
		{
			name: "a trailing slash goes", serverURL: "https://s.example.com",
			baseURL: "/r/1.5.0/", want: "https://s.example.com/r/1.5.0",
		},
		{name: "a path and no server", baseURL: "/r/1.5.0", wantErr: true},
		{name: "nothing at all", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveBase(tt.serverURL, tt.baseURL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveBase() = %q, %v", got, err)
			}
			if got != tt.want && !tt.wantErr {
				t.Errorf("resolveBase() = %q, want %q", got, tt.want)
			}
		})
	}
}
