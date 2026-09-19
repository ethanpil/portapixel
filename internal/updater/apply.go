package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethanpil/portapixel/internal/fleet"
	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/sigverify"
	"github.com/ethanpil/portapixel/internal/store"
)

// maxSumsBytes and maxSigBytes are the caps of the two small files. A signature is
// about 200 bytes and a checksum file a few hundred. A body with no end must not
// fill the partition.
const (
	maxSumsBytes = 1 << 16
	maxSigBytes  = 1 << 14
)

// versionTimeout is how long the staged binary may take to print its version.
const versionTimeout = 10 * time.Second

// Apply installs a release. It gives an error and changes nothing when any
// refusal applies (see doc.go).
//
// The steps, in this order:
//
//  1. Refuse a release that this device must not install.
//  2. Stage the three files in <root>/releases/<version>.staging.
//  3. Check the SHA-256 against SHA256SUMS and the minisign signature against the
//     public key of the build. Both must pass, and both pass before the flip.
//  4. Rename the staging directory to <root>/releases/<version>.
//  5. Point previous at the target of current.
//  6. Write the pending marker, then flip current.
//  7. Remove every release that is neither current nor previous.
//  8. Ask the service to restart.
func (m *Manager) Apply(ctx context.Context, rel Release) error {
	if !m.take() {
		return ErrBusy
	}
	defer m.release()

	if err := m.apply(ctx, rel); err != nil {
		m.mu.Lock()
		m.state.State = manifest.UpdateFailed
		m.state.Error = err.Error()
		m.mu.Unlock()
		m.opt.Log("update.fail", rel.Version+": "+err.Error())
		return err
	}
	return nil
}

func (m *Manager) apply(ctx context.Context, rel Release) error {
	// The name of a tag and the name of the build that came out of it must be one
	// name before any refusal reads it (see NormalizeVersion).
	rel.Version = NormalizeVersion(rel.Version)
	if err := m.refuse(rel); err != nil {
		return err
	}

	staging := filepath.Join(m.releasesDir(), rel.Version+".staging")
	if rel.Version == "" {
		// A sideload does not know the version until the binary is verified, so it
		// stages under a name of its own.
		staging = filepath.Join(m.releasesDir(), ".sideload.staging")
	}
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("remove the old staging directory: %w", err)
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return fmt.Errorf("make %s: %w", staging, err)
	}
	defer os.RemoveAll(staging)

	m.setState(manifest.UpdateDownloading, "")
	if err := m.stage(ctx, rel, staging); err != nil {
		return err
	}

	m.setState(manifest.UpdateVerifying, "")
	version, err := m.verify(staging, rel)
	if err != nil {
		return err
	}
	// A sideload learns its version here. Every refusal that needs the version
	// runs again now, before anything moves.
	if rel.Version == "" {
		rel.Version = version
		if err := m.refuse(rel); err != nil {
			return err
		}
	} else if version != "" && version != rel.Version {
		return fmt.Errorf("the release says it is %s and the source called it %s", version, rel.Version)
	}

	m.setState(manifest.UpdateApplying, "")
	if err := m.install(staging, rel.Version); err != nil {
		return err
	}
	m.prune(rel.Version)

	m.mu.Lock()
	m.state.State = manifest.UpdateRestarting
	m.state.Available = rel.Version
	m.state.Source = rel.Source
	m.state.Error = ""
	m.mu.Unlock()

	m.opt.Log("update.apply", rel.Version+" from "+rel.Source+"; the service restarts and has to write a health marker")
	if m.opt.Restart == nil {
		return nil
	}
	if err := m.opt.Restart(); err != nil {
		return fmt.Errorf("the service did not restart: %w", err)
	}
	return nil
}

// refuse holds every rule that stops an update before anything moves.
func (m *Manager) refuse(rel Release) error {
	if m.opt.PublicKey == "" {
		return ErrNoKey
	}
	// A release of another source than the one that this device may use now. A
	// check and an apply are minutes apart, and a device that paired between the
	// two must install what its server approved and nothing else (D28).
	if rel.Source != "sideload" && m.opt.SourceKind != nil && rel.Source != m.opt.SourceKind() {
		return fmt.Errorf("this device takes its releases from %s now, and %q came from %s: %w",
			m.opt.SourceKind(), rel.Version, rel.Source, ErrNoRelease)
	}
	if rel.Version == "" {
		// A sideload. The version comes after the signature check.
		return nil
	}
	if !ValidVersion(rel.Version) {
		return fmt.Errorf("%q is not a release name that this device accepts", rel.Version)
	}
	if m.opt.IsBadRelease != nil && m.opt.IsBadRelease(rel.Version) {
		return fmt.Errorf("%s: %w", rel.Version, ErrBadRelease)
	}
	// Equal counts as a downgrade. Installing the release that runs would put
	// previous and current on one directory, and the health gate would then have
	// nothing to go back to.
	if CompareVersions(rel.Version, m.opt.Running) <= 0 {
		return fmt.Errorf("%s is not newer than %s: %w", rel.Version, m.opt.Running, ErrDowngrade)
	}
	// The same rule against the link and not against the process. A flip that
	// worked and a restart that did not leaves current on the new release while the
	// old process still runs. A second install would then point previous at the
	// same directory as current, and prune would remove the only release that the
	// gate could go back to.
	if staged := m.currentVersion(); staged != "" && CompareVersions(rel.Version, staged) <= 0 {
		return fmt.Errorf("%s is already staged as the next release: %w", staged, ErrDowngrade)
	}
	if err := m.space(rel.Size); err != nil {
		return err
	}
	return nil
}

