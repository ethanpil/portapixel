package releases

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/sigverify"
	"github.com/ethanpil/portapixel/internal/store"
	"github.com/ethanpil/portapixel/internal/updater"
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

// The timeouts of a download.
//
// There is no whole-request timeout. A 200 MB binary over a slow line takes
// minutes, and a deadline on the whole transfer would give up on a download that
// works. These three cover the ways that a connection can stop without sending
// anything: the dial, the TLS handshake and the wait for the first header.
const (
	dialTimeout           = 15 * time.Second
	tlsHandshakeTimeout   = 15 * time.Second
	responseHeaderTimeout = 30 * time.Second
	// byteRate is the slowest transfer that the mirror accepts, in bytes a
	// second. The deadline of a download is its size limit divided by this, so a
	// connection that completes the handshake and then stalls for ever ends.
	byteRate = 16 << 10
)

// ErrNoKey says that this build holds no minisign public key, so it cannot check
// a release. A development build is in that state (internal/version.PublicKey).
var ErrNoKey = errors.New("this build has no release key, so it cannot verify a release")

// ErrBusy says that a mirror already runs.
var ErrBusy = errors.New("a mirror already runs")

// ErrNoSpace says that the data directory has not enough room for the work.
var ErrNoSpace = errors.New("the disk has not enough room for this release")

// spaceReserve is the room that a release job must leave. It is the reserve of the
// media store, so that a mirror cannot fill the disk under the media library and
// under the database.
const spaceReserve = 512 << 20

// needRoom refuses the work when the disk has not enough room for want bytes and
// the reserve on top.
//
// fsutil.FreeBytes answers a large constant on a system that it cannot measure. The
// check then always passes, which is the old behaviour and is correct: a guess must
// not stop a job that would work.
func (m *Mirror) needRoom(want int64) error {
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return fmt.Errorf("make %s: %w", m.Dir, err)
	}
	free, err := fsutil.FreeBytes(m.Dir)
	if err != nil {
		return nil
	}
	if int64(free) < want+spaceReserve {
		return fmt.Errorf("%w: it needs %d MB and %d MB are free",
			ErrNoSpace, (want+spaceReserve)>>20, free>>20)
	}
	return nil
}

// defaultDownloadHosts holds the hosts that a release file may come from.
//
// The URL of a file comes out of the GitHub API answer. That answer is input from
// outside, and a repository that somebody else controls can name any host in it. A
// server that followed such a URL would fetch a file of a stranger and would then
// hand it to the fleet. The signature check would refuse it, so this is the second
// wall and not the first, but a download to an address of somebody's choice is a
// request that we do not have to make.
var defaultDownloadHosts = []string{"github.com", "api.github.com", ".githubusercontent.com"}

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
	// DownloadHosts names the hosts that a release file may come from. A test
	// replaces it, because its stub GitHub answers on the loopback.
	DownloadHosts []string
	// AllowHTTP permits a release file over plain HTTP. Only a test sets it: its
	// stub GitHub has no certificate. In the server it is always false, so the
	// https rule below is a rule and not a comment.
	AllowHTTP bool

	mu sync.Mutex
	// jobs counts the background runs, so that a shutdown can wait for the last
	// report to reach the database.
	jobs sync.WaitGroup
	// working names the version that the mirror or a bundle install works on.
	working string
	// run counts the runs. A report of a run that finished after a later one
	// started is ignored: a late failure must never clear a good install.
	run int64
	// lastDone is the highest run number that finished.
	lastDone int64
}

// NewMirror makes a Mirror under dataDir.
func NewMirror(dataDir, repo string) *Mirror {
	return &Mirror{
		Dir:    filepath.Join(dataDir, "releases"),
		Lister: NewLister(repo),
		Client: &http.Client{
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
				TLSHandshakeTimeout:   tlsHandshakeTimeout,
				ResponseHeaderTimeout: responseHeaderTimeout,
			},
		},
		PublicKey:     version.PublicKey,
		Report:        func(string, string, string) {},
		DownloadHosts: defaultDownloadHosts,
	}
}

// VersionDir gives the directory of one version. The caller checks the version
// first with ValidVersion.
func (m *Mirror) VersionDir(v string) string { return filepath.Join(m.Dir, v) }

// ValidVersion reports if a version name is safe as a directory name and in a
// URL.
//
// It is the rule of internal/updater, so that the name that the server mirrors and
// the name that the device installs are the same name. Two rules drifted apart
// once: the server took a leading full stop, which would hide the directory, and
// it refused "+", which a build metadata tag holds.
func ValidVersion(v string) bool { return updater.ValidVersion(v) }

// Working gives the version that the mirror works on, or an empty value.
func (m *Mirror) Working() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.working
}

