// Package mdns announces the device on the local network (D20).
//
// Why this package exists: a person who plugs in a new screen has no address to
// type. The device answers to <name>.local, so the address is the name that the
// person gave it. While the name is still the factory default the announcement
// carries the last four characters of the device ID, because two fresh devices on
// one network must not fight over one name.
//
// Two rules hold this package to a small size:
//
//   - Nothing here may hold up the start of the daemon or the playback. A device
//     with no cable plays what it has. Run is a goroutine, every fault is an ops
//     log line, and the loop tries again at the next tick.
//   - The name and the addresses change while the daemon runs: a DHCP lease, a
//     cable that somebody plugged in, a new name in the web UI. The loop compares
//     them every 30 seconds and announces again when they are different.
//
// The announcement itself is github.com/hashicorp/mdns. It is behind a function,
// so the tests need no multicast socket.
package mdns
