# Changelog

Short entries. Newest first. Each entry gives the commit hash when it is known.

## Unreleased

- Add `portapixel-server`: database, media store, release mirror, device API, admin API, route tests and deployment files.
- Add the fleet server: `cmd/portapixel-server`, `internal/server/{db,api,admin,media,releases}` and `deploy/`. Three pairing flows, manifest resolution, release mirroring with signature checks, and the admin API for the server UI.
- Fix 29 code review findings in the daemon and the player (8f4431f to b25a1cb). `/media/` does not follow a link out of the media root and serves only image and video files.
- Add the device admin UI and its development server.
- Add the QEMU boot test and the builder VM.
- Add the image builder, the boot files, the OpenRC services and first boot.
- Add the package list and the installer. Each name is checked on Alpine 3.23.2.
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