// begin takes the working lock for one version. It gives the run number and a
// function that gives the lock back.
//
// The bundle install takes the same lock as the download. Without that, an upload
// and a download of one version would write the same directory at the same time,
// and the set of files that Verify then reads would be a mixture of the two.
func (m *Mirror) begin(v string) (int64, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.working != "" {
		return 0, nil, ErrBusy
	}
	m.working = v
	m.run++
	id := m.run
	return id, func() {
		m.mu.Lock()
		m.working = ""
		if id > m.lastDone {
			m.lastDone = id
		}
		m.mu.Unlock()
	}, nil
}

// report writes a state, unless a later run finished first.
//
// A download that fails long after a bundle install succeeded must not clear the
// install. The run number says which of the two spoke last.
func (m *Mirror) report(id int64, v, state, errText string) {
	m.mu.Lock()
	stale := id != 0 && id < m.lastDone
	m.mu.Unlock()
	if stale {
		return
	}
	m.Report(v, state, errText)
}

// Start runs a mirror in the background. The admin route calls it: a download of
// two binaries takes longer than a request may take.
//
// ctx is the life of the server. A shutdown cancels the download, so the process
// does not wait for a transfer that nobody reads any more.
func (m *Mirror) Start(ctx context.Context, v string) error {
	if !ValidVersion(v) {
		return fmt.Errorf("%q is not a version name", v)
	}
	id, done, err := m.begin(v)
	if err != nil {
		return err
	}

	m.jobs.Add(1)
	go func() {
		defer m.jobs.Done()
		defer done()
		m.run1(ctx, id, v)
	}()
	return nil
}

// Wait blocks until every background run has reported. The server calls it before
// it closes the database: the last report of a cancelled run writes a row, and a
// closed pool cannot take it.
func (m *Mirror) Wait() { m.jobs.Wait() }

// Run mirrors one version and reports what happened. It gives the error as well,
// so a test and the selftest can read it.
func (m *Mirror) Run(ctx context.Context, v string) error {
	id, done, err := m.begin(v)
	if err != nil {
		return err
	}
	defer done()
	return m.run1(ctx, id, v)
}

func (m *Mirror) run1(ctx context.Context, id int64, v string) error {
	// The working state is not reported for a version that is already mirrored.
	//
	// Both device gates read that state: the manifest stops naming the release and
	// the download route answers 404. A re-mirror of the version that runs would
	// otherwise take the live release off the air before it fetched one byte, and a
	// failure would leave it off the air although the verified files never moved.
	// The UI learns that work runs from the "mirroring" field of the releases route.
	if !m.haveVersion(v) {
		m.report(id, v, MirrorWorking, "")
	}
	err := m.mirror(ctx, v)
	if err != nil {
		if m.haveVersion(v) {
			// The verified files of the last run are still there, so the release stays
			// on the air and the admin reads why the new attempt failed.
			m.report(id, v, MirrorDone, err.Error())
			return err
		}
		m.report(id, v, MirrorFailed, err.Error())
		return err
	}
	m.report(id, v, MirrorDone, "")
	return nil
}

// haveVersion reports if the directory of a version is there with its files.
func (m *Mirror) haveVersion(v string) bool {
	if !ValidVersion(v) {
		return false
	}
	for _, name := range Files() {
		if _, err := os.Stat(filepath.Join(m.VersionDir(v), name)); err != nil {
			return false
		}
	}
	return true
}

// The state words. They are the words of the mirror_state column of the releases
// table, and internal/server/db holds the same constants. This package cannot
// import that one, so a test keeps the two sets equal.
const (
	MirrorWorking = "working"
	MirrorDone    = "done"
	MirrorFailed  = "failed"
)

// mirror downloads every file of the release and verifies it.
//
// The files go to a staging directory and the directory of the version takes them
// at the end, after Verify passes. A download straight into the version directory
// would serve a half-written set of files to any device that polled in the middle,
// and a failed run would leave the binary of one release beside the checksums of
// another.
func (m *Mirror) mirror(ctx context.Context, v string) error {
	if !ValidVersion(v) {
		return fmt.Errorf("%q is not a version name", v)
	}
	if m.PublicKey == "" {
		return ErrNoKey
	}
	// Two binaries at their limit, plus the reserve. The releases directory is on the
	// same filesystem as the media store and the database, and the reserve of the
	// media store does not cover this path.
	if err := m.needRoom(int64(len(binaries)) * maxBinaryBytes); err != nil {
		return err
	}
	rel, err := m.Lister.Release(ctx, v)
	if err != nil {
		return err
	}

	staging, err := m.staging(v)
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	for _, name := range Files() {
		url, ok := rel.Assets[name]
		if !ok {
			return fmt.Errorf("the release %s has no file %s", v, name)
		}
		if err := m.download(ctx, url, filepath.Join(staging, name), limitFor(name)); err != nil {
			return err
		}
	}
	if err := Verify(staging, m.PublicKey); err != nil {
		return err
	}
	return m.commit(staging, v)
}

// staging makes a scratch directory beside the version directories. The name
// starts with a full stop, and ValidVersion refuses such a name, so a staging
// directory can never be read as a version.
func (m *Mirror) staging(v string) (string, error) {
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return "", fmt.Errorf("make %s: %w", m.Dir, err)
	}
	dir, err := os.MkdirTemp(m.Dir, ".staging-"+v+"-*")
	if err != nil {
		return "", fmt.Errorf("make a staging directory in %s: %w", m.Dir, err)
	}
	return dir, nil
}

