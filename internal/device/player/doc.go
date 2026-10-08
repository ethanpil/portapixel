// Package player starts mpv and keeps it alive.
//
// Why this package exists: a person repairs most faults of the device over SSH.
// A picture that stopped needs a visit to the site. So one package owns the whole
// life of the player: the command line, the playlist, the transitions, the
// fallback screen, the watchdog ladder, the nightly restart and the wait for a
// display.
//
// mpv shows the content directly on DRM/KMS. There is no compositor. The daemon
// controls mpv with JSON IPC over a unix socket (ARCHITECTURE section 7). The
// command line lives in command.go and nowhere else. A full override,
// --player-cmd or PORTAPIXEL_PLAYER_CMD, replaces the program for a test and for
// development on a desktop.
//
// The daemon gives mpv the whole active playlist, with the options of each item.
// mpv loops it by itself, so the picture goes on when the daemon is busy. A
// schedule change or a playlist change replaces the list. When there is nothing
// to show, mpv shows a picture of the fallback screen that the daemon renders
// (D18).
//
// The transitions run inside mpv, in the Lua script transitions.lua. The daemon
// writes the script into the run directory and gives each item the kind and the
// length of its transition.
//
// The watchdog ladder (plan 3.3) has four reasons to restart mpv: the process
// ends, the IPC does not answer, a video does not move, or an image does not go
// to the next item in time. Four counted restarts in one hour reboot the device.
// The thresholds are in watchdog.go and supervisor.go.
//
// A display that is not plugged in is a wait state. It is never part of the
// ladder (D44): a television in standby after a power cut must not put the device
// into a restart loop. The supervisor waits with a backoff, writes one ops log
// line, and starts mpv when a connector appears.
package player