// space refuses a release that does not fit. size is 0 when the source does not
// report it, and the reserve alone is then the test.
func (m *Manager) space(size int64) error {
	free, err := m.opt.FreeBytes(m.opt.Root)
	if err != nil {
		// The free space is not known. The download itself reports a full
		// partition, so this is not a reason to stop.
		return nil
	}
	need := uint64(size) + spaceReserve
	if free < need {
		return fmt.Errorf("a release needs %d MB and %d MB are free: %w",
			need>>20, free>>20, ErrNoSpace)
	}
	return nil
}

// stage puts the three files in the staging directory. A download gets the
// checksum file first, so that the binary download can check its own bytes.
//
// The binary is downloaded into the download directory and moved into the staging
// directory afterwards. The staging directory is made new for each attempt, and a
// part file inside it went away with it: a release of 25 MB on a link of 2 Mbit/s
// then started from zero at every attempt and never finished. The download
// directory holds the part file across attempts, so a slow link needs many attempts
// and not one long one (D24).
func (m *Manager) stage(ctx context.Context, rel Release, staging string) error {
	asset := m.AssetName()
	if rel.Dir != "" {
		return copyBundle(rel.Dir, staging, asset)
	}

	if err := m.fetchSmall(ctx, rel, rel.SumsURL, filepath.Join(staging, SumsName), maxSumsBytes); err != nil {
		return err
	}
	if err := m.fetchSmall(ctx, rel, rel.SigURL, filepath.Join(staging, asset+SigSuffix), maxSigBytes); err != nil {
		return err
	}
	sums, err := readSums(filepath.Join(staging, SumsName))
	if err != nil {
		return err
	}
	want, ok := sums[asset]
	if !ok {
		return fmt.Errorf("%s of %s names no checksum for %s", SumsName, rel.Version, asset)
	}
	// store.Download checks the SHA-256 of the complete file and renames it only
	// when the value matches (D24). The release mirror of a fleet server needs the
	// device token; GitHub gets no header at all.
	bearer, err := bearerFor(rel, rel.BinaryURL)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.downloadDir(), 0o755); err != nil {
		return fmt.Errorf("make %s: %w", m.downloadDir(), err)
	}
	holding := filepath.Join(m.downloadDir(), asset)
	// A complete file from an attempt that failed after the download holds the bytes
	// of some release, and nothing says which. The part file beside it is the resume
	// and it stays.
	os.Remove(holding)
	if err := store.Download(ctx, m.opt.Client, rel.BinaryURL, bearer, holding, want, rel.Size); err != nil {
		return fmt.Errorf("get %s: %w", asset, err)
	}
	if err := os.Rename(holding, filepath.Join(staging, asset)); err != nil {
		return fmt.Errorf("move %s into the staging directory: %w", asset, err)
	}
	return nil
}

// bearerFor gives the token that one address may see.
//
// The device token is the key to this device on its fleet server. It goes to the
// host of that server and to no other host. A release mirror that answers with a
// redirect to another host, and a GitHub release, both get no header: a token in a
// request to a stranger is a token that the stranger keeps.
func bearerFor(rel Release, address string) (string, error) {
	if rel.Bearer == "" {
		return "", nil
	}
	if _, err := url.Parse(address); err != nil {
		return "", fmt.Errorf("the release address %q is not a URL: %w", address, err)
	}
	if rel.BearerOrigin == "" || !fleet.SameHost(address, rel.BearerOrigin) {
		return "", nil
	}
	return rel.Bearer, nil
}