// commit puts the staged files in place of the version directory.
//
// A rename of the whole directory is not possible when the old one is there, and a
// remove before the rename would take a working mirror off the air if the rename
// then failed. So the old directory moves aside first, the new one takes its
// place, and the old one goes after that.
func (m *Mirror) commit(staging, v string) error {
	dest := m.VersionDir(v)
	old := ""
	if _, err := os.Stat(dest); err == nil {
		old = dest + ".old"
		os.RemoveAll(old)
		if err := os.Rename(dest, old); err != nil {
			return fmt.Errorf("move %s aside: %w", dest, err)
		}
	}
	if err := os.Rename(staging, dest); err != nil {
		if old != "" {
			// The rollback error matters: when it fails, the only verified copy stays
			// at <version>.old, and the admin must know that. CleanStaging brings such
			// a copy back at the next start.
			if back := os.Rename(old, dest); back != nil {
				return fmt.Errorf("put %s in place: %w (and the copy stays at %s: %v)",
					dest, err, filepath.Base(old), back)
			}
		}
		return fmt.Errorf("put %s in place: %w", dest, err)
	}
	if old != "" {
		os.RemoveAll(old)
	}
	fsutil.SyncDir(m.Dir)
	return nil
}

// CleanStaging tidies the release directory after a stopped process. The server
// calls it at start.
//
// A staging directory holds files that Verify never passed, so it goes. A ".old"
// directory is the OPPOSITE: it is the copy that Verify did pass, parked between
// the two renames of commit. When the version directory is missing, that copy is
// the only one there is, and on a closed network it came from a bundle that nobody
// can fetch again. So it comes back instead of going away.
func (m *Mirror) CleanStaging() {
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() {
			continue
		}
		switch {
		case strings.HasPrefix(name, ".staging-"):
			os.RemoveAll(filepath.Join(m.Dir, name))
		case strings.HasSuffix(name, ".old"):
			m.recoverOld(name)
		}
	}
}

// recoverOld answers one ".old" directory. See CleanStaging.
func (m *Mirror) recoverOld(name string) {
	path := filepath.Join(m.Dir, name)
	base := strings.TrimSuffix(name, ".old")
	if !ValidVersion(base) {
		// Not the aside copy of a version that we could serve.
		os.RemoveAll(path)
		return
	}
	dest := m.VersionDir(base)
	if _, err := os.Stat(dest); err == nil {
		// The second rename of commit finished. The aside copy is the old one.
		os.RemoveAll(path)
		return
	}
	if err := os.Rename(path, dest); err != nil {
		// Leave it where it is. A copy that we cannot move is still a copy, and the
		// admin can mirror the version again.
		return
	}
	fsutil.SyncDir(m.Dir)
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
func (m *Mirror) download(ctx context.Context, rawURL, dest string, limit int64) error {
	if err := m.allowedURL(rawURL); err != nil {
		return err
	}
	// The deadline is the size limit at the slowest rate that we accept. A
	// transfer that works finishes long before it; a transfer that stalls after
	// the headers ends here.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(limit/byteRate+30)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	client := *m.Client
	// A redirect must not leave the hosts of the allowlist either. GitHub answers
	// a download with a redirect to its object store, so redirects stay on.
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return m.allowedURL(r.URL.String())
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get %s: the server answered %s", rawURL, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return writeMember(dest, resp.Body, limit)
}

// allowedURL refuses a URL that is not https on one of the hosts of the
// allowlist.
func (m *Mirror) allowedURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%q is not a URL: %w", rawURL, err)
	}
	hosts := m.DownloadHosts
	host := strings.ToLower(u.Hostname())
	for _, allowed := range hosts {
		allowed = strings.ToLower(allowed)
		if strings.HasPrefix(allowed, ".") {
			if strings.HasSuffix(host, allowed) {
				return m.checkScheme(u, rawURL)
			}
			continue
		}
		if host == allowed {
			return m.checkScheme(u, rawURL)
		}
	}
	return fmt.Errorf("a release file may not come from %s", u.Host)
}

// checkScheme refuses a URL that is not https.
//
// AllowHTTP is the one way past it, and only a test sets that field. The rule used
// to read DownloadHosts instead, which NewMirror always fills, so the rule never
// ran in the server: a GitHub answer that named a plain http URL, and a redirect
// from https to http, were both followed.
func (m *Mirror) checkScheme(u *url.URL, rawURL string) error {
	if u.Scheme == "https" || m.AllowHTTP {
		return nil
	}
	return fmt.Errorf("a release file must come over https, and %s does not", rawURL)
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

// limitReader is io.LimitReader. It is here so that every body from outside has
// one place that puts a limit on it.
func limitReader(r io.Reader, limit int64) io.Reader { return io.LimitReader(r, limit) }
