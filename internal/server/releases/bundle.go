package releases

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// maxBundleEntries is the largest number of members that a bundle may hold. A
// release bundle holds five files. An archive with thousands of members is not one
// of ours.
const maxBundleEntries = 64

// ErrBadBundle says that the upload is not a release bundle that we accept.
var ErrBadBundle = errors.New("this file is not a release bundle")

// InstallBundle takes a release bundle that the admin uploaded and mirrors the
// version from it. It is the path for a network that cannot reach GitHub (D28).
//
// The bundle is a .tar.gz or a .zip that holds the same files as the GitHub
// release. The check after the extraction is the same check. The signature and the
// checksum must both pass, or the version stays unmirrored.
//
// The names in an archive are input from outside. A name with a parent step in it,
// an absolute name, or a name with a drive letter stops the whole upload.
// Somebody made that archive to write outside the release directory, and the safe
// answer is to accept nothing from it.
//
// It takes the same working lock as the download and it stages in the same way. An
// upload and a download of one version would otherwise write one directory
// together, and the files that Verify then reads would be a mixture of the two.
func (m *Mirror) InstallBundle(v string, body io.Reader, fileName string) error {
	if !ValidVersion(v) {
		return fmt.Errorf("%q is not a version name", v)
	}
	// The checks that need no work come first. A report writes a releases row, and
	// nothing removes such a row, so a request that never started work must not make
	// a version appear on the Versions page.
	if m.PublicKey == "" {
		return ErrNoKey
	}
	kind, err := bundleKind(fileName)
	if err != nil {
		return err
	}
	// A zip goes to a temporary file first and then extracts, so the worst case is
	// the archive and its contents at the same time.
	if err := m.needRoom(2 * bundleLimit()); err != nil {
		return err
	}

	id, done, err := m.begin(v)
	if err != nil {
		return err
	}
	defer done()

	if !m.haveVersion(v) {
		m.report(id, v, MirrorWorking, "")
	}
	err = m.installBundle(v, body, kind)
	if err != nil {
		if m.haveVersion(v) {
			m.report(id, v, MirrorDone, err.Error())
			return err
		}
		m.report(id, v, MirrorFailed, err.Error())
		return err
	}
	m.report(id, v, MirrorDone, "")
	return nil
}

// bundleKind reads the archive kind out of the file name of an upload.
func bundleKind(fileName string) (string, error) {
	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return "zip", nil
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tar.gz", nil
	}
	return "", fmt.Errorf("%w: the name must end with .tar.gz, .tgz or .zip", ErrBadBundle)
}

// bundleLimit is the largest archive that a bundle upload reads: the sum of the
// limits of the files that a bundle of our own holds.
func bundleLimit() int64 {
	return int64(maxSumsBytes + len(binaries)*(maxBinaryBytes+maxSigBytes))
}

func (m *Mirror) installBundle(v string, body io.Reader, kind string) error {
	staging, err := m.staging(v)
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	// One limit for the whole body, whatever the archive kind is. The zip path had
	// its own; the tar.gz path had none at all, so a member with a huge declared
	// size was decompressed and thrown away one block at a time.
	limit := bundleLimit()
	counted := &countingReader{src: limitReader(body, limit+1)}
	switch kind {
	case "zip":
		err = extractZip(counted, staging, limit)
	default:
		err = extractTarGz(counted, staging)
	}
	if err != nil {
		return err
	}
	if counted.n > limit {
		return fmt.Errorf("%w: it is longer than %d bytes", ErrBadBundle, limit)
	}
	for _, name := range Files() {
		if _, statErr := os.Stat(filepath.Join(staging, name)); statErr != nil {
			return fmt.Errorf("%w: it holds no file %s", ErrBadBundle, name)
		}
	}
	if err := Verify(staging, m.PublicKey); err != nil {
		return err
	}
	return m.commit(staging, v)
}

// extractTarGz writes the wanted members of a tar.gz into dir.
func extractTarGz(body io.Reader, dir string) error {
	gz, err := gzip.NewReader(body)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadBundle, err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for i := 0; ; i++ {
		head, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrBadBundle, err)
		}
		if i >= maxBundleEntries {
			return fmt.Errorf("%w: it holds more than %d members", ErrBadBundle, maxBundleEntries)
		}
		name, ok, err := memberName(head.Name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if head.Typeflag != tar.TypeReg {
			// A link, a device or a directory with the name of a release file.
			return fmt.Errorf("%w: %s is not a plain file", ErrBadBundle, head.Name)
		}
		if err := writeMember(filepath.Join(dir, name), tr, limitFor(name)); err != nil {
			return err
		}
	}
}

// extractZip writes the wanted members of a zip into dir. A zip needs the whole
// file, so the body goes to a temporary file first. The caller limits the body;
// limit says how many bytes of it are permitted.
func extractZip(body io.Reader, dir string, limit int64) error {
	tmp, err := os.CreateTemp(dir, "bundle*.zip")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	n, err := io.Copy(tmp, body)
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("%w: it is longer than %d bytes", ErrBadBundle, limit)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}

	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadBundle, err)
	}
	if len(zr.File) > maxBundleEntries {
		return fmt.Errorf("%w: it holds more than %d members", ErrBadBundle, maxBundleEntries)
	}
	for _, f := range zr.File {
		name, ok, err := memberName(f.Name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		// Not only a directory: a zip holds the mode of a member in its external
		// attributes, so it can name a symbolic link as well. The tar path refuses
		// every member that is not a plain file, and this one now says the same.
		if !f.FileInfo().Mode().IsRegular() {
			return fmt.Errorf("%w: %s is not a plain file", ErrBadBundle, f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrBadBundle, err)
		}
		err = writeMember(filepath.Join(dir, name), rc, limitFor(name))
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// memberName checks the name of an archive member and says which release file it
// is. It gives false for a member that we do not want, and an error for a name
// that tries to leave the release directory.
func memberName(raw string) (string, bool, error) {
	name := strings.ReplaceAll(raw, `\`, "/")
	if name == "" || strings.Contains(name, "\x00") {
		return "", false, fmt.Errorf("%w: it holds a member with no name", ErrBadBundle)
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", false, fmt.Errorf("%w: the member name %q is not a relative path", ErrBadBundle, raw)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", false, fmt.Errorf("%w: the member name %q goes outside the bundle", ErrBadBundle, raw)
		}
	}
	base := path.Base(path.Clean(name))
	if !IsMirrorFile(base) {
		// A README or a licence file in the bundle. Leave it.
		return "", false, nil
	}
	return base, true, nil
}

// countingReader counts the bytes that came out of a reader. The bundle path uses
// it to tell "the archive ended" from "the archive reached the limit": a limited
// reader answers io.EOF for both, and a gzip or zip reader then reports a damaged
// archive instead of one that is too long.
type countingReader struct {
	src io.Reader
	n   int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.n += int64(n)
	return n, err
}

// writeMember writes one file to dest through a temporary file. It takes one byte
// more than the limit, so a body that is too long is an error and not a file that
// got cut. The bundle extraction and the download both call it.
func writeMember(dest string, src io.Reader, limit int64) error {
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

	n, err := io.Copy(tmp, limitReader(src, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("%s is longer than the limit of %d bytes", filepath.Base(dest), limit)
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
