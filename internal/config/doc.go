// Package config reads and writes portapixel.toml.
//
// Why this package exists: portapixel.toml is the single source of truth for
// every device setting (D16). One package holds the model, the defaults, the
// rules, and the canonical text of the file, so the web UI, the first boot and a
// hand edit can never disagree about what a key means.
//
// Two rules of the package come from the plan. The first is resilience (D38):
// after each good parse the daemon copies the file to the ext4 state directory,
// and it boots from that shadow copy when the copy on PPMEDIA is missing or bad.
// A damaged media partition must never take the device off the network. The
// second is that a save renders the whole file from the model. The stock
// comments come back; comments that the user wrote do not.
//
// The package embeds the time zone database (time/tzdata). The device must be
// able to check a time zone name even when the operating system has no database,
// and the tests must run on a Windows development machine.
package config
