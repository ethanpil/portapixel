# Changelog

Short entries. Newest first. Each entry gives the commit hash when it is known.

## Unreleased

- Add screen power, mDNS, the A/B updater and install-to-disk to the daemon (308462d to bc3e060).
- Give each status warning a code. Add the all-day schedule rule and the WiFi country (aa6db83, a207985, 514020e).
- Fix the player syntax error and the black screen at boot. Record the M0 results (15a6754, 82dcaca).
- Move the safe-name rule to `internal/slug` (1d0b653).
- Add `portapixel-server` and its deployment files (00213b6 to b6d61f7).
- Fix 29 code review findings in the daemon and the player (8f4431f to b25a1cb).
- Add the device admin UI and its development server (7d3d12d, 915aa73).
- Add the QEMU boot test and the builder VM (1e5048d).
- Add the image builder, the boot files and first boot (3a1d37d to 6957d95).
- Add the package list and the installer (2c2a9eb).
- Fix the remaining review findings in `web/shared` (4396d0f).
- Ignore a refused chmod only on a filesystem without modes. Add `TotalBytes` (6148e24).
- Repair bad config values one field at a time (7091201).
- Make the session cookie slide. Export `IsLoopback` (fd92eab).
- Refuse a bad object hash and a legacy signature (953ced2).
- Fix 15 code review findings in the foundation and `web/shared` (a8deb1c).
- Add the device daemon for v0.1 (0ee122c).
- Add the player SPA and its mock daemon (e6f02e5).
- Change the display stack to Chromium in `cage`. Alpine 3.23 has no `cog`.
- Add the shared web design system and the playlist editor (9200fd9).
- Add the shared foundation packages (01ec737).
- Add the repository layout, the architecture contract and `CONTEXT.md` (6933340).
