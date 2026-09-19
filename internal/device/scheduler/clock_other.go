//go:build !linux

package scheduler

// kernelSynced has no answer away from Linux. The device always runs Linux; every
// other system is a development machine, where the year test is enough.
func kernelSynced() (bool, bool) { return false, false }
