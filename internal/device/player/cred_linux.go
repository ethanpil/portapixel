//go:build linux

package player

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

// kioskGroups are the groups that mpv needs: video for /dev/dri/card* and audio
// for /dev/snd. mpv reads no input device. Alpine has no render group: eudev
// gives /dev/dri/renderD* to the group video with mode 0666, so the render node
// that Mesa opens for vo=gpu (a Pi 4 renders on v3d and shows on vc4) needs
// nothing more. Seen on the pp-zero lab VM.
var kioskGroups = []string{"video", "audio"}

// applyCredential makes the command run as the kiosk account (D43). mpv parses
// files that come from a USB stick and from the network, so it must never be
// root.
//
// mpv needs no privilege for the display. The kernel makes the first process that
// opens a DRM primary node the DRM master, root or not. Measured on the pp-zero
// lab VM on 2026-10-08: mpv 0.40 as the kiosk account, with the groups video and
// audio only, showed video on vo=drm and on vo=gpu.
//
// A name that the system does not know gives an error. mpv then does not start
// and the fault is in the ops log and in /api/status. That is better than the
// other choice: a player that runs as root because a lookup failed.
//
// Setpgid puts mpv into a process group of its own, so that one signal stops it
// and every process that it starts.
func applyCredential(cmd *exec.Cmd, kioskUser string) error {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if kioskUser == "" {
		cmd.SysProcAttr = attr
		return nil
	}
	uid, gid, err := lookupKiosk(kioskUser)
	if err != nil {
		return err
	}
	groups := []uint32{uint32(gid)}
	for _, name := range kioskGroups {
		g, err := user.LookupGroup(name)
		if err != nil {
			continue // the group is not on this system; go on without it
		}
		if n, err := strconv.Atoi(g.Gid); err == nil && n != gid {
			groups = append(groups, uint32(n))
		}
	}
	attr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}
	cmd.SysProcAttr = attr
	return nil
}

// ownBy gives a directory to the kiosk account. mpv makes its socket there.
func ownBy(path, kioskUser string) error {
	if kioskUser == "" {
		return nil
	}
	uid, gid, err := lookupKiosk(kioskUser)
	if err != nil {
		return err
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("give %s to %s: %w", path, kioskUser, err)
	}
	return nil
}

// lookupKiosk gives the user and group IDs of the kiosk account.
func lookupKiosk(name string) (uid, gid int, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, fmt.Errorf("the player account %q is not on this system: %w", name, err)
	}
	if uid, err = strconv.Atoi(u.Uid); err != nil {
		return 0, 0, fmt.Errorf("the user id of %q is not a number: %w", name, err)
	}
	if gid, err = strconv.Atoi(u.Gid); err != nil {
		return 0, 0, fmt.Errorf("the group id of %q is not a number: %w", name, err)
	}
	return uid, gid, nil
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
