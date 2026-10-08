// Package power turns the display on and off (D31).
//
// Why this package exists: a screen schedule needs two separate things. The
// display must stop showing a picture, and the player must stop running. Plan
// section 8 puts both under one name, because the order is the whole problem.
//
// The player (mpv) runs directly on DRM/KMS, with no compositor. It is the DRM
// master while it runs, and only the master may switch a display. So the off
// order is: the player stops first and the display call comes second. The on
// order is the reverse: the display call first and the player second. The
// Controller does this in apply, and nowhere else.
//
// The package knows two ways to switch a display:
//
//   - CEC, through cec-ctl of v4l-utils. It works when /dev/cec* exists and the
//     display answers the probe. A television on HDMI goes to standby, which is
//     what a person calls "off".
//   - DPMS, through the DRM device of the kernel (drm.go, drm_linux.go). The
//     package sets the property "DPMS" of each connected connector to "off" and
//     holds the device open. The kernel puts the display on again when the last
//     handle closes, so the hold is the "off" state. The next On sets "on" and
//     lets go. It needs no program and no compositor.
//
// The method comes from display.power_method: auto, cec, dpms or none. "auto"
// probes CEC one time and falls to DPMS.
//
// Everything that runs a program or opens a device is an interface, so the tests
// use fakes and need no display, no Linux and no root.
package power
