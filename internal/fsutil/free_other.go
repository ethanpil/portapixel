//go:build !linux

package fsutil

import "os"

// fakeBytes is the number that both functions below report. One value for the
// two of them shows a filesystem that is empty, not one that is full.
const fakeBytes = 1 << 50

// FreeBytes has no true implementation away from Linux. The device always runs
// Linux; Windows is only the development machine, where a free-space number has
// no meaning. This version makes sure the path exists and then reports a large
// number, so that a free-space test never stops a test run. Keep it small: the
// Linux version in free_linux.go is the one that counts.
func FreeBytes(dir string) (uint64, error) {
	if _, err := os.Stat(dir); err != nil {
		return 0, err
	}
	return fakeBytes, nil
}

// TotalBytes has no true implementation away from Linux either, for the reason
// above.
func TotalBytes(dir string) (uint64, error) {
	if _, err := os.Stat(dir); err != nil {
		return 0, err
	}
	return fakeBytes, nil
}
