package fsutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// chmodFile changes the mode of an open file. It is a variable, because a test
// must be able to make the change fail on a machine that permits it.
var chmodFile = func(f *os.File, perm os.FileMode) error { return f.Chmod(perm) }

// WriteFileAtomic writes data to path in a way that a power cut cannot damage.
// It writes a temporary file in the same directory, syncs it, and then renames
// it onto path. The rename is the commit. After the rename it syncs the parent
// directory, so that the new name is also on the disk.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("make temporary file for %s: %w", path, err)
	}
	tmp := f.Name()
	// Remove the temporary file if any step before the rename fails.
	done := false
	defer func() {
		if !done {
			f.Close()
			os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	// A filesystem that has no file modes refuses this change. PPMEDIA is such a
	// filesystem: exFAT takes the mode from the mount options and answers
	// "operation not permitted". A mode is not worth a lost write, so a refusal
	// is not an error here. os.CreateTemp makes the file with mode 0600, so the
	// file that stays after a refusal has the safe mode, not a wide one.
	chmodFile(f, perm)
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}
	done = true
	SyncDir(dir)
	return nil
}

// SyncDir asks the operating system to write the directory entries of dir to
// the disk. It is a best effort step: Windows gives no way to sync a directory,
// and some filesystems refuse the call. A failure here does not make the data
// wrong, so SyncDir reports nothing.
func SyncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	d.Sync()
	d.Close()
}

// CopyFileSync copies src to dst and syncs dst before it returns. The caller
// gets a complete file on the disk or an error.
func CopyFileSync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("sync %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}
	return nil
}
