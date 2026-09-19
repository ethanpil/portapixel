package browser

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DisableCommand is the override value that stops the browser. A development
// machine has no cage and no display, and a daemon that tries to start Chromium
// every five seconds fills the ops log with nothing.
const DisableCommand = "none"

// URLPlaceholder is where the URL goes in an override command. Without it, the
// URL is the last argument.
const URLPlaceholder = "%u"

// DefaultDebugPort is the loopback port of the Chrome DevTools Protocol, which
// is navigation rung 1.
const DefaultDebugPort = 9222

// CommandConfig says how to start the browser. It is the only place in the
// program that knows the command line (ARCHITECTURE section 7).
type CommandConfig struct {
	// Override replaces the whole command. It comes from --browser-cmd or from
	// PORTAPIXEL_BROWSER_CMD. The value "none" stops the browser.
	Override string

	// KioskUser is the unprivileged account that runs the browser (D43). An
	// empty name runs the browser as the daemon's own user, which is for
	// development only.
	KioskUser string
	// CacheDir is the size-capped tmpfs that holds the Chromium profile, the
	// cache and the home directory of the kiosk user (D39).
	CacheDir string
	// RuntimeDir is XDG_RUNTIME_DIR of the cage session.
	RuntimeDir string

	DebugPort int
	// DebugURL replaces the address of the DevTools Protocol. Only a test sets
	// it, to point rung 1 at a stub browser.
	DebugURL string
}

// Disabled reports if the browser is switched off.
func (c CommandConfig) Disabled() bool {
	return strings.TrimSpace(c.Override) == DisableCommand
}

// Port gives the debug port.
func (c CommandConfig) Port() int {
	if c.DebugPort > 0 {
		return c.DebugPort
	}
	return DefaultDebugPort
}

// Endpoint gives the base URL of the DevTools Protocol.
func (c CommandConfig) Endpoint() string {
	if c.DebugURL != "" {
		return c.DebugURL
	}
	return "http://127.0.0.1:" + strconv.Itoa(c.Port())
}

// Build makes the command that shows url.
//
// The flags, in the order of ARCHITECTURE section 7:
//
//	cage -s --                       one fullscreen window, no desktop
//	--kiosk                          no browser interface at all
//	--ozone-platform=wayland         cage is a Wayland compositor
//	--remote-debugging-*             navigation rung 1, loopback only
//	--remote-allow-origins=...       lets OUR client open the socket
//	--autoplay-policy=...            video with sound starts by itself (D5)
//	--user-data-dir --disk-cache-dir the capped tmpfs, never the flash (D39)
//	--no-first-run ... --password-store=basic
//	                                 every dialogue, bubble and keyring that
//	                                 would appear over the content
//
// About --remote-allow-origins: Chromium 111 and later answer 403 to a DevTools
// WebSocket that carries an Origin header which this flag does not name.
// golang.org/x/net/websocket always sends an Origin and cannot leave it out, so
// without the flag rung 1 finds the page target, reports "cdp", and then fails
// at the first navigation with "bad status". Measured with Chromium 149 in QEMU
// on 2026-09-19.
//
// The value is the endpoint itself, which is exactly the Origin that our own
// client sends. It is NOT "*". The browser also shows pages from the internet,
// and such a page has its own origin, which stays outside this list: it cannot
// open the DevTools socket and take the device over.
func (c CommandConfig) Build(url string) (*exec.Cmd, error) {
	if c.Disabled() {
		return nil, errors.New("the browser is switched off")
	}
	if strings.TrimSpace(c.Override) != "" {
		args := splitArgs(c.Override)
		if len(args) == 0 {
			return nil, fmt.Errorf("the browser command %q holds no program name", c.Override)
		}
		used := false
		for i, a := range args {
			if strings.Contains(a, URLPlaceholder) {
				args[i] = strings.ReplaceAll(a, URLPlaceholder, url)
				used = true
			}
		}
		if !used {
			args = append(args, url)
		}
		return c.command(args[0], args[1:]...)
	}

	args := []string{
		"-s", "--", "chromium",
		"--kiosk",
		"--ozone-platform=wayland",
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=" + strconv.Itoa(c.Port()),
		"--remote-allow-origins=" + c.Endpoint(),
		"--autoplay-policy=no-user-gesture-required",
		"--user-data-dir=" + c.profileDir(),
		"--disk-cache-dir=" + c.cacheDir(),
		"--no-first-run",
		"--noerrdialogs",
		"--disable-infobars",
		"--disable-session-crashed-bubble",
		"--disable-features=Translate",
		"--password-store=basic",
		url,
	}
	return c.command("cage", args...)
}

