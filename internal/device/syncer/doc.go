// Package syncer is the fleet client of the device: pairing, the poll loop, the
// object store, the fleet playlists and the remote commands (D24, D25, D26).
//
// Why the package exists. The device is complete on its own, and a fleet server
// is an addition (plan section 13). One package holds every rule of that addition,
// so a standalone device runs no fleet code and a reader finds the whole protocol
// in one place. The wire types are in internal/manifest, which the server shares,
// so the two ends cannot drift.
//
// The rules that are not obvious from the code:
//
//   - Nothing here may hold up the start of the daemon or the picture on the
//     screen. Run is a goroutine that starts after the HTTP server and the
//     browser. Every fault is an ops log line and a sentence in /api/status.
//     The device plays what it has; a sync improves it later (plan 3.3).
//
//   - The per-device token lives in state.json on ext4 and never in
//     portapixel.toml. The TOML holds only the token that a person typed, so a
//     flashed card stays clonable (D25).
//
//   - hardware_id is a secret between the device and its server. It leaves the
//     device in the enroll request and in the heartbeat and in no other place.
//     It is what permits a re-pair with the fleet enrollment token.
//
//   - A manifest is applied whole or not at all. The objects come first, then the
//     playlists go into a staging directory and a rename puts them in place. A
//     failed download keeps the old playlists and the part file, and the next poll
//     continues (D24, D41).
//
//   - A manifest that is the same as the last one writes nothing at all: no file,
//     no rescan, no player event. The steady state of a paired device must write
//     nothing to the flash medium (D2).
//
//   - A command runs one time. The server sends a command again when no
//     acknowledgement arrives, so the state file keeps the IDs that ran. A reboot
//     is acknowledged before the machine goes down.
package syncer
