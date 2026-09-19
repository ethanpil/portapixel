package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// randrTimeout is the time that a wlr-randr call may take. The tool talks to the
// compositor over a socket and answers at once.
const randrTimeout = 10 * time.Second

// The wait for the compositor.
const (
	// socketWait is how long applyDisplay waits for the Wayland socket of the
	// cage session. cage makes the socket some time after the process forks, and
	// wlr-randr against a socket that is not there yet loses the rotation until
	// the next launch.
	socketWait = 15 * time.Second
	// socketPoll is the time between two looks at the runtime directory.
	socketPoll = 250 * time.Millisecond
)

// applyDisplay puts the rotation and the output mode on the display.
//
// It runs in a goroutine of its own. The wait for the compositor takes seconds.
// The loop of the supervisor must stay free: a tick that waits is a tick that does
// not watch the browser.
func (s *Supervisor) applyDisplay() {
	ctx, cancel := context.WithTimeout(context.Background(), socketWait+2*randrTimeout)
	defer cancel()
	s.applyOutput(ctx)
}

// applyOutput sets the rotation and, when it is given, the output mode of the
// display. It runs wlr-randr in the cage session, so the rotation holds for every
// page, the pages of the internet included (ARCHITECTURE section 7).
//
// Every fault here is a log line and nothing more. A picture that is the right
// way up is better than a rotated picture, and a rotated picture is much better
// than no picture: we never stop the browser because a rotation failed.
func (s *Supervisor) applyOutput(ctx context.Context) {
	display := s.display()
	rotation, mode := display.Rotation, display.VideoMode
	if rotation == 0 && mode == "" {
		return
	}
	if s.opt.Command.Override != "" {
		// A development or test command is not a cage session. There is no
		// compositor to talk to. Say so once for each launch, because a rotation
		// that does nothing must not be a silent rotation.
		s.log("browser.randr.skip", "the browser command is an override, so there is no compositor to rotate")
		return
	}
	if err := waitForCompositor(ctx, s.opt.Command.RuntimeDir); err != nil {
		s.log("browser.randr.skip", err.Error())
		return
	}

	output, err := s.outputName(ctx)
	if err != nil {
		s.log("browser.randr.skip", err.Error())
		return
	}

	args := []string{"--output", output, "--transform", transformName(rotation)}
	if mode != "" {
		args = append(args, "--mode", mode)
	}
	call, cancel := context.WithTimeout(ctx, randrTimeout)
	defer cancel()
	cmd, err := s.opt.Command.Tool(call, "wlr-randr", args...)
	if err != nil {
		s.log("browser.randr.skip", err.Error())
		return
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		s.log("browser.randr.fail", strings.TrimSpace(string(out))+" ("+err.Error()+")")
		return
	}
	s.log("browser.randr", "output="+output+" transform="+transformName(rotation)+" mode="+mode)
}

// waitForCompositor waits until the Wayland socket of the cage session is there.
//
// An empty runtime directory is a development machine. There is nothing to wait
// for, so the wait ends at once and wlr-randr reports its own fault.
func waitForCompositor(ctx context.Context, runtimeDir string) error {
	if runtimeDir == "" {
		return nil
	}
	socket := filepath.Join(runtimeDir, WaylandDisplay)
	deadline := time.Now().Add(socketWait)
	for {
		if _, err := os.Stat(socket); err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the compositor made no %s in %s", socket, socketWait)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the wait for %s ended: %w", socket, ctx.Err())
		case <-time.After(socketPoll):
		}
	}
}

// outputName gives the name of the first output that wlr-randr reports, for
// example HDMI-A-1. The device has one display (D10), so the first output is the
// display.
//
// The output of wlr-randr starts each output at the left margin and indents
// everything under it:
//
//	HDMI-A-1 "Acme 27 (HDMI-A-1)"
//	  Make: Acme
func (s *Supervisor) outputName(ctx context.Context) (string, error) {
	call, cancel := context.WithTimeout(ctx, randrTimeout)
	defer cancel()
	cmd, err := s.opt.Command.Tool(call, "wlr-randr")
	if err != nil {
		return "", err
	}

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("wlr-randr did not answer: %w", err)
	}
	return firstOutput(string(out))
}

// firstOutput takes the name of the first output out of the text of wlr-randr.
//
// The guard tests the first character, and a line can hold whitespace that is
// neither a space nor a tab. strings.Fields then gives no field at all, so the
// length of the list is tested: a byte from another program must never stop the
// daemon.
func firstOutput(text string) (string, error) {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] != "" {
			return fields[0], nil
		}
	}
	return "", errors.New("wlr-randr named no output")
}

// transformName turns a rotation in degrees into the word that wlr-randr uses.
func transformName(rotation int) string {
	switch rotation {
	case 90, 180, 270:
		return strconv.Itoa(rotation)
	default:
		return "normal"
	}
}
