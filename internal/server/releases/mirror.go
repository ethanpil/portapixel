package releases

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/sigverify"
	"github.com/ethanpil/portapixel/internal/store"
	"github.com/ethanpil/portapixel/internal/version"
)

// The files of a release that the server mirrors. The device needs the binary of
// its own architecture, the signature of that binary, and the checksum file
// (D28, D47).
var (
	binaries = []string{"portapixeld-amd64", "portapixeld-arm64"}
	// SumsName is the checksum file of a release.
	SumsName = "SHA256SUMS"
)

// Files gives every file name that a mirror holds. The download route serves
// these names and no others: a name in a URL must never reach the filesystem.
func Files() []string {
	out := make([]string, 0, len(binaries)*2+1)
	for _, b := range binaries {
		out = append(out, b, b+".minisig")
	}
	return append(out, SumsName)
}

// IsMirrorFile reports if name is one of the files of a mirror.
func IsMirrorFile(name string) bool {
	for _, f := range Files() {
		if f == name {
			return true
		}
	}
	return false
}

// The size limits of the mirrored files. A body that has no end must not fill
// the disk of the server.
const (
	maxBinaryBytes = 200 << 20
	maxSigBytes    = 4 << 10
	maxSumsBytes   = 64 << 10
)

// ErrNoKey says that this build holds no minisign public key, so it cannot check
// a release. A development build is in that state (internal/version.PublicKey).
var ErrNoKey = errors.New("this build has no release key, so it cannot verify a release")

// ErrBusy says that a mirror already runs.
var ErrBusy = errors.New("a mirror already runs")

// Mirror downloads and verifies the files of a release.
type Mirror struct {
	// Dir is <data>/releases. Each version gets a directory under it.
	Dir string
	// Lister finds the download URLs of a version.
	Lister *Lister
	// Client fetches the files.
	Client *http.Client
	// PublicKey is the minisign key that a release must match. It is a field and
	// not a constant so that a test can sign with a key of its own; the default
	// is the key of the build.
	PublicKey string
	// Report says what the mirror does now. The caller writes it to the database.
	Report func(version, state, errText string)

	mu      sync.Mutex
	working string
}

// NewMirror makes a Mirror under dataDir.
func NewMirror(dataDir, repo string) *Mirror {
	return &Mirror{
		Dir:       filepath.Join(dataDir, "releases"),
		Lister:    NewLister(repo),
		Client:    &http.Client{},
		PublicKey: version.PublicKey,
		Report:    func(string, string, string) {},
	}
}

// VersionDir gives the directory of one version. The caller checks the version
// first with ValidVersion.
func (m *Mirror) VersionDir(v string) string { return filepath.Join(m.Dir, v) }

// ValidVersion reports if a version name is safe as a directory name and in a
// URL. A release tag holds digits, full stops, letters and the hyphen. Nothing
// else may reach a path.
func ValidVersion(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_':
		default:
			return false
		}
	}
	// A name of full stops only would be "." or "..".
	return strings.Trim(v, ".") != ""
}

// Working gives the version that the mirror works on, or an empty value.
func (m *Mirror) Working() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.working
}

// Start runs a mirror in the background. The admin route calls it: a download of
// two binaries takes longer than a request may take.
func (m *Mirror) Start(v string) error {
	if !ValidVersion(v) {
		return fmt.Errorf("%q is not a version name", v)
	}
	m.mu.Lock()
	if m.working != "" {
		m.mu.Unlock()
		return ErrBusy
	}
	m.working = v
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.working = ""
			m.mu.Unlock()
		}()
		m.Run(context.Background(), v)
	}()
	return nil
}

// Run mirrors one version and reports what happened. It gives the error as well,
// so a test and the selftest can read it.
func (m *Mirror) Run(ctx context.Context, v string) error {
	m.Report(v, stateWorking, "")
	err := m.mirror(ctx, v)
	if err != nil {
		m.Report(v, stateFailed, err.Error())
		return err
	}
	m.Report(v, stateDone, "")
	return nil
}

// The state words. They are the words of the mirror_state column of the
// releases table.
const (
	stateWorking = "working"
	stateDone    = "done"
	stateFailed  = "failed"
)

// mirror downloads every file of the release and verifies it.
func (m *Mirror) mirror(ctx context.Context, v string) error {
	if !ValidVersion(v) {
		return fmt.Errorf("%q is not a version name", v)
	}
	if m.PublicKey == "" {
		return ErrNoKey
	}
	rel, err := m.Lister.Release(ctx, v)
	if err != nil {
		return err
	}

	dir := m.VersionDir(v)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	for _, name := range Files() {
		url, ok := rel.Assets[name]
		if !ok {
			return fmt.Errorf("the release %s has no file %s", v, name)
		}
		if err := m.download(ctx, url, filepath.Join(dir, name), limitFor(name)); err != nil {
			return err
		}
	}
	return Verify(dir, m.PublicKey)
}

// limitFor gives the size limit of one file of a release.
func limitFor(name string) int64 {
	switch {
	case name == SumsName:
		return maxSumsBytes
	case strings.HasSuffix(name, ".minisig"):
		return maxSigBytes
	default:
		return maxBinaryBytes
	}
}

// download fetches one file to dest. It writes a temporary file and renames it,
// so a dropped connection never leaves a short file where a good one belongs.
func (m *Mirror) download(ctx context.Context, url, dest string, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := m.Client.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get %s: the server answered %s", url, resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".part*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		tmp.Close()
		if !done {
			os.Remove(tmpName)
		}
	}()

	n, err := io.Copy(tmp, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", url, err)
	}
	if n > limit {
		return fmt.Errorf("%s is longer than the limit of %d bytes for this kind of file", url, limit)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	done = true
	fsutil.SyncDir(filepath.Dir(dest))
	return nil
}

// Verify checks every binary of a mirror directory two ways: against its
// minisign signature with publicKey, and against the checksum file of the
// release. Both must pass.
//
// Two checks are not one check twice. The signature says that the project made
// this file. The checksum file says that this file is the one that the release
// names, which catches a mirror where two releases mixed.
func Verify(dir, publicKey string) error {
	if publicKey == "" {
		return ErrNoKey
	}
	sums, err := readSums(filepath.Join(dir, SumsName))
	if err != nil {
		return err
	}
	for _, name := range binaries {
		path := filepath.Join(dir, name)
		if err := sigverify.VerifyFile(path, path+".minisig", publicKey); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		want, ok := sums[name]
		if !ok {
			return fmt.Errorf("%s names no checksum for %s", SumsName, name)
		}
		got, err := store.HashFile(path)
		if err != nil {
			return err
		}
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("%s has the checksum %s and %s says %s", name, got, SumsName, want)
		}
	}
	return nil
}

// readSums reads a SHA256SUMS file. Each line is a checksum, spaces, and a file
// name. A name with a path in it is ignored: the checksum of a file outside the
// release directory means nothing here.
func readSums(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if len(data) > maxSumsBytes {
		return nil, fmt.Errorf("%s is too long", filepath.Base(path))
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// The second field can carry a "*" for a binary read.
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if strings.ContainsAny(name, `/\`) {
			continue
		}
		out[name] = fields[0]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no checksum", filepath.Base(path))
	}
	return out, nil
}

// newLimitReader is io.LimitReader under a name that says why it is here: every
// body that comes from outside gets a limit.
func newLimitReader(r io.Reader, limit int64) io.Reader { return io.LimitReader(r, limit) }
