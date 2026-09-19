# PortaPixel documentation

This page is an index of the documentation. Start at the main
[README](../README.md) for an overview and a quick start.

## For a person who runs a screen

- [install.md](install.md) — flash the image, set up WiFi before the first
  boot, install onto an existing Alpine host, and move a trial install onto
  an internal disk.
- [content.md](content.md) — playlists, item types, schedules, and the two
  ways to add media.
- [settings.md](settings.md) — every key of `portapixel.toml`, its default
  value, and when a change of it takes effect.
- [troubleshooting.md](troubleshooting.md) — symptoms, their causes, and
  what to do about each one.

## For a person who runs a fleet

- [fleet.md](fleet.md) — the fleet server, the three pairing flows, what the
  server manages, and its security notes.
- [updates.md](updates.md) — how updates are signed, applied and rolled
  back, and how to sideload one.
- The server's own install steps are in [`deploy/README.md`](../deploy/README.md).

## For a contributor

- [ARCHITECTURE.md](ARCHITECTURE.md) — the contract between packages: names,
  paths and wire formats.
- [RELEASING.md](RELEASING.md) — the steps of cutting a release.
- [release-checklist.md](release-checklist.md) — the manual hardware
  checklist that a release must pass.
- [`../CONTEXT.md`](../CONTEXT.md) — what the project actually built, where
  it differs from the plan, and the lessons that are not obvious from the
  code.
