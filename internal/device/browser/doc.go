// Package browser starts the display stack and keeps it alive.
//
// Why this package exists: the screen is the product. Everything else on the
// device can be repaired over SSH, but a frozen picture is a field visit. So one
// package owns the whole life of the browser: the command line, the navigation,
// the watchdog ladder, the URL items, the nightly restart and the wait for a
// display that is not plugged in yet.
//
// The stack is Chromium in kiosk mode inside cage (CONTEXT.md section 3). The
// command line lives in command.go and nowhere else (ARCHITECTURE section 7). A
// full override, --browser-cmd or PORTAPIXEL_BROWSER_CMD, replaces the command
// for a test and for development on a desktop.
//
// The navigation ladder has two rungs, and both are tested code paths, because
// an untested fallback is a rumour:
//
//	rung 1 "cdp"       the Chrome DevTools Protocol on 127.0.0.1:9222.
//	rung 2 "relaunch"  start the browser again with the new URL.
//
// The watchdog ladder (plan 3.3): no heartbeat for 30 seconds outside a URL
// window, or a frame counter that stops while the heartbeats still arrive (D45),
// or a dead control rung, restarts the browser. Four restarts in one hour reboot
// the device. Every threshold is a constant in one block in supervisor.go.
//
// Display absence is a wait state and never part of the ladder (D44): a
// television in standby after a power cut must not put the device into a restart
// loop. The supervisor idles with a backoff, writes one ops log line, and starts
// the browser when a connector appears.
package browser