// verify checks the staged release two ways and gives the version that the binary
// reports.
//
// Two checks are not one check twice. The signature says that the project made
// this file. The checksum file says that this file is the one that the release
// names, which catches a mirror where two releases mixed.
func (m *Manager) verify(staging string, rel Release) (string, error) {
	asset := m.AssetName()
	binary := filepath.Join(staging, asset)

	if _, err := os.Stat(binary); err != nil {
		return "", fmt.Errorf("the release holds no %s: %w", asset, ErrArch)
	}
	if err := sigverify.VerifyFile(binary, binary+SigSuffix, m.opt.PublicKey); err != nil {
		return "", fmt.Errorf("the signature of %s is not valid: %w", asset, err)
	}
	sums, err := readSums(filepath.Join(staging, SumsName))
	if err != nil {
		return "", err
	}
	want, ok := sums[asset]
	if !ok {
		return "", fmt.Errorf("%s names no checksum for %s", SumsName, asset)
	}
	got, err := store.HashFile(binary)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(got, want) {
		return "", fmt.Errorf("%s has the checksum %s and %s says %s", asset, got, SumsName, want)
	}

	// The file is the file of the project. Only now is it safe to run it.
	if err := os.Chmod(binary, 0o755); err != nil {
		return "", fmt.Errorf("set the mode of %s: %w", asset, err)
	}
	// The binary is asked what it is on every path, and not only for a sideload
	// that has no other source for it. The answer is the cross-check against the
	// name that the source gave: a tag and the build that came out of it must
	// agree, or the release installs under a name that the health gate does not
	// wait for.
	info, err := m.opt.BinaryVersion(binary)
	if err != nil {
		return "", fmt.Errorf("the release does not say which version it is: %w", err)
	}
	// The processor is a refusal of its own, with a message of its own. A mirror
	// that holds the asset of another architecture gives a file with a good
	// signature of this project, and that file installs and then never starts.
	if info.Arch != m.opt.Arch {
		return "", fmt.Errorf("this release holds a binary for %s and this device is %s: %w",
			info.Arch, m.opt.Arch, ErrArch)
	}
	version := NormalizeVersion(info.Version)
	if !ValidVersion(version) {
		return "", fmt.Errorf("%q is not a release name that this device accepts", version)
	}
	return version, nil
}

