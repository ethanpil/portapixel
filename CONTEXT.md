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
| v0.2 the appliance | In work. The daemon side is built: power, mdns, updater, installer. No hardware check yet. |
| v0.3 the fleet | Not started |

## 3. Divergences from the plan

| Plan | What we did | Why |
|---|---|---|
| D3, D4, D5: WPE WebKit + cog on DRM/KMS, no compositor, three navigation rungs | Chromium in kiosk mode inside `cage`. Two navigation rungs: CDP, then relaunch. The `--autoplay-policy=no-user-gesture-required` flag is now REQUIRED. `seatd` and `wlr-randr` are in the image. | Alpine removed `cog`, `wpewebkit` and `wpebackend-fdo` after 3.21 (checked with `apk` against 3.22, 3.23 and edge on 2026-09-18). The maintainer chose Chromium + cage on 2026-09-18. `cage` shows one fullscreen window. It is not a desktop. Weston, sway and labwc stay out of scope. |
| Section 7: GStreamer packages | Not installed. | Chromium has its own media stack. |
| D39: browser tmpfs about 96 MB | About 256 MB. | A Chromium profile is larger than a WebKit one. |
| D6, D7: vendored Bootstrap for the two admin UIs | One hand-written stylesheet, `web/shared/pp.css`. No Bootstrap. | The wireframes give a full custom design (IBM Plex, moss green, `oklch` colours). Bootstrap below it would be more code, not less. Open decision for the maintainer. |
| Fonts from Google Fonts (wireframes) | IBM Plex Sans and Mono as local `woff2` files in `web/shared/fonts/` | A device on a closed network cannot get remote fonts. The licence is OFL. |
| D52: a sideload bundle names its version | The version comes from the staged binary, after the signature check. | The release assets are `portapixeld-<arch>`, `<name>.minisig` and `SHA256SUMS`. Not one of the three names holds a version, and a person who downloads them from GitHub has three loose files. Running a binary that minisign already proved is safe; a naming rule would be one more thing to get wrong. |
| Plan section 15: stage in `releases/<ver>.staging` | A download does. A sideload stages in `releases/.sideload.staging`. | A sideload does not know the version until the binary is verified (the row above). |

## 4. M0 results

Proven on 2026-09-19 with a real image in QEMU (KVM, virtio-gpu, llvmpipe). Each line was
checked with a screenshot of the virtual display or with the ops log.

- The image boots with SeaBIOS and with OVMF. Chromium draws the player in `cage`.
  `browser_state` is `running` 22 s after power on (BIOS, 2 GB) and 38 s (UEFI, 1 GB).
- Rung 1 (CDP) works. A URL item goes to the page, stays for the dwell time and comes
  back to the player at the next item. No relaunch occurs.
- Rotation 90 and 270 through `wlr-randr` work. All content turns.
- A killed browser is back in 1 s. A stopped browser (`kill -STOP`) is restarted after
  31 s with no heartbeat. The nightly restart waits for an item boundary.
- First boot runs one time. After a reboot the picture comes back.
- The Chromium sandbox is complete. There is no `--no-sandbox` and no
  `--disable-gpu-sandbox`. The user namespace sandbox works for the `kiosk` user.
- No GPU flag is necessary. The default path draws correctly on virtio-gpu.
- RAM: about 340 MB of anonymous memory plus 130 MB of tmpfs. 2 GB and 1 GB are good.
  512 MB with no swap FAILS (Chromium error code 4, restart loop). 512 MB with 512 MB of
  zram works, but 189 MB is in swap for the fallback screen alone. Do not promise the
  Pi Zero 2 W.
- Do not put `WAYLAND_DISPLAY` in the environment of `cage`. wlroots then selects its
  nested backend, finds no parent compositor and stops. The screen stays black. Only an
  outside client, `wlr-randr`, needs the variable.
- Chromium 149 refuses a DevTools WebSocket (403) when the request has an `Origin` that
  `--remote-allow-origins` does not name. `golang.org/x/net/websocket` always sends an
  `Origin`. The flag names the loopback DevTools endpoint only, never `*`.
- The daemon makes `HOME` for the kiosk user in the tmpfs. The init script must create
  it. Chromium writes its crash reports there.
- A status API that answers does not prove a picture. `player.js` once had a syntax
  error: the screen was black and the status looked good. Use
  `tests/qemu/boot-dev.sh shot` to see the screen. Check each player module with
  `node --check` on a copy with the `.mjs` extension.
- Not proven without real hardware: real GPU drivers, VA-API decode, CEC, DPMS, EDID and
  `video_mode`, HDMI audio, WiFi, the aarch64 image, and the PPMEDIA grow on a real card.
- Alpine 3.23 has no `cog` and no `wpewebkit`. The last branch with them is 3.21
  (WPE WebKit 2.40.5, from 2023). This is risk 1 of the plan's risk register. See
  section 3 for the decision.
- Alpine 3.23 has `chromium` 149, `cage` 0.2.1, `seatd` and `wlr-randr` for x86_64 and
  aarch64.
- `swclock` is part of the `openrc` package. `cec-ctl` is in `v4l-utils`. `sgdisk` is its
  own package. `intel-ucode`, `amd-ucode`, `syslinux`, `intel-media-driver` and
  `libva-intel-driver` are x86_64 only.
