//go:build linux

package player

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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

// killStray stops each process of the kiosk account and waits until they have
// ended. It gives the number of processes that it stopped.
//
// The kiosk account runs mpv and nothing else (D43), so after a stop of our own
// mpv each process of the account is a stray. A daemon that the kernel stopped
// for lack of memory, or that a panic ended, leaves its mpv behind: mpv is in a
// process group of its own, and supervise-daemon starts the daemon again with
// no stop_post. That mpv holds the DRM master. A new mpv then cannot show a
// picture, and the DPMS call of the screen power cannot switch the display.
func killStray(kioskUser string) (int, error) {
	if kioskUser == "" {
		return 0, nil
	}
	uid, _, err := lookupKiosk(kioskUser)
	if err != nil {
		return 0, err
	}
	pids := processesOf(uid)
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	// SIGKILL cannot be refused, so the wait is short. The kernel closes the
	// DRM handle when the process ends, before a parent reaps it.
	deadline := time.Now().Add(stopGrace)
	for len(processesOf(uid)) > 0 {
		if time.Now().After(deadline) {
			return len(pids), fmt.Errorf("a process of %s did not end after SIGKILL", kioskUser)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return len(pids), nil
}

// processesOf gives the process IDs of the live processes of a user. A zombie
// is not live: it holds no file any more.
func processesOf(uid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		status, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue // the process ended
		}
		if owner, live := statusOwner(string(status)); live && owner == uid {
			pids = append(pids, pid)
		}
	}
	return pids
}

// statusOwner reads /proc/<pid>/status. It gives the real user ID, and false
// for a zombie or a text that it cannot read.
func statusOwner(status string) (int, bool) {
	uid, live := -1, true
	for _, line := range strings.Split(status, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		switch {
		case key == "State" && len(fields) > 0:
			live = fields[0] != "Z" && fields[0] != "X"
		case key == "Uid" && len(fields) > 0:
			if n, err := strconv.Atoi(fields[0]); err == nil {
				uid = n
			}
		}
	}
	return uid, live && uid >= 0
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
