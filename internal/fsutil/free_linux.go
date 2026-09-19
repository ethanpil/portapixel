//go:build linux

package fsutil

import (
	"fmt"
	"syscall"
)

// FreeBytes gives the free space of the filesystem that holds dir. The sync
// client uses it to refuse a manifest that cannot fit (D41).
func FreeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// TotalBytes gives the size of the filesystem that holds dir. The dashboard
// shows the free space against it.
func TotalBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return st.Blocks * uint64(st.Bsize), nil
}
