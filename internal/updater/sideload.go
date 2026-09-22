package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// maxBundleBinary is the largest binary that a sideload bundle may hold. A release
// binary is about 25 MB. The cap stops a file with no end from filling the
// partition while it is read out of an archive (D52).
const maxBundleBinary = 200 << 20

// ArchiveSuffix is the extension of a packed sideload bundle.
const ArchiveSuffix = ".tar.gz"

// Sideload looks in the sideload directory for a release bundle and installs it
// (D52).
//
// A bundle is the three files of a release: the binary of this architecture, its
// minisign signature and SHA256SUMS. They lie loose in the directory, or in a
// directory under it, or in one .tar.gz. Nothing in a bundle carries the version:
// the release assets have the architecture in their names and nothing else. So the
// version comes from the binary itself, after the signature check proved that the
// project made it.
//
// The bundle goes away after the run, whether it worked or not. Without that a
// reboot applies the same bundle again, and a bundle that the device refused
// would fill the ops log at every scan.
//
// Sideload does nothing and gives no error when there is no bundle. The daemon
// calls it at start, at each rescan and every 60 seconds.
func (m *Manager) Sideload(ctx context.Context) error {
	if m.opt.SideloadDir == "" {
		return nil
	}
	// One look at a time. POST /api/rescan starts this in a goroutine and the loop
	// of the updater starts it every 60 seconds, so two calls can meet. The loser
	// used to unpack into the directory that the winner was reading and then remove
	// the bundle under it.
	if !m.sideload.TryLock() {
		return nil
	}
	defer m.sideload.Unlock()

	dir, ok := m.findBundle()
	if !ok {
		return nil
	}

	m.opt.Log("update.sideload", "a release bundle is in "+m.opt.SideloadDir)
	err := m.Apply(ctx, Release{Source: SourceSideload, Dir: dir})
	if errors.Is(err, ErrBusy) {
		// Another update is running, so this bundle was not read at all. The bundle
		// of the person stays where it is and the next pass tries it again.
		return err
	}
	if err != nil {
		m.opt.Log("update.sideload.refused", err.Error()+"; the bundle is removed, so a reboot does not try it again")
	}
	// A run that worked already cleared the bundle before the restart. This call is
	// for the refusal path, and it is safe twice: the directory is then empty.
	m.clearSideload()
	return err
}

// findBundle gives the directory that holds the three files of a bundle.
//
// An archive is unpacked into a directory beside it, so that both shapes reach the
// same code. That writes the binary to the media partition one more time. A
// sideload is a rare step that a person takes by hand, so the write costs nothing
// that matters, and one code path after this point is worth it. The caller removes
// everything in the sideload directory afterwards.
func (m *Manager) findBundle() (string, bool) {
	asset := m.AssetName()
	root := m.opt.SideloadDir

	if hasBundle(root, asset) {
		return root, true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		full := filepath.Join(root, e.Name())
		if e.IsDir() {
			if hasBundle(full, asset) {
				return full, true
			}
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), ArchiveSuffix) {
			continue
		}
		out := full + ".unpacked"
		os.RemoveAll(out)
		if err := unpackBundle(full, out, asset); err != nil {
			m.opt.Log("update.sideload.refused", e.Name()+": "+err.Error())
			os.RemoveAll(out)
			continue
		}
		return out, true
	}
	return "", false
}

// hasBundle reports if a directory holds the three files.
func hasBundle(dir, asset string) bool {
	for _, name := range []string{asset, asset + SigSuffix, SumsName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

// clearSideload empties the sideload directory. The directory itself stays, so a
// person can drop the next bundle in without making it again.
func (m *Manager) clearSideload() {
	entries, err := os.ReadDir(m.opt.SideloadDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(m.opt.SideloadDir, e.Name())); err != nil {
			m.opt.Log("update.sideload.clear.fail", e.Name()+": "+err.Error())
		}
	}
}

// copyBundle copies the three files of a bundle into the staging directory. It
// copies and does not move, because the bundle is on removable storage and the
// staging directory is on the system partition.
func copyBundle(from, staging, asset string) error {
	for _, name := range []string{asset, asset + SigSuffix, SumsName} {
		source := filepath.Join(from, name)
		info, err := os.Stat(source)
		if err != nil {
			return fmt.Errorf("the bundle holds no %s: %w", name, err)
		}
		if info.Size() > maxBundleBinary {
			return fmt.Errorf("%s in the bundle is larger than %d MB", name, maxBundleBinary>>20)
		}
		if err := fsutil.CopyFileSync(source, filepath.Join(staging, name)); err != nil {
			return err
		}
	}
	return nil
}

// unpackBundle reads the three files out of a .tar.gz into out.
//
// The archive comes from removable storage, so every entry is checked: the name
// must be one of the three names with no directory in front of it, the entry must
// be a plain file, and the size must be under the cap. A link, a device node, an
// absolute name and a name with ".." in it are all refused.
func unpackBundle(archive, out, asset string) error {
	want := map[string]bool{asset: true, asset + SigSuffix: true, SumsName: true}

	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("this file is not a gzip archive: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	found := 0
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read the archive: %w", err)
		}
		// path.Base is not enough on its own: a name of "../x" would become "x" and
		// would pass. The name must be exactly one of the three names.
		name := header.Name
		if strings.HasPrefix(name, "./") {
			name = name[2:]
		}
		if name != path.Base(name) || !want[name] {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("%s in the archive is not a plain file", name)
		}
		if header.Size > maxBundleBinary {
			return fmt.Errorf("%s in the archive is larger than %d MB", name, maxBundleBinary>>20)
		}
		if err := writeLimited(filepath.Join(out, name), reader, header.Size); err != nil {
			return err
		}
		delete(want, name)
		found++
	}
	if found != 3 {
		return fmt.Errorf("the archive holds %d of the three files that a bundle needs", found)
	}
	return nil
}

// writeLimited copies at most size bytes into a file and syncs it.
func writeLimited(dest string, r io.Reader, size int64) error {
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(r, size)); err != nil {
		f.Close()
		os.Remove(dest)
		return fmt.Errorf("write %s: %w", filepath.Base(dest), err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync %s: %w", filepath.Base(dest), err)
	}
	return f.Close()
}
