//go:build linux

package scheduler

import "syscall"

// staUnsync is STA_UNSYNC of <sys/timex.h>. The kernel sets it while no time
// source holds the clock, and chrony clears it at the first real synchronisation.
// The constant is not in the syscall package.
const staUnsync = 0x0040

// kernelSynced asks the kernel if the clock has a source.
//
// adjtimex with a zero buffer changes nothing: it only reads. The second answer
// is false when the call itself failed, and the caller then falls back to the year
// test.
func kernelSynced() (bool, bool) {
	var buf syscall.Timex
	if _, err := syscall.Adjtimex(&buf); err != nil {
		return false, false
	}
	return buf.Status&staUnsync == 0, true
}
