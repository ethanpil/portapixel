//go:build !linux

package browser

import (
	"errors"
	"os"
	"os/exec"
)

// applyCredential has no work away from Linux. The device always runs Linux;
// another system is a development machine, where the browser runs under
// --browser-cmd as the developer.
//
// A kiosk account name here is a configuration fault, not something to ignore: a
// browser that quietly runs with full rights is the one result that we must never
// give.
func applyCredential(cmd *exec.Cmd, kioskUser string) error {
	if kioskUser != "" {
		return errors.New("this system cannot run the browser as another user; leave the kiosk user empty")
	}
	return nil
}

// terminate stops one process. There are no process groups here, and the
// override command of a development machine has no child processes.
func terminate(pid int, hard bool) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
