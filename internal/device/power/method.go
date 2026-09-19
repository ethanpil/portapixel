package power

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// The names of the two programs. cec-ctl is in the v4l-utils package and
// wlr-randr is the wlroots tool that the image already carries for the rotation.
const (
	cecTool   = "cec-ctl"
	randrTool = "wlr-randr"
)

// ---------------------------------------------------------------------- CEC
//
// NOT VERIFIED ON HARDWARE. Every cec-ctl call of this product is in this block,
// so one hardware check corrects one place. The release checklist has the check:
// a television on HDMI must go to standby and come back, and the television must
// switch its input to this device.
//
// The calls are:
//
//	probe: cec-ctl -d <device> --playback -S
//	       It configures the adapter as a playback device and shows the topology.
//	       A display that answers gives a line with the physical address in it.
//	on:    cec-ctl -d <device> --playback --to 0 --image-view-on
//	       cec-ctl -d <device> --playback --active-source phys-addr=<address>
//	       Image View On wakes the display. Active Source makes it switch its
//	       input to us. Active Source is a broadcast message, so it takes no --to.
//	off:   cec-ctl -d <device> --playback --to 0 --standby
//
// Logical address 0 is the television. A playback device is what a signage box
// is: it sends a picture and it is not an amplifier and not a recorder.

// cecProbeArgs, cecOnArgs, cecActiveSourceArgs and cecOffArgs build the argument
// lists. They are here and nowhere else.
func cecProbeArgs(device string) []string {
	return []string{"-d", device, "--playback", "-S"}
}

func cecOnArgs(device string) []string {
	return []string{"-d", device, "--playback", "--to", "0", "--image-view-on"}
}

func cecActiveSourceArgs(device, address string) []string {
	return []string{"-d", device, "--playback", "--active-source", "phys-addr=" + address}
}

func cecOffArgs(device string) []string {
	return []string{"-d", device, "--playback", "--to", "0", "--standby"}
}

// cecDevices gives the CEC device nodes of the machine. No node means that the
// hardware has no CEC adapter, so the probe does not even run.
func cecDevices() []string {
	nodes, err := filepath.Glob("/dev/cec*")
	if err != nil {
		return nil
	}
	return nodes
}

// probeCEC looks for a CEC adapter with a display on it. It gives the device node
// and the physical address of this device.
//
// Both facts have to come from one call, because the address is in the output of
// the probe. Without an address the on path cannot send Active Source, and the
// television wakes up on the wrong input.
func (c *Controller) probeCEC(ctx context.Context) (device, address string, ok bool) {
	if c.opt.Run == nil {
		return "", "", false
	}
	for _, node := range c.opt.CECDevices() {
		out, err := c.opt.Run.Run(ctx, cecTool, cecProbeArgs(node)...)
		if err != nil {
			c.log("power.cec.probe", node+" did not answer: "+err.Error())
			continue
		}
		address = physicalAddress(string(out))
		if address == "" {
			c.log("power.cec.probe", node+" answered, and no display acknowledged")
			continue
		}
		c.log("power.cec.probe", node+" has a display at physical address "+address)
		return node, address, true
	}
	return "", "", false
}

// physicalAddress takes the physical address out of the output of cec-ctl -S.
//
// The tool prints a line of the form "Physical Address : 1.0.0.0". An address of
// f.f.f.f means that the adapter has no display: the display gives the address
// over the HDMI hot-plug line, so a value of all f is the same as no answer.
func physicalAddress(text string) string {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		_, value, found := strings.Cut(line, ":")
		if !found || !strings.Contains(strings.ToLower(line), "physical address") {
			continue
		}
		address := strings.TrimSpace(value)
		if address == "" || strings.EqualFold(address, "f.f.f.f") {
			continue
		}
		return address
	}
	return ""
}

// cec switches the display over CEC.
func (c *Controller) cec(ctx context.Context, device, address string, on bool) error {
	if c.opt.Run == nil {
		return errors.New("no program runner is wired")
	}
	if device == "" {
		return errors.New("no CEC device was found")
	}
	if !on {
		return c.run(ctx, cecTool, cecOffArgs(device))
	}
	if err := c.run(ctx, cecTool, cecOnArgs(device)); err != nil {
		return err
	}
	if address == "" {
		// The display woke up. It may show another input, and nothing here can
		// tell it which input to take.
		return nil
	}
	return c.run(ctx, cecTool, cecActiveSourceArgs(device, address))
}

// --------------------------------------------------------------------- DPMS
//
// wlr-randr talks to the compositor of the kiosk session, so it works only while
// the browser runs. That is why the off path switches the display before it stops
// the browser (D31, and the block comment of apply).

// dpms switches the display over the compositor.
func (c *Controller) dpms(ctx context.Context, on bool) error {
	if c.opt.Run == nil {
		return errors.New("no program runner is wired")
	}
	output, err := c.dpmsOutput(ctx)
	if err != nil {
		return err
	}
	state := "--off"
	if on {
		state = "--on"
	}
	return c.run(ctx, randrTool, []string{"--output", output, state})
}

// dpmsOutput gives the name of the display, for example HDMI-A-1. The device has
// one display (D10), so the first output that wlr-randr names is the display.
func (c *Controller) dpmsOutput(ctx context.Context) (string, error) {
	out, err := c.opt.Run.Run(ctx, randrTool)
	if err != nil {
		return "", fmt.Errorf("%s did not answer: %w", randrTool, err)
	}
	return firstOutput(string(out))
}

// firstOutput takes the name of the first output out of the text of wlr-randr.
// The tool starts each output at the left margin and indents everything under it.
func firstOutput(text string) (string, error) {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s named no output", randrTool)
}

// run runs one program and puts its output in the error message. A tool that
// fails says why on its output, and an exit status on its own teaches nobody.
func (c *Controller) run(ctx context.Context, name string, args []string) error {
	out, err := c.opt.Run.Run(ctx, name, args...)
	if err == nil {
		return nil
	}
	if text := strings.TrimSpace(string(out)); text != "" {
		return fmt.Errorf("%s: %s (%w)", name, firstLine(text), err)
	}
	return fmt.Errorf("%s: %w", name, err)
}

// firstLine keeps the first line of the output of a tool. An error goes in the ops
// log, which trims at 1000 lines.
func firstLine(text string) string {
	if at := strings.IndexByte(text, '\n'); at > 0 {
		return text[:at]
	}
	return text
}
