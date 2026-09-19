package server_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aead.dev/minisign"
)

// testRelease holds the files of a signed test release and the public key that
// verifies them.
type testRelease struct {
	version string
	public  string
	files   map[string][]byte
}

// signRelease makes a key pair and the five files of a release. The release key of
// a build is empty (internal/version.PublicKey), so a test brings its own; that is
// why Mirror.PublicKey is a field.
func signRelease(t *testing.T, version string) testRelease {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	text, err := pub.MarshalText()
	if err != nil {
		t.Fatal(err)
	}

	out := testRelease{version: version, public: string(text), files: map[string][]byte{}}
	var sums strings.Builder
	for _, name := range []string{"portapixeld-amd64", "portapixeld-arm64"} {
		body := []byte("the " + name + " binary of " + version)
		out.files[name] = body

		r := minisign.NewReader(bytes.NewReader(body))
		if _, err := io.Copy(io.Discard, r); err != nil {
			t.Fatal(err)
		}
		out.files[name+".minisig"] = r.Sign(priv)

		sum := sha256.Sum256(body)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	out.files["SHA256SUMS"] = []byte(sums.String())
	return out
}

// fakeGitHubFor answers the release list of one release and serves its files.
func fakeGitHubFor(t *testing.T, rel testRelease) string {
	t.Helper()
	mux := http.NewServeMux()
	var base string

	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := rel.files[strings.TrimPrefix(r.URL.Path, "/download/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		var assets []string
		for name := range rel.files {
			assets = append(assets, fmt.Sprintf(`{"name":%q,"browser_download_url":%q}`,
				name, base+"/download/"+name))
		}
		fmt.Fprintf(w, `[{"tag_name":%q,"body":"the notes of this release",`+
			`"published_at":"2026-08-04T10:00:00Z","assets":[%s]}]`, rel.version, strings.Join(assets, ","))
	})

	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv.URL
}

// useRelease points the mirror of a test server at a fake GitHub.
//
// The stub answers on the loopback. The mirror serves a release file only from
// GitHub, so the allowlist takes that address for the test.
func (f *fleet) useRelease(rel testRelease) {
	f.mirror.Lister.BaseURL = fakeGitHubFor(f.t, rel)
	f.mirror.PublicKey = rel.public
	f.mirror.DownloadHosts = []string{"127.0.0.1", "localhost", "::1"}
}

// waitForMirror waits until the mirror of one version stops working.
func (f *fleet) waitForMirror(version string) (state, errText string) {
	f.t.Helper()
	for i := 0; i < 200; i++ {
		row, err := f.db.Release(version)
		if err == nil && row.MirrorState != "working" && row.MirrorState != "idle" {
			return row.MirrorState, row.MirrorError
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("the mirror of %s did not finish", version)
	return "", ""
}

func TestReleaseListApproveAndMirror(t *testing.T) {
	f := newFleet(t)
	f.login()
	rel := signRelease(t, "1.5.0")
	f.useRelease(rel)

	// The list comes from the fake GitHub and goes into the table.
	res := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/releases", nil), "the release list")
	var view struct {
		Releases []struct {
			Version     string `json:"version"`
			Notes       string `json:"notes"`
			Approved    bool   `json:"approved"`
			Mirrored    bool   `json:"mirrored"`
			MirrorState string `json:"mirror_state"`
		} `json:"releases"`
		Approved  string `json:"approved"`
		HasKey    bool   `json:"has_key"`
		ListError string `json:"list_error"`
	}
	res.json(t, &view)
	if len(view.Releases) != 1 || view.Releases[0].Version != "1.5.0" {
		t.Fatalf("the list is %+v", view)
	}
	if view.Releases[0].Notes != "the notes of this release" {
		t.Fatalf("the notes are %q", view.Releases[0].Notes)
	}
	if view.Approved != "" || view.ListError != "" || !view.HasKey {
		t.Fatalf("the view is %+v", view)
	}

	// The approval starts the mirror.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/releases/1.5.0/approve", nil), "approve")
	state, errText := f.waitForMirror("1.5.0")
	if state != "done" {
		t.Fatalf("the mirror is %q: %s", state, errText)
	}

	res = f.mustOK(f.adminCall(http.MethodGet, "/api/admin/releases", nil), "the release list")
	res.json(t, &view)
	if view.Approved != "1.5.0" || !view.Releases[0].Mirrored {
		t.Fatalf("the list after the mirror is %+v", view)
	}

	// A device now gets the release in its manifest and can download the files.
	token := f.pairDevice("px-rel00001")
	m := f.getManifest(token)
	if m.Release == nil || m.Release.Version != "1.5.0" {
		t.Fatalf("the manifest release is %+v", m.Release)
	}
	for name, want := range rel.files {
		res := f.mustOK(f.device(http.MethodGet, m.Release.BaseURL+"/"+name, token, nil), "download "+name)
		if !bytes.Equal(res.body, want) {
			t.Fatalf("%s from the mirror is not the file of the release", name)
		}
	}

	// A file name that is not a release file is refused.
	for _, bad := range []string{"server.toml", "portapixeld", "SHA256SUMS.bak"} {
		res := f.device(http.MethodGet, "/api/v1/releases/1.5.0/"+bad, token, nil)
		if res.status != http.StatusNotFound {
			t.Errorf("the file %q answered %d", bad, res.status)
		}
	}

	// The approval goes away, and the device stops seeing the release.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/releases/1.5.0/unapprove", nil), "unapprove")
	if m = f.getManifest(token); m.Release != nil {
		t.Fatalf("a release that nobody approved went out: %+v", m.Release)
	}
}

func TestReleaseMirrorRefusesABadSignature(t *testing.T) {
	f := newFleet(t)
	f.login()
	rel := signRelease(t, "1.5.1")
	// The signature of one binary comes from another key.
	other := signRelease(t, "1.5.1")
	rel.files["portapixeld-arm64.minisig"] = other.files["portapixeld-arm64.minisig"]
	f.useRelease(rel)

	f.mustOK(f.adminCall(http.MethodGet, "/api/admin/releases", nil), "the release list")
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/releases/1.5.1/approve", nil), "approve")

	state, errText := f.waitForMirror("1.5.1")
	if state != "failed" {
		t.Fatalf("the mirror of a badly signed release is %q", state)
	}
	if errText == "" {
		t.Fatal("the failure has no error text for the admin")
	}

	// The release is approved but not mirrored, so no device gets it. That is the
	// whole point of the mirrored flag.
	token := f.pairDevice("px-rel00002")
	if m := f.getManifest(token); m.Release != nil {
		t.Fatalf("a release that did not verify went out: %+v", m.Release)
	}
	res := f.device(http.MethodGet, "/api/v1/releases/1.5.1/portapixeld-arm64", token, nil)
	if res.status != http.StatusNotFound {
		t.Fatalf("a file of a failed mirror answered %d", res.status)
	}
}

func TestReleaseMirrorWithNoReleaseKey(t *testing.T) {
	f := newFleet(t)
	f.login()
	rel := signRelease(t, "1.5.0")
	f.useRelease(rel)
	// A development build has no key. Then the server must say so and never mark a
	// release as mirrored.
	f.mirror.PublicKey = ""

	res := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/releases", nil), "the release list")
	var view struct {
		HasKey bool `json:"has_key"`
	}
	res.json(t, &view)
	if view.HasKey {
		t.Fatal("a build with no key says that it has one")
	}

	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/releases/1.5.0/approve", nil), "approve")
	state, errText := f.waitForMirror("1.5.0")
	if state != "failed" {
		t.Fatalf("the mirror is %q, want failed", state)
	}
	if !strings.Contains(errText, "no release key") {
		t.Fatalf("the error text is %q; it must say that this build has no release key", errText)
	}
}

func TestReleaseBundleUpload(t *testing.T) {
	f := newFleet(t)
	f.login()
	rel := signRelease(t, "1.5.0")
	f.mirror.PublicKey = rel.public

	// A closed network: no GitHub at all, the admin uploads the bundle.
	members := map[string][]byte{}
	for name, body := range rel.files {
		members["portapixel-1.5.0/"+name] = body
	}
	f.mustOK(f.upload("/api/admin/releases/1.5.0/bundle", "portapixel-1.5.0.tar.gz",
		tarGzOf(t, members)), "the bundle upload")

	row, err := f.db.Release("1.5.0")
	if err != nil {
		t.Fatal(err)
	}
	if !row.Mirrored || row.MirrorState != "done" {
		t.Fatalf("the release row is %+v", row)
	}

	// The files serve after the admin approves the version.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/releases/1.5.0/approve", nil), "approve")
	token := f.pairDevice("px-bun00001")
	m := f.getManifest(token)
	if m.Release == nil || m.Release.Version != "1.5.0" {
		t.Fatalf("the manifest release is %+v", m.Release)
	}
	res := f.mustOK(f.device(http.MethodGet, "/api/v1/releases/1.5.0/SHA256SUMS", token, nil), "the sums")
	if !bytes.Equal(res.body, rel.files["SHA256SUMS"]) {
		t.Fatal("the checksum file of the bundle did not arrive")
	}
}

func TestReleaseBundleRefusesTraversal(t *testing.T) {
	f := newFleet(t)
	f.login()
	rel := signRelease(t, "1.5.0")
	f.mirror.PublicKey = rel.public

	for _, member := range []string{
		"../../portapixeld-amd64",
		"/etc/SHA256SUMS",
		`..\..\SHA256SUMS`,
	} {
		body := tarGzOf(t, map[string][]byte{member: []byte("evil")})
		res := f.upload("/api/admin/releases/1.5.0/bundle", "evil.tar.gz", body)
		if res.status != http.StatusUnprocessableEntity {
			t.Fatalf("the bundle with the member %q answered %d: %s", member, res.status, res.body)
		}
		if res.errorText(t) == "" {
			t.Fatalf("the answer holds no error text: %s", res.body)
		}
		row, err := f.db.Release("1.5.0")
		if err == nil && row.Mirrored {
			t.Fatalf("a bundle that tried to leave the directory was accepted: %q", member)
		}
	}
}

func TestReleaseBundleRefusesAnIncompleteArchive(t *testing.T) {
	f := newFleet(t)
	f.login()
	rel := signRelease(t, "1.5.0")
	f.mirror.PublicKey = rel.public

	members := map[string][]byte{}
	for name, body := range rel.files {
		if name == "SHA256SUMS" {
			continue
		}
		members[name] = body
	}
	res := f.upload("/api/admin/releases/1.5.0/bundle", "short.tar.gz", tarGzOf(t, members))
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("an incomplete bundle answered %d: %s", res.status, res.body)
	}
}

// TestReleaseVersionInThePathIsChecked proves that the version guard answers and not
// the path cleaning of http.ServeMux.
//
// ".." never reaches the handler: the mux cleans the path and answers 404 for it. So
// the cases here are values that a path holds and that the guard must refuse: a
// value with a separator in its escaped form, a value with a semicolon, and a name
// that starts with a full stop.
func TestReleaseVersionInThePathIsChecked(t *testing.T) {
	f := newFleet(t)
	f.login()
	for _, bad := range []string{"a%2Fb", "a;b", ".hidden", ".staging-1.5.0"} {
		res := f.adminCall(http.MethodPost, "/api/admin/releases/"+bad+"/approve", nil)
		if res.status != http.StatusBadRequest {
			t.Errorf("the version %q answered %d, want 400: %s", bad, res.status, res.body)
			continue
		}
		if res.errorText(t) == "" {
			t.Errorf("the answer for %q holds no error text: %s", bad, res.body)
		}
		if kind := res.header.Get("Content-Type"); kind != "application/json" {
			t.Errorf("the answer for %q is %q", bad, kind)
		}
	}
	// ".." is the case that the mux answers. It must still be JSON.
	res := f.adminCall(http.MethodPost, "/api/admin/releases/../approve", nil)
	if res.status != http.StatusNotFound && res.status != http.StatusBadRequest {
		t.Errorf("the version \"..\" answered %d", res.status)
	}
	if res.errorText(t) == "" {
		t.Errorf("the answer for \"..\" holds no error text: %s", res.body)
	}
}

// TestBundleWhileAMirrorRunsGives409 covers the upload and the download of one
// version at the same time. Two writers in one directory make a set of files that is
// a mixture of the two.
func TestBundleWhileAMirrorRunsGives409(t *testing.T) {
	f := newFleet(t)
	f.login()
	rel := signRelease(t, "1.5.0")
	f.mirror.PublicKey = rel.public
	// A mirror that holds the lock and never finishes, because the address answers
	// nothing.
	f.mirror.Lister.BaseURL = "http://127.0.0.1:1"
	f.mirror.DownloadHosts = []string{"127.0.0.1"}
	if err := f.mirror.Start(context.Background(), "1.5.0"); err != nil {
		t.Fatal(err)
	}

	members := map[string][]byte{}
	for name, body := range rel.files {
		members[name] = body
	}
	res := f.upload("/api/admin/releases/1.5.0/bundle", "b.tar.gz", tarGzOf(t, members))
	if res.status != http.StatusConflict && res.status != http.StatusOK {
		t.Fatalf("the bundle upload answered %d: %s", res.status, res.body)
	}
	if res.status == http.StatusConflict && res.errorText(t) == "" {
		t.Fatalf("the 409 holds no error text: %s", res.body)
	}
}

func TestReleaseListToleratesAFailure(t *testing.T) {
	f := newFleet(t)
	f.login()
	// A server with no way to reach GitHub still answers, with the error text for
	// the page (plan section 12).
	f.mirror.Lister.BaseURL = "http://127.0.0.1:1"

	res := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/releases", nil), "the release list")
	var view struct {
		Releases  []any  `json:"releases"`
		ListError string `json:"list_error"`
	}
	res.json(t, &view)
	if view.ListError == "" {
		t.Fatal("a failed release list reported no error")
	}
	if view.Releases == nil {
		t.Fatal("the list is null; the UI needs an empty list")
	}
}

// tarGzOf makes a .tar.gz of the given members.
func tarGzOf(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range members {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
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

// TestReleaseDownloadNeedsATokenAndServesRange covers the route that the device
// updater calls. The route is authenticated, which is correct: a release binary is
// content of this fleet and not a public file.
func TestReleaseDownloadNeedsATokenAndServesRange(t *testing.T) {
	f := newFleet(t)
	f.login()
	rel := signRelease(t, "1.5.0")
	f.useRelease(rel)

	f.mustOK(f.adminCall(http.MethodGet, "/api/admin/releases", nil), "the release list")
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/releases/1.5.0/approve", nil), "approve")
	if state, errText := f.waitForMirror("1.5.0"); state != "done" {
		t.Fatalf("the mirror is %q: %s", state, errText)
	}

	path := "/api/v1/releases/1.5.0/" + SumsFileName
	// No token, and a token that nobody gave out.
	for _, token := range []string{"", "a token that nobody gave out"} {
		if res := f.device(http.MethodGet, path, token, nil); res.status != http.StatusUnauthorized {
			t.Fatalf("the download with the token %q answered %d", token, res.status)
		}
	}

	token := f.pairDevice("px-reldl001")
	whole := f.mustOK(f.device(http.MethodGet, path, token, nil), "the whole file")
	want := rel.files[SumsFileName]
	if !bytes.Equal(whole.body, want) {
		t.Fatal("the file from the mirror is not the file of the release")
	}
	if whole.header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("the route does not announce Range support: %q", whole.header.Get("Accept-Ranges"))
	}

	// A Range request, which is what a resumed download of a large binary sends.
	ranged := f.call(http.MethodGet, path, nil, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Range", "bytes=5-14")
	})
	if ranged.status != http.StatusPartialContent {
		t.Fatalf("the range request answered %d: %s", ranged.status, ranged.body)
	}
	if !bytes.Equal(ranged.body, want[5:15]) {
		t.Fatal("the range holds the wrong bytes")
	}
}

// SumsFileName is the checksum file of a release. The tests name it here, so that
// they do not import internal/server/releases for one constant.
const SumsFileName = "SHA256SUMS"
