// Package power turns the display on and off (D31).
//
// Why this package exists: a screen schedule needs two separate things. The
// display must stop showing a picture, and the browser must stop running. Plan
// section 8 puts both under one name, because the order is the whole problem.
// The off order is: the display first, the browser second. The DPMS path talks
// to the Wayland compositor that the browser session owns, so a browser that
// stopped first takes the compositor with it and leaves the display on. The on
// order is the reverse.
//
// The package knows two ways to switch a display:
//
//   - CEC, through cec-ctl of v4l-utils. It works when /dev/cec* exists and the
//     display answers the probe. A television on HDMI goes to standby, which is
//     what a person calls "off".
//   - DPMS, through wlr-randr inside the kiosk session. cage is a wlroots
//     compositor, so wlr-randr is the clean path. It needs a running session.
//
// The method comes from display.power_method: auto, cec, dpms or none. "auto"
// probes CEC one time and falls to DPMS.
//
// Everything that runs a program is an interface, so the tests use fakes and
// need no display, no Linux and no root.
package power
