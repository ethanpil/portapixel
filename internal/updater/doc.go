// Package updater replaces the running binary with a new release (D28, plan
// section 15).
//
// Why this package exists: the device and the fleet server are two programs from
// one module, and both replace themselves the same way. The steps, the refusals
// and the health gate belong in one place, so a fix reaches both. Nothing in this
// package knows about a device: no playlist, no browser, no media partition. The
// caller gives the paths, the release source and the functions that touch the
// world.
//
// The layout under the release root is the layout of plan section 15:
//
//	<root>/releases/1.4.0/portapixeld
//	<root>/releases/1.5.0/portapixeld
//	<root>/current   -> releases/1.5.0
//	<root>/previous  -> releases/1.4.0
//	<root>/health/<version>.bad  a release that failed its gate
//	<root>/health/gate-boots     how many starts the pending version got
//	<root>/.swap-pending         the version that must prove itself
//
// The health marker of a release is NOT here. It is in the run directory, which
// is a tmpfs: <run>/health/<version>.ok, and MarkerPath is the one function that
// names it. A marker answers "the release that runs NOW came up", so it is true
// for one boot and the kernel must clear it. On the flash it needed a step that
// removed a stale marker, a rule about which of two processes goes first, and a
// loop in the daemon that wrote the file again every few seconds. That loop cost
// about 17,000 flash writes a day while the gate stayed armed, against D2.
//
// The three files above stay on the flash, because each must survive a restart of
// the service.
//
// The contract with os/overlay/usr/libexec/portapixel/health-gate.sh and
// os/overlay/etc/init.d/portapixeld:
//
//   - Apply writes the new version into <root>/.swap-pending BEFORE it restarts
//     the service. The gate does nothing at all without that file. The name in it
//     is already in the normal form (NormalizeVersion), and the gate uses that
//     name for both markers.
//   - The init script clears <run>/health in start_pre. That covers a restart of
//     the service with no restart of the machine. The daemon starts after it.
//   - The new daemon writes <run>/health/<version>.ok ONE time, when it is up. The
//     write is atomic, so the gate never reads a file of zero bytes.
//   - The gate looks for that file every PP_HEALTH_POLL seconds. It then removes
//     the pending file and stops. Nothing goes to the flash in the steady state.
//   - Without a marker the gate points current back to the target of previous,
//     writes <root>/health/<version>.bad, and restarts the service.
//   - The daemon finds the .bad file at its next start. CheckRollback records the
//     version in state.json, so the updater never offers that release again.
//
// The order of the daemon and the gate cannot decide the answer. That was once a
// rule that both sides had to keep; now it is true by construction, because a
// marker of an earlier boot cannot exist.
//
// Options.BinaryVersion reads the version of a staged release with
// "version --json" (see BinaryInfo). The human line of that subcommand is for a
// person and a program never reads it.
//
// Both symlinks are relative, for example "releases/1.5.0". install.sh writes
// them in that shape, and the gate reads them with readlink and writes them back
// unchanged. An absolute link would be wrong inside an --root install.
//
// Every refusal happens before the flip: an empty public key, a bad signature, a
// SHA-256 that does not match, a release that the gate already marked bad, a
// downgrade, another architecture, and not enough free space. The release gate of
// plan section 18 tests each of them.
package updater
