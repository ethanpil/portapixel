# Changelog

Short entries. Newest first. Each entry gives the commit hash when it is known.

## [Unreleased]

## [0.5.0-rc.1] - 2026-10-09

The first pre-release. The device player is mpv. It is for tests on real hardware.

### Removed

- Chromium, `cage`, `seatd`, `dbus` and the browser fonts from the image (6bd36ad). The root file system goes from 1558 MB to 1062 MB.
- The Chromium player: the browser package, the web player, `cage`, the DevTools control and the `--browser-*` flags (9b3031f, 0d30320).
- Web page items, single-URL kiosk mode, the device tier and the codec report (37115a5 to 002b7bb). The player becomes mpv, which shows images and videos only. Server migration 2 deletes the web page items and changes old transition words to `fade`.

### Changed

- Rewrite the README, the user docs, the release checklist, CONTEXT.md and the licence files for the mpv player (6a59463, 612fbee to 2e37263). The demo videos name the Mixkit licence. `libdvdcss` stays in the image, because `libdvdread` links it.
- Offer 22 transitions: add fade-white, slide-in and slide-out in four directions, zoom-out and split (a2c927b). An item can name its own transition (4e4b5b2). New playlist option `ken_burns`, a slow zoom on images with gpu output (8f1a5c7, 0917424).
- Offer eleven transitions again: cut, fade, crossfade, wipe and push in four directions (8c62018, 4c5e688). A crossfade from a video moves on x86 and on a Pi 5, with a guard (1325177, `playback.motion`). Fix 4 findings of the review (e605abf to 9437262).
- Decode H.264 in hardware on a Raspberry Pi (90bfce7, 38fecb1). Hide the text console (61128f7, 3c0744b). Give a 512 MB Pi 128 MB of CMA (61128f7).
- The release workflow uses the `PORTAPIXEL_MINISIGN_*` names, and a tag `vX.Y.Z-rc.N` publishes a signed pre-release (ec1d5b7). Fix 3 findings of the stage 3 review (caf3472 to a9f891e).

### Fixed

- A video with frames far apart no longer counts as a stall: the limit is two frame intervals of the file (0a3ff39, f6700dd). A push or slide-in into a picture moves on a slow `drm` device (4d5b021). A palette picture or a picture with a clear background starts its transition from the right picture on `drm` (57b4746, 5b180d1). The first boot log line has no doubled key (fb2d632).
- Fix the findings of the final review of the whole code (9acef4d, 5936ec4, and the merges after them). Device: an orphan mpv after a daemon crash, a file that crashes mpv, an unbounded `video_mode`, animated images and the watchdog, a slow fallback render, an explicit CEC method, padded screen rows, a chain of moving crossfades, a turned screen, a lost screen-off, a stack buffer given to the kernel, lost playlist changes, the fallback flash after a schedule change, flash wear from repeated log lines, and `auto` audio on a Pi. Server: a database fault that unpaired every screen, a reject that removed a paired screen, the client address behind a proxy, the pending-row limit, server.toml saves, the upload allowlist, pre-release marks and the order of `-rc.N`. OS and web: an on-box install that could erase PPMEDIA, a first boot with no PPMEDIA mount, and the server playlist rename.
- The 3.5 mm audio jack of a Raspberry Pi was never on: `dtparam=audio` was below the overlay in `config.txt` (61128f7).
- The device player is mpv on DRM/KMS, driven over JSON IPC, with a watchdog that measures time with the monotonic clock (3a619c0, 0d30320, ab9b606). An embedded Lua script makes the transitions inside mpv (e0c3738).
- The fallback screen is a PNG that the daemon draws (65f2578). The screen goes off through DRM DPMS, not `wlr-randr` (a56c330).
- The command `restart-browser` is now `restart-player` (d5791ce). New setting `display.video_output` (a42c1c9).
- Fix 8 findings of the stage 2 review (0de6a4f to 6a98d3c).
- A transition is `cut` or `fade` for now (ea016cb). The status names the player state `player_state` (37115a5).

- Fix 13 findings of the review of the preview and rename work (83fa177 to a59af14). A remote rename does not write over a config file that has a fault. One lock holds each config write. A name has 1 to 63 characters, with no invisible or bidi characters. A clone does not change the name of the row. A first boot copy leaves no short file. CI runs the node tests.
- Show what plays in the two admin UIs, and rename a screen from the server as a command (bb7ba5c to c440e62). The name of a row now follows the name of the device. A name that only the old rename route set goes back to the name of the device at its first heartbeat. Note: the tree does not pass `go vet` at 37930f6, because its test uses a field that a5a36ec adds; skip 37930f6 in a bisect. 37930f6 also holds the admin route for video frames, a5a36ec holds the hash report of the daemon, and bb7ba5c holds the name rule.
- Ship seven demo videos as the default content (e9f2a58, bddbc5e). They are not CC0; see `LICENSES-THIRD-PARTY.md`.
- Use short sentences on the Server health page (6abc339).
- Turn the back/forward cache of Chromium off: a URL item no longer leaves a renderer process behind (d36e3c9). Sixteen other flags were measured and refused; see CONTEXT.md section 4.
- Select only fleet playlists while paired. Apply the audio settings (D11). Make the watchdog tunable (D30) (466ad4b to 3f0e44e).
- Fix 15 findings of the final device review (19504ab to 699eb0a). The status for the LAN does not tell which passwords are the default. A fleet playlist named media cannot delete the object store. A standalone device writes nothing in its steady state. Each answer has security headers.
- Fix 12 findings of the final OS, CI, web and document review (d855054 to e02d989). The container image and the release wait for each gate. The arm64 container holds an arm64 binary. The Activity page shows again.
- Fix 13 findings of the final server review (d2faa1d to ec84667). A waiting enrollment is keyed on the hardware ID too. Command IDs never come back. Each answer has security headers, and a stored object is inert. Note: 7e2494d also holds the media store files and ec84667 holds the server tests; the two titles are wrong.
- Fix the faults that the first CI run found (760ef9d to 872455a). An update reads the version from `version --json`. The health gate cannot remove a good marker. The on-box installer adds no kernel. The tree does not build at commit a0fa2fc: it uses a type that the next commit, ff36380, adds. Skip a0fa2fc in a bisect.
- Add the lint and release workflows for GitHub Actions and `docs/RELEASING.md` (43d3553, f983b59, c09fe86). The aarch64 image and the Docker image build for the first time.
- Read the list of fleet fields from the daemon in the device admin UI (b4c17ea).
- Add the README and the user documents (4932af3).
- Fix the review findings in the OS layer (23f479c to e30b90e). The first boot grow of PPMEDIA is safe at each power cut. The health gate is fail safe. Add `acpid`.
- Fix the review findings in the daemon, the updater, the installer and the sync client (6a3fd1e to 52cfee8). The device token goes to the paired server only. Commit 52cfee8 also holds the server code for the `token-revoked` answer; its message names only the contract.
- Fix the review findings in the two admin UIs. Move their common code to `web/shared` (66d584c to b05b496).
- Add the lab scripts for two test machines on a bridge (105b3b6).
- Add the fleet sync client, the pairing routes and the local lock for a paired device.
- Hide the mouse pointer, keep the browser output, stop the Chromium calls to Google, add placeholder slides.
- Fix 39 code review findings in the server (67be516 to a357a46). An enroll request cannot take a paired screen. A hardware repair is not a clone.
- Add the control server admin UI, its development server and its seed tool.
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