// Tool makes a command that runs one program in the cage session, for example
// wlr-randr. It runs as the kiosk user, because a Wayland client must be the
// user that owns the session, and it gets WAYLAND_DISPLAY, because a client that
// joins the session from outside must find the socket.
//
// The context ends the program. exec.CommandContext kills the child itself. It
// does that before cmd.Wait reaps the child and never after. A timeout of our own
// sent a signal to a process number that the system gave to another program.
func (c CommandConfig) Tool(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// The child is a process group of its own, so Cancel must end the group.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return terminate(cmd.Process.Pid, true)
	}
	cmd.WaitDelay = toolWaitDelay
	cmd.Env = c.toolEnv()
	if err := applyCredential(cmd, c.KioskUser); err != nil {
		return nil, err
	}
	return cmd, nil
}

// toolWaitDelay is how long a tool may hold its output pipes after the context
// ended. Without it, a child of the tool that keeps the pipe open would hold
// cmd.Wait for ever.
const toolWaitDelay = 2 * time.Second

// command makes the command and gives it the environment and the account.
func (c CommandConfig) command(name string, args ...string) (*exec.Cmd, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = c.env()
	if err := applyCredential(cmd, c.KioskUser); err != nil {
		return nil, err
	}
	return cmd, nil
}

// env gives the environment of the browser command, which is cage itself.
//
// WLR_LIBINPUT_NO_DEVICES lets cage start on a machine with no keyboard and no
// mouse. Without it, wlroots refuses to start and the screen stays black: a
// signage device normally has no input devices at all.
//
// WAYLAND_DISPLAY is deliberately NOT here. wlroots reads that variable first:
// when it is set, cage asks for a window in a PARENT compositor instead of
// taking the DRM device. A signage device has no parent compositor, so cage
// stops at once with
//
//	[ERROR] [backend/wayland/backend.c] Could not connect to remote display
//	[ERROR] [cage.c] Unable to create the wlroots backend
//
// and the screen stays black. This was measured in QEMU on 2026-09-19. cage
// MAKES the socket and puts WAYLAND_DISPLAY in the environment of the program
// that it starts, so Chromium gets the value anyway. Only a client that joins
// the session from outside needs it (see toolEnv).
func (c CommandConfig) env() []string {
	env := []string{
		"WLR_LIBINPUT_NO_DEVICES=1",
	}
	if c.RuntimeDir != "" {
		env = append(env, "XDG_RUNTIME_DIR="+c.RuntimeDir)
	}
	if home := c.home(); home != "" {
		env = append(env, "HOME="+home)
	}
	// A minimal PATH: the kiosk account has no login shell, so it gets no
	// profile and no PATH of its own.
	env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin")
	return env
}

// toolEnv gives the environment of a program that joins the cage session from
// outside, for example wlr-randr. This one needs WAYLAND_DISPLAY, because it has
// to find the socket that cage made.
func (c CommandConfig) toolEnv() []string {
	return append(c.env(), "WAYLAND_DISPLAY="+WaylandDisplay)
}

// WaylandDisplay is the socket name of the cage session. cage makes
// wayland-0 in XDG_RUNTIME_DIR, and wlr-randr needs the same name.
const WaylandDisplay = "wayland-0"

// home gives HOME for the browser. It must be on the capped tmpfs: Chromium
// writes dot directories into HOME, and the flash must never take them (D39).
func (c CommandConfig) home() string {
	if c.CacheDir == "" {
		return ""
	}
	return c.CacheDir + "/home"
}

func (c CommandConfig) profileDir() string {
	if c.CacheDir == "" {
		return "/tmp/portapixel-profile"
	}
	return c.CacheDir + "/profile"
}

func (c CommandConfig) cacheDir() string {
	if c.CacheDir == "" {
		return "/tmp/portapixel-cache"
	}
	return c.CacheDir + "/cache"
}

// splitArgs cuts a command line into arguments. It gives space and the two
// quotation marks their usual meaning, so a path with a space in it works on a
// Windows development machine. It is not a shell: there is no expansion, no
// pipe and no redirection, and that is deliberate.
func splitArgs(line string) []string {
	var (
		out   []string
		cur   strings.Builder
		quote rune
		have  bool
	)
	flush := func() {
		if have {
			out = append(out, cur.String())
			cur.Reset()
			have = false
		}
	}
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			have = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	flush()
	return out
}
