// Package browser starts the display stack and keeps it alive.
//
// Why this package exists: a person repairs most faults of the device over SSH.
// A picture that stopped needs a visit to the site. So one package owns the whole
// life of the browser: the command line, the navigation, the watchdog ladder, the
// URL items, the nightly restart and the wait for a display.
//
// The stack is Chromium in kiosk mode inside cage (CONTEXT.md section 3). The
// command line lives in command.go and nowhere else (ARCHITECTURE section 7). A
// full override, --browser-cmd or PORTAPIXEL_BROWSER_CMD, replaces the command
// for a test and for development on a desktop.
//
// The navigation ladder has two rungs. A test drives both of them: a fallback
// that no test drives is a fallback that we do not know:
//
//	rung 1 "cdp"       the Chrome DevTools Protocol on 127.0.0.1:9222.
//	rung 2 "relaunch"  start the browser again with the new URL.
//
// The watchdog ladder (plan 3.3) has three reasons to restart the browser. No
// heartbeat for 30 seconds outside a URL window. A frame counter that stops while
// the heartbeats still arrive (D45). A control rung that does not answer. Four
// restarts in one hour reboot the device. Every threshold is a constant in one
// block in supervisor.go.
//
// A display that is not plugged in is a wait state. It is never part of the
// ladder (D44): a television in standby after a power cut must not put the device
// into a restart loop. The supervisor waits with a backoff, writes one ops log
// line, and starts the browser when a connector appears.
package browser
