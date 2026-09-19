//go:build linux

package health

import "syscall"

// space gives the free and the total bytes of the filesystem that holds dir.
//
// It makes the syscall itself instead of asking fsutil.FreeBytes, because the
// dashboard needs both numbers and one statfs call gives both. When fsutil grows
// a total, this file goes away.
func space(dir string) (free, total uint64) {
	if dir == "" {
		return 0, 0
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0
	}
	return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize)
}
