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
// release bundle holds five files. An archive with thousands of members is not
// one of ours.
const maxBundleEntries = 64

// ErrBadBundle says that the upload is not a release bundle that we accept.
var ErrBadBundle = errors.New("this file is not a release bundle")

// InstallBundle takes a release bundle that the admin uploaded and mirrors the
// version from it. It is the path for a network that cannot reach GitHub (D28).
//
// The bundle is a .tar.gz or a .zip that holds the same files that the GitHub
// release holds. The check after the extraction is the same check: signature and
// checksum, or the version stays unmirrored.
//
// The names in an archive are input from outside. A name with a parent step in
// it, an absolute name, or a name with a drive letter stops the whole upload:
// somebody made that archive to write outside the release directory, and the
// safe answer is to accept nothing from it.
func (m *Mirror) InstallBundle(v string, body io.Reader, fileName string) error {
	if !ValidVersion(v) {
		return fmt.Errorf("%q is not a version name", v)
	}
	m.Report(v, stateWorking, "")
	err := m.installBundle(v, body, fileName)
	if err != nil {
		m.Report(v, stateFailed, err.Error())
		return err
	}
	m.Report(v, stateDone, "")
	return nil
}

func (m *Mirror) installBundle(v string, body io.Reader, fileName string) error {
	if m.PublicKey == "" {
		return ErrNoKey
	}
	dir := m.VersionDir(v)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}

	lower := strings.ToLower(fileName)
	var err error
	switch {
	case strings.HasSuffix(lower, ".zip"):
		err = extractZip(body, dir)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		err = extractTarGz(body, dir)
	default:
		return fmt.Errorf("%w: the name must end with .tar.gz, .tgz or .zip", ErrBadBundle)
	}
	if err != nil {
		return err
	}
	for _, name := range Files() {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
			return fmt.Errorf("%w: it holds no file %s", ErrBadBundle, name)
		}
	}
	return Verify(dir, m.PublicKey)
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
// file, so the body goes to a temporary file first.
func extractZip(body io.Reader, dir string) error {
	tmp, err := os.CreateTemp(dir, "bundle*.zip")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	// The limit is the sum of the limits of the files, which is what a bundle of
	// our own can hold.
	limit := int64(maxSumsBytes + len(binaries)*(maxBinaryBytes+maxSigBytes))
	n, err := io.Copy(tmp, io.LimitReader(body, limit+1))
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
		if f.FileInfo().IsDir() {
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

// writeMember writes one member of an archive. It takes one byte more than the
// limit, so a member that is too long is an error and not a file that got cut.
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

	n, err := io.Copy(tmp, io.LimitReader(src, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("%w: %s is longer than the limit of %d bytes", ErrBadBundle, filepath.Base(dest), limit)
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
