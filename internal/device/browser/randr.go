package browser

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// randrTimeout is the time that a wlr-randr call may take. The tool talks to the
// compositor over a socket and answers at once.
const randrTimeout = 10 * time.Second

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
		// compositor to talk to.
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
	cmd, err := s.opt.Command.Tool("wlr-randr", args...)
	if err != nil {
		s.log("browser.randr.skip", err.Error())
		return
	}
	call, cancel := context.WithTimeout(ctx, randrTimeout)
	defer cancel()
	go func() {
		<-call.Done()
		if cmd.Process != nil {
			terminate(cmd.Process.Pid, true)
		}
	}()
	if out, err := cmd.CombinedOutput(); err != nil {
		s.log("browser.randr.fail", strings.TrimSpace(string(out))+" ("+err.Error()+")")
		return
	}
	s.log("browser.randr", "output="+output+" transform="+transformName(rotation)+" mode="+mode)
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
	cmd, err := s.opt.Command.Tool("wlr-randr")
	if err != nil {
		return "", err
	}
	call, cancel := context.WithTimeout(ctx, randrTimeout)
	defer cancel()
	go func() {
		<-call.Done()
		if cmd.Process != nil {
			terminate(cmd.Process.Pid, true)
		}
	}()

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("wlr-randr did not answer: %w", err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if name := strings.Fields(line)[0]; name != "" {
			return name, nil
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
