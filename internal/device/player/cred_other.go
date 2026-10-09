//go:build !linux

package player

import (
	"errors"
	"os"
	"os/exec"
)

// errNoKiosk is the answer for a kiosk account away from Linux. The device always
// runs Linux; another system is a development machine, where the player runs
// under --player-cmd as the developer. A player that quietly runs with full
// rights is the one result that we must never give.
var errNoKiosk = errors.New("this system cannot run the player as another user; leave the kiosk user empty")

// applyCredential has no work away from Linux.
func applyCredential(cmd *exec.Cmd, kioskUser string) error {
	if kioskUser != "" {
		return errNoKiosk
	}
	return nil
}

// ownBy has no work away from Linux.
func ownBy(path, kioskUser string) error {
	if kioskUser != "" {
		return errNoKiosk
	}
	return nil
}

// killStray has no work away from Linux: there is no kiosk account.
func killStray(kioskUser string) (int, error) {
	if kioskUser != "" {
		return 0, errNoKiosk
	}
	return 0, nil
}

// terminate stops one process. There are no process groups here.
func terminate(pid int, hard bool) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