- Alpine 3.23 has apk-tools 3, not apk 2. `--initdb` is an option of `add`. `--root` is
  `-p`. For another architecture, apk needs the keys from `/usr/share/apk/keys/<arch>/`.
  The x86_64 keys refuse the aarch64 index as UNTRUSTED.
- Alpine 3.23 puts each OpenRC script in a `-openrc` subpackage, for example
  `openssh-server-common-openrc`, `busybox-openrc`, `chrony-openrc`, `seatd-openrc`.
  Without it, `rc-update add` fails.
- Firmware names: iwlwifi is in `linux-firmware-intel`. nouveau uses
  `linux-firmware-nvidia`. mt76 is in `linux-firmware-mediatek`. PCIe ath9k needs no
  firmware. `partx` is its own package. `partprobe` is in `parted`.
- The mkinitfs feature for SATA is `ata`. There is no `sata` feature.
- Do not add the `kms` mkinitfs feature. It copies all GPU firmware into the initramfs
  (167 MB against 18.5 MB). The root filesystem loads the GPU driver later.
- `ifupdown-ng` fails when `/etc/network/interfaces` does not exist. Then `networking`
  fails and OpenRC does not start `chronyd`. `install.sh` writes a safe DHCP default.
- `syslinux --install` changes the FAT boot sector but not its backup copy, so
  `fsck.vfat` reports a fault at each boot. The fstab pass number of PPBOOT is 0.
- busybox `blkid` takes no options. Use `findfs LABEL=...` in shell scripts.
- Name the clock service for each architecture: `hwclock` on x86_64, `swclock` on
  aarch64. The two provide `clock`. If it is implicit, OpenRC makes the choice.
- `install.sh` makes the `seat` group. The post-install script of `seatd` cannot run for
  another architecture.
- With UEFI, the firmware framebuffer takes `/dev/dri/card0` and the GPU is `card1`. Do
  not write `card0` into code.
- QEMU needs the package `qemu-hw-display-virtio-vga` to give the guest a DRM device.
- The x86_64 root filesystem is 1488 MB with Chromium, 41 % of the 3.5 GB PPROOT.
- The aarch64 build needs qemu-user binfmt on the build host (plan section 17). Without
  it the apk triggers fail with `Exec format error`. GitHub Actions has it. The test
  container does not.
- A container has no loop devices. `tests/qemu/builder-vm.sh` builds an image in a KVM
  guest.
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
- Rule: the screen-off order is the display first and the browser second. The DPMS path
  is `wlr-randr` inside the cage session, so a browser that stopped first takes the
  compositor with it and the display stays on all night. `internal/device/power` owns
  both steps for that one reason.
- A manual `screen-on` or `screen-off` holds until the screen schedule crosses an EDGE.
  A hold that ended at the next tick made the button useless.
- Rule: a `[[schedule]]` rule has both times or neither. Both empty is the whole day,
  which is how "weekends: this playlist" is written. Before this, such a rule matched
  nothing at all and the person saw the default playlist with no word about why.
- `status.warnings` is a list of `{code, message}`. The UI matches the code. It matched
  the first words of the message before, and one better sentence broke a banner.
- The player probes MediaCapabilities one time and sends the answer with its FIRST
  heartbeat. The daemon caches it in `status.codecs`, so the admin UI warns about the
  screen and not about the laptop of the person who looks at it (D12).
- `POST /api/rescan` also looks for a release bundle in `_update/`. The daemon owns that
  rule, so `httpd` calls `Deps.Rescan` and not `Library.Rescan`.
- The install progress hub replays its last event. The admin UI opens the stream after
  the POST answered, so a `done` event that went out first would never reach the page.
- A static address goes to `wlan0` when an SSID is set, else to `eth0`.
- The `opslog` tests take about 20 s because they write many lines to a real file.
- `internal/config` imports `time/tzdata` (about 450 KB). Without it, time zone checks
  fail on a host with no zone database. The system database still wins on Alpine.
- The API shows a secret as `********`. A PUT that sends this mask keeps the old secret.
  An empty string clears it. A blank mask would make it impossible to clear a WiFi key.
- Rule: an enroll request never changes a device row that was paired. The first server
  had two ways to take a screen from its owner, and neither needed a password. One: an
  enroll with no token and the ID of a paired screen put that screen into the pending
  list. The admin approved a row that looked familiar, and the caller got the token.
  Two: the fleet enrollment token, which is in clear text on each card, could replace
  the token of any device ID. Now a pending request lives in its own table. A paired row
  changes only when the admin approves, or when the request has a valid enrollment token
  and the same `hardware_id` as the row (a card that was flashed again).
- Rule: `hardware_id` is a secret between the device and the server. The device ID shows
  only its first 8 hex characters. No route without an admin session gives the
  `hardware_id`. The device does not put it in `/api/status`.
- Rule: each secret in the server database is a SHA-256 hash. This includes the claim
  secret of a pending enrollment.
- Rule: a JSON route refuses a body that is not `application/json`. A page on another
  site cannot then send a simple request with no preflight.
- Rule: only the lead edits `CHANGELOG.md`. A worker made a second entry for one change.
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
