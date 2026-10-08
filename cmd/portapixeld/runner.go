package main

import (
	"context"
	"os/exec"
	"runtime"
)

// rootRunner runs a program as root, which is the account of the daemon (D43).
// sgdisk, mkfs.exfat, mount and cec-ctl all need it.
type rootRunner struct{}

func (rootRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// restartService asks the service manager to start the daemon again after an
// update (plan section 15).
//
// The command is detached: a service restart from inside the service that it
// restarts would kill the process in the middle of the call, and the exit code would
// then never reach the updater. OpenRC stops the old process itself.
func restartService() error {
	if runtime.GOOS != "linux" {
		// A development machine has no service. The updater has done its work: the
		// links point at the new release and the next start uses it.
		return nil
	}
	cmd := exec.Command("rc-service", "portapixeld", "restart")
	return cmd.Start()
}
