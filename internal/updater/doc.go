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
//	<root>/health/               the markers of the health gate
//	<root>/.swap-pending         the version that must prove itself
//
// The contract with os/overlay/usr/libexec/portapixel/health-gate.sh:
//
//   - Apply writes the new version into <root>/.swap-pending BEFORE it restarts
//     the service. The gate does nothing at all without that file.
//   - The gate runs "health-gate.sh arm" in start_pre, and the service waits for
//     it. That step removes a stale <root>/health/<version>.ok of an earlier
//     install, and it is the ONLY step that removes a marker. It happens before
//     the daemon can write one.
//   - The new daemon writes <root>/health/<version>.ok when it is up, and writes
//     it again every few seconds while .swap-pending names its own version. The
//     gate removes the pending file and stops, and the daemon then stops writing.
//     Nothing goes to the flash in the steady state.
//   - Without a marker the gate points current back to the target of previous,
//     writes <root>/health/<version>.bad, and restarts the service.
//   - The daemon finds the .bad file at its next start. CheckRollback records the
//     version in state.json, so the updater never offers that release again.
//
// The order of the daemon and the gate cannot decide the answer, and that is the
// point of the two rules above. The gate removed the stale marker AFTER it
// started before, so a daemon that was quick lost its marker. The gate then
// waited the whole timeout and marked a release that works bad for ever.
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
