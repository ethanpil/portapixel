# PortaPixel context

The plan (`portapixel-dev-plan.md`, revision 5) is the intent. This file is the truth:
what we built, where we changed the plan, and the lessons that are not obvious from the
code. Read this file first. Then read `docs/ARCHITECTURE.md`.

Write this file in ASD-STE100 Simplified Technical English. Add an entry when you learn
something that cost you time. Remove an entry when it is no longer true.

## 1. How to work in this repository

- One Go module makes two binaries: `portapixeld` (device) and `portapixel-server`.
- `CGO_ENABLED=0` always. The web assets have no build step.
- `docs/ARCHITECTURE.md` is the contract for package names, paths and wire formats.
  Change the contract first, then the code.
- Run `gofmt -l .`, `go vet ./...` and `go test ./...` before each commit. The tests must
  pass on Windows and on Linux.
- Commit one logical unit of work at a time. Put a short entry in `CHANGELOG.md`.
- Standing review question: would a senior engineer say this is too complex?

## 2. Status

| Milestone | State |
|---|---|
| M0 bring-up spike | Done in QEMU only. No real hardware was available. See section 4. |
| v0.1 boot and play | In work |
| v0.2 the appliance | Not started |
| v0.3 the fleet | Not started |

## 3. Divergences from the plan

| Plan | What we did | Why |
|---|---|---|
| D3, D4, D5: WPE WebKit + cog on DRM/KMS, no compositor, three navigation rungs | Chromium in kiosk mode inside `cage`. Two navigation rungs: CDP, then relaunch. The `--autoplay-policy=no-user-gesture-required` flag is now REQUIRED. `seatd` and `wlr-randr` are in the image. | Alpine removed `cog`, `wpewebkit` and `wpebackend-fdo` after 3.21 (checked with `apk` against 3.22, 3.23 and edge on 2026-09-18). The maintainer chose Chromium + cage on 2026-09-18. `cage` shows one fullscreen window. It is not a desktop. Weston, sway and labwc stay out of scope. |
| Section 7: GStreamer packages | Not installed. | Chromium has its own media stack. |
| D39: browser tmpfs about 96 MB | About 256 MB. | A Chromium profile is larger than a WebKit one. |
| D6, D7: vendored Bootstrap for the two admin UIs | One hand-written stylesheet, `web/shared/pp.css`. No Bootstrap. | The wireframes give a full custom design (IBM Plex, moss green, `oklch` colours). Bootstrap below it would be more code, not less. Open decision for the maintainer. |
| Fonts from Google Fonts (wireframes) | IBM Plex Sans and Mono as local `woff2` files in `web/shared/fonts/` | A device on a closed network cannot get remote fonts. The licence is OFL. |

## 4. M0 results

- Alpine 3.23 has no `cog` and no `wpewebkit`. The last branch with them is 3.21
  (WPE WebKit 2.40.5, from 2023). This is risk 1 of the plan's risk register. See
  section 3 for the decision.
- Alpine 3.23 has `chromium` 149, `cage` 0.2.1, `seatd` and `wlr-randr` for x86_64 and
  aarch64.
- `swclock` is part of the `openrc` package. `cec-ctl` is in `v4l-utils`. `sgdisk` is its
  own package. `intel-ucode`, `amd-ucode`, `syslinux`, `intel-media-driver` and
  `libva-intel-driver` are x86_64 only.
- The Pi Zero 2 W (512 MB) is now marginal, because Chromium uses more RAM than WPE.
  Test it on real hardware before we promise it.

## 5. Lessons

- The daemon does not serve `*.toml` files or `_update/` from `/media/`. The first live
  run showed that `/media/portapixel.toml` gave the admin password and the WiFi key to
  the LAN. `/media/` has no session check, because the player has no session.
- `--browser-cmd none` sets the browser off (`browser_state: "disabled"`). Use it for
  development on a desktop. The health marker treats it as healthy.
- The daemon picks the navigation rung one time, at the first browser start. A probe at
  each restart would cost 45 s each time.
- The reboot ladder counts crashes and watchdog restarts only. A restart from a person,
  from a settings change or from the nightly job does not count.
- The frame counter starts at 0 on each new page. A lower value is a reset. The same
  value in three heartbeats is a stall (D45).
- The device ID falls back to a hash of the host name when there is no Pi serial, no DMI
  UUID and no physical NIC. Only a development machine gets there.
- A day that is not in `power_days` has no on-period. The screen stays off that day.
- A `playlist.toml` with no items is not a fault. The editor makes one before the first
  item.
- A static address goes to `wlan0` when an SSID is set, else to `eth0`.
- The `opslog` tests take about 20 s because they write many lines to a real file.
- `internal/config` imports `time/tzdata` (about 450 KB). Without it, time zone checks
  fail on a host with no zone database. The system database still wins on Alpine.
- The API shows a secret as `********`. A PUT that sends this mask keeps the old secret.
  An empty string clears it. A blank mask would make it impossible to clear a WiFi key.
- Rule: a bad value in `portapixel.toml` never costs the user the rest of the file.
  `config.Load` repairs each bad field to its default and keeps all other values. It
  gives the list in `Result.Repaired`. The shadow copy is only for a file that is missing
  or that is not TOML. A repaired config never goes into the shadow.
- Rule: no code writes the config to the media root when `Load` reports `FromShadow`,
  `FromDefault` or `Repaired`. The first review made `Load` refuse a file with one bad
  value. Then `provision` wrote the defaults on top of the user's WiFi key on first boot.
- Rule: a bad hand edit while the daemon runs keeps the config that runs. The shadow
  fallback is for the daemon start only.
- `/media/` serves by an allowlist of media extensions, not by a denylist. The atomic
  write makes `portapixel.toml.tmp<random>` in the media root. After a power cut that
  file can stay, and a denylist on `.toml` does not catch it.
- `config.Load` never returns an error. It always gives a config that works (D38). The
  caller reads `FromShadow`, `FromDefault` and `Warning`.
- `config.ChangeClass` has three classes: `live`, `browser` (restart the browser) and
  `reboot`. The reasons are in `internal/config/change.go`.
- The rendered TOML comments are in Simplified Technical English. They are not a copy of
  plan section 11.1. Each key and default of 11.1 is there.
- `version.PublicKey` is empty until release 1. `sigverify` refuses an empty key, so a
  development build cannot install an update.
- `store.Download` computes the SHA-256 on the complete `.part` file. A resumed download
  cannot continue a hash that was in progress.
- `fsutil.FreeBytes` is real on Linux only. Other systems get a large constant.
- Run `go test -race` in CI on Linux. The Windows machine has no C compiler.
- The development machine is Windows. Linux-only code (statfs, DRM, CEC, mount) sits
  behind build tags or runtime checks, so that `go test ./...` runs on Windows.