// install moves the staged release into place and flips the links.
//
// The pending marker goes in before the flip. Without it the health gate does
// nothing at all, so a release that never comes up would stay. A flip that fails
// takes the marker away again, because a marker with no flip makes the gate roll
// back a release that works.
func (m *Manager) install(staging, version string) error {
	final := filepath.Join(m.releasesDir(), version)

	// The last lock on the rule of refuse: never install over the directory that
	// current names. previous would then be current, and the gate could go nowhere.
	if m.currentVersion() == version {
		return fmt.Errorf("%s is the release that current names already: %w", version, ErrDowngrade)
	}
	if err := os.RemoveAll(final); err != nil {
		return fmt.Errorf("remove %s: %w", final, err)
	}
	if err := os.Rename(filepath.Join(staging, m.AssetName()), filepath.Join(staging, m.opt.BinaryName)); err != nil {
		return fmt.Errorf("name the binary %s: %w", m.opt.BinaryName, err)
	}
	if err := os.Rename(staging, final); err != nil {
		return fmt.Errorf("move the release into %s: %w", final, err)
	}
	// The bytes of the binary are already on the disk: every path that stages a
	// release syncs the file that it wrote. Only the new directory entry is left,
	// and SyncDir writes that.
	fsutil.SyncDir(m.releasesDir())

	// previous keeps the release that runs now, so the gate can go back to it.
	if old := m.currentTarget(); old != "" {
		if err := m.opt.Flip(filepath.Join(m.opt.Root, PreviousLink), old); err != nil {
			return fmt.Errorf("point %s at %s: %w", PreviousLink, old, err)
		}
	}

	target := ReleasesDir + "/" + version
	if err := os.MkdirAll(m.healthDir(), 0o755); err != nil {
		return fmt.Errorf("make %s: %w", m.healthDir(), err)
	}
	if err := fsutil.WriteFileAtomic(m.pendingPath(), []byte(version+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", PendingFile, err)
	}
	if err := m.opt.Flip(filepath.Join(m.opt.Root, CurrentLink), target); err != nil {
		os.Remove(m.pendingPath())
		return fmt.Errorf("point %s at %s: %w", CurrentLink, target, err)
	}
	fsutil.SyncDir(m.opt.Root)
	return nil
}

// currentTarget reads the target of the current link, for example
// "releases/1.4.0".
func (m *Manager) currentTarget() string { return m.linkTarget(CurrentLink) }

// currentVersion gives the release that the current link names, or "".
func (m *Manager) currentVersion() string {
	return strings.TrimPrefix(m.currentTarget(), ReleasesDir+"/")
}

// prune removes every release directory that is neither current nor previous nor
// the release that runs. A device has 3.5 GB for the whole system, so two releases
// is the whole history that it keeps (plan section 15).
//
// The release that runs is in the list for a reason. A flip that worked and a
// restart that did not leaves current on the new release while the old process is
// still the process that serves. Removing its directory takes the binary out from
// under the service, and the health gate then has nothing to go back to.
func (m *Manager) prune(keep string) {
	previous := strings.TrimPrefix(m.linkTarget(PreviousLink), ReleasesDir+"/")
	entries, err := os.ReadDir(m.releasesDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || name == keep || name == previous || name == m.opt.Running {
			continue
		}
		if strings.HasPrefix(name, ".") {
			continue // the staging and the download directories of this package
		}
		if strings.HasSuffix(name, ".staging") {
			continue // a staging directory of a run that is going on
		}
		if err := os.RemoveAll(filepath.Join(m.releasesDir(), name)); err != nil {
			m.opt.Log("update.prune.fail", name+": "+err.Error())
			continue
		}
		m.opt.Log("update.prune", "the old release "+name+" is removed")
	}
}

// linkTarget reads one of the two links. It gives "" when the link is not there,
// which is the state of a development tree.
//
// A link that Options.Flip wrote as a plain file is read as well. Windows without
// the developer mode cannot make a symlink, so a test there gives a Flip that
// writes the target into a file, and this function must read both shapes or the
// test covers less code than the device runs.
func (m *Manager) linkTarget(name string) string {
	path := filepath.Join(m.opt.Root, name)
	if target, err := os.Readlink(path); err == nil {
		return filepath.ToSlash(strings.TrimSpace(target))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// CheckRollback looks for the marker that the health gate leaves behind after a
// rollback. It records the release as bad, so the updater never offers it again,
// and it reports the version that failed.
//
// The daemon calls it at start, before it serves anything. The gate has already
// put current back, so the release that runs is the good one (plan section 15).
func (m *Manager) CheckRollback() (string, bool) {
	entries, err := os.ReadDir(m.healthDir())
	if err != nil {
		return "", false
	}
	found := ""
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, BadSuffix) {
			continue
		}
		bad := strings.TrimSuffix(name, BadSuffix)
		if bad == "" {
			continue
		}
		if m.opt.MarkBadRelease != nil {
			m.opt.MarkBadRelease(bad)
		}
		m.opt.Log("update.rolled-back", bad+" did not write a health marker; the device runs "+m.opt.Running+" and never installs "+bad+" again")
		found = bad
		// The marker is read one time. MarkBadRelease put the release in the state
		// file, which is what refuses it for ever, so the file has done its work. A
		// marker that stayed made the rollback banner come back at every boot for the
		// life of the device.
		if err := os.Remove(filepath.Join(m.healthDir(), name)); err != nil {
			m.opt.Log("update.rolled-back.clear.fail", name+": "+err.Error())
		}
	}
	if found == "" {
		return "", false
	}
	m.mu.Lock()
	m.state.State = manifest.UpdateRolledBack
	m.state.Error = found + " did not come up, so the device went back to " + m.opt.Running
	m.mu.Unlock()
	return found, true
}

// fetchSmall gets one small file. It is for the signature and the checksum file:
// the binary goes through store.Download, which checks a SHA-256 and can continue
// a broken download.
func (m *Manager) fetchSmall(ctx context.Context, rel Release, address, dest string, limit int64) error {
	if address == "" {
		return fmt.Errorf("the release names no address for %s", filepath.Base(dest))
	}
	// A deadline for this one request. The client has no overall timeout, because
	// the same client downloads the release binary.
	ctx, cancel := context.WithTimeout(ctx, smallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	bearer, err := bearerFor(rel, address)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := m.opt.Client.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", filepath.Base(dest), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get %s: the server answered %s", filepath.Base(dest), resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(dest), err)
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("%s is longer than %d bytes", filepath.Base(dest), limit)
	}
	return fsutil.WriteFileAtomic(dest, data, 0o644)
}

// readSums reads a SHA256SUMS file. Each line is a checksum, whitespace and a
// file name. A name with a path in it is ignored: the checksum of a file outside
// the release means nothing here.
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
		// The second field can carry a "*" for a read in binary mode.
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

// FlipSymlink points a link at a target in one step: it makes a link under
// another name and renames it over the old one. A rename of a link is atomic on
// every filesystem that we use, so a power cut in the middle leaves the old link
// and never half a link.
//
// The target is relative, for example "releases/1.5.0". install.sh writes the same
// shape and health-gate.sh reads it with readlink, so an absolute path here would
// break a --root install.
func FlipSymlink(link, target string) error {
	staging := link + ".new"
	if err := os.Remove(staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, staging); err != nil {
		return err
	}
	if err := os.Rename(staging, link); err != nil {
		os.Remove(staging)
		return err
	}
	fsutil.SyncDir(filepath.Dir(link))
	return nil
}
