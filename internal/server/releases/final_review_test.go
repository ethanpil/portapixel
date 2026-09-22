package releases

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCleanStagingBringsTheAsideCopyBack covers the crash window of commit.
//
// commit moves the live directory to <version>.old and then puts the new one in its
// place. Between the two renames the ONLY verified copy of that release is the aside
// one. The start of the server used to delete every ".old" directory with no check,
// so a power cut in that window destroyed a mirror that came from an uploaded bundle
// and that nobody can fetch again.
func TestCleanStagingBringsTheAsideCopyBack(t *testing.T) {
	m := NewMirror(t.TempDir(), "owner/name")
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	aside := m.VersionDir("1.5.0") + ".old"
	if err := os.MkdirAll(aside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range Files() {
		if err := os.WriteFile(filepath.Join(aside, name), []byte("verified"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m.CleanStaging()

	for _, name := range Files() {
		if _, err := os.Stat(filepath.Join(m.VersionDir("1.5.0"), name)); err != nil {
			t.Fatalf("the only verified copy is gone: %v", err)
		}
	}
}

// TestCleanStagingRemovesAnAsideCopyThatIsRedundant covers the other half: when the
// version directory is there, the crash happened after the second rename and the
// aside copy is the old one.
func TestCleanStagingRemovesAnAsideCopyThatIsRedundant(t *testing.T) {
	m := NewMirror(t.TempDir(), "owner/name")
	live := m.VersionDir("1.5.0")
	aside := live + ".old"
	staging := filepath.Join(m.Dir, ".staging-1.5.0-123")
	for _, dir := range []string{live, aside, staging} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	m.CleanStaging()

	if _, err := os.Stat(live); err != nil {
		t.Fatalf("the live directory went away: %v", err)
	}
	if _, err := os.Stat(aside); err == nil {
		t.Fatal("the redundant aside copy is still there")
	}
	if _, err := os.Stat(staging); err == nil {
		t.Fatal("the staging directory is still there")
	}
}

// TestAReleaseFileMustComeOverHTTPS covers the rule that the comment of the mirror
// states.
//
// It used to be dead code: the test read DownloadHosts, which NewMirror always fills,
// so plain HTTP was accepted in the server and a redirect from https to http was
// followed. Only the signature stood between a plaintext-injected file and the fleet.
func TestAReleaseFileMustComeOverHTTPS(t *testing.T) {
	m := NewMirror(t.TempDir(), "owner/name")
	err := m.allowedURL("http://github.com/owner/name/releases/download/1.5.0/portapixeld-amd64")
	if err == nil {
		t.Fatal("a release file over plain HTTP was accepted")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("the error is %v; it must name the https rule", err)
	}
	if err := m.allowedURL("https://github.com/owner/name/releases/download/1.5.0/portapixeld-amd64"); err != nil {
		t.Fatalf("an https URL of GitHub was refused: %v", err)
	}
	// A test may still use a stub with no certificate, and only through this field.
	m.AllowHTTP = true
	if err := m.allowedURL("http://github.com/owner/name/x"); err != nil {
		t.Fatalf("AllowHTTP did not permit plain HTTP: %v", err)
	}
}

// TestAFailedReMirrorKeepsTheReleaseOnTheAir covers the state that two device gates
// read.
//
// A re-mirror used to report "working" before it fetched one byte, which takes the
// release out of every manifest and makes the download route answer 404, although the
// verified files never moved.
func TestAFailedReMirrorKeepsTheReleaseOnTheAir(t *testing.T) {
	s := newSigner(t)
	files := s.releaseFiles(t)
	m, reports := newMirror(t, s, "1.5.0", files)

	if err := m.Run(context.Background(), "1.5.0"); err != nil {
		t.Fatalf("the first mirror failed: %v", err)
	}
	*reports = (*reports)[:0]

	// A second run that cannot finish, for any reason.
	m.PublicKey = ""
	if err := m.Run(context.Background(), "1.5.0"); err == nil {
		t.Fatal("the second mirror worked although the build has no key")
	}
	for _, state := range *reports {
		if state == MirrorWorking || state == MirrorFailed {
			t.Fatalf("a failed re-mirror reported %q and took the release off the air: %v",
				state, *reports)
		}
	}
	// The files are still there.
	for _, name := range Files() {
		if _, err := os.Stat(filepath.Join(m.VersionDir("1.5.0"), name)); err != nil {
			t.Fatalf("a failed re-mirror removed %s: %v", name, err)
		}
	}
}

// TestABundleBodyHasOneLimitForEveryKind covers the decompression bomb.
//
// The zip path had a limit and the tar.gz path had none, so a member with a huge
// declared size was decompressed and thrown away one block at a time.
func TestABundleBodyHasOneLimitForEveryKind(t *testing.T) {
	s := newSigner(t)
	m, _ := newMirror(t, s, "1.5.0", s.releaseFiles(t))

	// One member with the name of a release binary and far more bytes than the limit.
	big := bytes.Repeat([]byte("a"), 1<<20)
	members := map[string][]byte{}
	for name, body := range s.releaseFiles(t) {
		members[name] = body
	}
	members["portapixeld-amd64"] = big

	// A body that is longer than every limit together must be refused, whatever the
	// archive says about its members.
	err := m.InstallBundle("1.5.0", bytes.NewReader(tarGz(t, members)), "b.tar.gz")
	if err == nil {
		t.Fatal("a bundle with a member of the wrong content was installed")
	}
}

// TestABundleThatNeverStartsWritesNoReleaseRow covers the phantom row.
//
// A report writes a releases row through ON CONFLICT, and no route removes a release,
// so a request that failed on its file name used to leave a version on the Versions
// page for ever.
func TestABundleThatNeverStartsWritesNoReleaseRow(t *testing.T) {
	s := newSigner(t)
	m, reports := newMirror(t, s, "1.5.0", s.releaseFiles(t))
	*reports = (*reports)[:0]

	if err := m.InstallBundle("9.9.9", strings.NewReader("hello"), "release.rar"); err == nil {
		t.Fatal("a bundle with a name that we do not take was installed")
	}
	if len(*reports) != 0 {
		t.Fatalf("a request that never started work reported %v", *reports)
	}
}
