//go:build linux

package browser

import (
	"fmt"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

// kioskGroups are the groups that the browser needs: video for /dev/dri, input
// for /dev/input, audio for /dev/snd, and seat so that cage can ask seatd for
// DRM master without being root (plan 3.2).
var kioskGroups = []string{"video", "input", "audio", "seat"}

// applyCredential makes the command run as the kiosk account (D43). The browser
// renders pages from the internet and must never be root.
//
// A name that the system does not know gives an error. The browser then does not
// start and the fault is in the ops log and in /api/status. That is better than
// the other choice: a browser that runs as root because a lookup failed.
//
// Setpgid puts cage and every process that it starts into one process group, so
// that one signal stops the whole tree. Chromium starts many child processes,
// and a signal to cage alone leaves them behind.
func applyCredential(cmd *exec.Cmd, kioskUser string) error {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if kioskUser == "" {
		cmd.SysProcAttr = attr
		return nil
	}

	u, err := user.Lookup(kioskUser)
	if err != nil {
		return fmt.Errorf("the browser account %q is not on this system: %w", kioskUser, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("the user id of %q is not a number: %w", kioskUser, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return fmt.Errorf("the group id of %q is not a number: %w", kioskUser, err)
	}

	groups := []uint32{uint32(gid)}
	for _, name := range kioskGroups {
		g, err := user.LookupGroup(name)
		if err != nil {
			continue // the group is not on this system; go on without it
		}
		if n, err := strconv.Atoi(g.Gid); err == nil && uint32(n) != uint32(gid) {
			groups = append(groups, uint32(n))
		}
	}

	attr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}
	cmd.SysProcAttr = attr
	return nil
}

// terminate stops the whole process group. The minus sign in front of the
// process id is what makes the signal go to the group.
func terminate(pid int, hard bool) error {
	sig := syscall.SIGTERM
	if hard {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(-pid, sig); err != nil {
		return syscall.Kill(pid, sig)
	}
	return nil
}
