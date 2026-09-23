package fsutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// chmodFile changes the mode of an open file. It is a variable, because a test
// must be able to make the change fail on a machine that permits it.
var chmodFile = func(f *os.File, perm os.FileMode) error { return f.Chmod(perm) }

// modeRefused reports if err says that the filesystem cannot hold a file mode.
// exFAT answers EPERM, because it takes the mode from the mount options; other
// filesystems of that kind answer ENOTSUP or EINVAL. Windows has no POSIX modes
// at all, so a failure there is always this case.
func modeRefused(err error) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EINVAL)
}

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
	// "operation not permitted". A mode is not worth a lost write there. Every
	// other failure is an error: a file that keeps the mode 0600 of the
	// temporary file when the caller asked for 0644 is a file that another user
	// cannot read, and that fault must not be silent.
	if err := chmodFile(f, perm); err != nil && !modeRefused(err) {
		return fmt.Errorf("set the mode of %s: %w", tmp, err)
	}
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
//
// The copy goes to a temporary file in the directory of dst, and a rename puts it
// at dst, as WriteFileAtomic does. A copy that stops in the middle, from an I/O
// error or a power cut, then leaves no short file at dst. A caller that skips a
// dst that exists, such as the first boot copy of the default media, would keep
// a short file for ever.
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

	dir := filepath.Dir(dst)
	out, err := os.CreateTemp(dir, filepath.Base(dst)+".tmp*")
	if err != nil {
		return fmt.Errorf("create a temporary file for %s: %w", dst, err)
	}
	tmp := out.Name()
	done := false
	defer func() {
		if !done {
			out.Close()
			os.Remove(tmp)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	// The temporary file has the mode 0600. See WriteFileAtomic for the
	// filesystems that refuse a mode.
	if err := chmodFile(out, info.Mode().Perm()); err != nil && !modeRefused(err) {
		return fmt.Errorf("set the mode of %s: %w", tmp, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, dst, err)
	}
	done = true
	SyncDir(dir)
	return nil
}
