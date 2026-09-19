package main

import (
	"context"
	"os/exec"
	"runtime"

	"github.com/ethanpil/portapixel/internal/device/browser"
)

// rootRunner runs a program as root, which is the account of the daemon (D43).
// sgdisk, mkfs.exfat, mount and cec-ctl all need it.
type rootRunner struct{}

func (rootRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// kioskRunner runs a program inside the cage session of the browser, as the kiosk
// user and with WAYLAND_DISPLAY set. wlr-randr is a Wayland client, so it must be
// the user that owns the session.
type kioskRunner struct {
	cmd browser.CommandConfig
}

func (k kioskRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := k.cmd.Tool(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	return cmd.CombinedOutput()
}

// powerRunner sends each program to the account that can run it.
//
// cec-ctl talks to /dev/cec*, which belongs to root. wlr-randr talks to the Wayland
// socket of the kiosk session, which belongs to the kiosk user. One runner that sent
// both to one account would fail on one of them at every call.
type powerRunner struct {
	root  rootRunner
	kiosk kioskRunner
}

func (p powerRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "wlr-randr" {
		return p.kiosk.Run(ctx, name, args...)
	}
	return p.root.Run(ctx, name, args...)
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
