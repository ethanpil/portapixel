package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// blockSize is the size of one read and one write of a partition copy. 4 MiB is
// large enough that the disk works in long runs and small enough that the progress
// moves and the memory stays flat on a device with 512 MB.
const blockSize = 4 << 20

// syncEvery is how many bytes a copy writes between two fsync calls. Without it the
// page cache holds hundreds of megabytes, and the last write then takes minutes
// while the progress bar stands at 100 per cent.
const syncEvery = 64 << 20

// reportEvery is the shortest time between two progress events. A stream that sends
// an event for each block sends a thousand events a second.
const reportEvery = 500 * time.Millisecond

// openTarget opens a partition or a disk for writing. It never makes the file.
//
// The flag O_CREATE was here once, and it made a whole install that wrote nothing.
// The kernel needs a moment to make the partition nodes after the new table, and a
// write to /dev/sdb1 before that moment made an ordinary file on devtmpfs, which is
// memory. Every step after it worked, the UI said "Power the machine off", and the
// disk held an empty partition table.
//
// On the device the target must be a block device as well. See isBlockDevice.
func openTarget(to string) (*os.File, error) {
	info, err := os.Stat(to)
	if err != nil {
		return nil, fmt.Errorf("%s is not there: %w", to, err)
	}
	if err := isBlockDevice(to, info); err != nil {
		return nil, err
	}
	out, err := os.OpenFile(to, os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", to, err)
	}
	return out, nil
}

// isBlockDevice holds the rule that the target of a write is a block device node.
//
// It is a variable for ONE reason: a test writes to ordinary files that it made
// itself, and a real device node needs root rights and a real disk. A test
// replaces this variable. The rule itself is never weakened, and
// TestTheRealTargetCheck proves on Linux what the real rule refuses.
var isBlockDevice = realIsBlockDevice

// realIsBlockDevice is the rule that runs on a device. It is a Linux rule: another
// system cannot make a device node, so there is nothing to test against there.
func realIsBlockDevice(path string, info os.FileInfo) error {
	if runtime.GOOS != "linux" || info.Mode()&os.ModeDevice != 0 {
		return nil
	}
	return fmt.Errorf("%s is not a block device", path)
}

// clone copies size bytes from one device to another. It is the dd of this package.
//
// The two partitions have the same size, so a block copy needs to know nothing
// about FAT32 or ext4. The copy syncs as it goes and syncs at the end, because a
// file that only lives in the page cache is a file that a power cut takes away.
func clone(ctx context.Context, from, to string, size int64, report func(done, total int64)) error {
	in, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("open %s: %w", from, err)
	}
	defer in.Close()

	out, err := openTarget(to)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, blockSize)
	var done, sinceSync int64
	lastReport := time.Time{}

	for done < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buf))
		if left := size - done; left < want {
			want = left
		}
		read, readErr := io.ReadFull(in, buf[:want])
		if read > 0 {
			if _, err := out.Write(buf[:read]); err != nil {
				return fmt.Errorf("write %s: %w", to, err)
			}
			done += int64(read)
			sinceSync += int64(read)
		}
		if sinceSync >= syncEvery {
			if err := out.Sync(); err != nil {
				return fmt.Errorf("sync %s: %w", to, err)
			}
			sinceSync = 0
		}
		if report != nil && time.Since(lastReport) >= reportEvery {
			lastReport = time.Now()
			report(done, size)
		}
		if readErr != nil {
			// The source is shorter than the size that the kernel reported. That is
			// a fault of the layout, not of the copy.
			if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
				return fmt.Errorf("%s ended after %d of %d bytes", from, done, size)
			}
			return fmt.Errorf("read %s: %w", from, readErr)
		}
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", to, err)
	}
	if report != nil {
		report(size, size)
	}
	return nil
}

// copyHead copies the first n bytes of one device onto another. It is the MBR boot
// code step, and it must not touch one byte more: the bytes after the boot code are
// the protective partition table that sgdisk wrote.
func copyHead(from, to string, n int64) error {
	in, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("open %s: %w", from, err)
	}
	defer in.Close()

	head := make([]byte, n)
	if _, err := io.ReadFull(in, head); err != nil {
		return fmt.Errorf("read the first %d bytes of %s: %w", n, from, err)
	}

	out, err := openTarget(to)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := out.WriteAt(head, 0); err != nil {
		return fmt.Errorf("write %s: %w", to, err)
	}
	return out.Sync()
}

// copyTree copies a directory tree with an fsync for each file (D41).
//
// It walks the source twice: once to count the bytes, so that the progress bar
// means something, and once to copy them. A count of a media partition is a read of
// directory entries and costs nothing next to the copy itself.
func copyTree(ctx context.Context, from, to string, report func(done, total int64)) error {
	total, err := treeBytes(from)
	if err != nil {
		return err
	}
	var done int64
	lastReport := time.Time{}

	return filepath.Walk(from, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(to, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, 0o755)
		case !info.Mode().IsRegular():
			// A link or a device node on the media partition is not content. exFAT
			// cannot hold one anyway.
			return nil
		}
		if err := fsutil.CopyFileSync(path, target); err != nil {
			// A file that this account may not read is not a reason to stop a whole
			// install.
			if os.IsPermission(err) {
				return nil
			}
			return err
		}
		done += info.Size()
		if report != nil && time.Since(lastReport) >= reportEvery {
			lastReport = time.Now()
			report(done, total)
		}
		return nil
	})
}

// treeBytes adds up the sizes of the regular files of a tree.
func treeBytes(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", root, err)
	}
	return total, nil
}
