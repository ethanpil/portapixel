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
| v0.1 boot and play | Built and reviewed. Proven in QEMU with screenshots. No real hardware check yet. |
| v0.2 the appliance | Built and reviewed. CEC, DPMS and install-to-disk have no hardware check yet. |
| v0.3 the fleet | Built and reviewed. Proven with two machines in the lab (`tests/qemu/lab`). |
| v1.0 hardening | Reviews done. The release pipeline is proven to a draft release. Open: the release key, the 30 day soak, the hardware checklist. |
| Change to mpv (2026-10-08) | Done on main: the content model, the mpv player, the OS image and CI, and the docs. Proven in QEMU and in the pp-zero lab VM. No real hardware check yet. See the decision record in section 3. |

The aarch64 image builds in GitHub Actions (the test container has no qemu-user binfmt),
and CI reads the boot files inside it. No Raspberry Pi has started this image.

## 3. Divergences from the plan

| Plan | What we did | Why |
|---|---|---|
| D3, D4, D5: a browser on DRM/KMS as the player, with three navigation rungs | mpv directly on DRM/KMS, with no compositor and no browser. `portapixeld` controls it over JSON IPC. | See the decision record below. |
| Section 7: GStreamer packages | Not installed. | mpv decodes with FFmpeg. |
| D6, D7: vendored Bootstrap for the two admin UIs | One hand-written stylesheet, `web/shared/pp.css`. No Bootstrap. | The wireframes give a full custom design (IBM Plex, moss green, `oklch` colours). Bootstrap below it would be more code, not less. Open decision for the maintainer. |
| Fonts from Google Fonts (wireframes) | IBM Plex Sans and Mono as local `woff2` files in `web/shared/fonts/` | A device on a closed network cannot get remote fonts. The licence is OFL. |
| D52: a sideload bundle names its version | The version comes from the staged binary, after the signature check. | The release assets are `portapixeld-<arch>`, `<name>.minisig` and `SHA256SUMS`. Not one of the three names holds a version, and a person who downloads them from GitHub has three loose files. Running a binary that minisign already proved is safe; a naming rule would be one more thing to get wrong. |
| D37: a CC0 image set, 1920x1080 | Seven H.264 720p demo videos, 59 MB in total, in `os/default-media/`. Five come from Mixkit (Mixkit Stock Video Free License). Two have no recorded source. | The maintainer supplied them on 2026-09-22. They are not CC0. The owner decided on 2026-10-09 to keep all seven. The maintainer must confirm the source of the last two. |
| Plan section 15: stage in `releases/<ver>.staging` | A download does. A sideload stages in `releases/.sideload.staging`. | A sideload does not know the version until the binary is verified (the row above). |

### Decision record: the display stack is mpv on DRM/KMS (2026-10-08)

The plan (D3 to D5) had a browser as the player. The first build used Chromium
inside a `cage` session. On 2026-10-08 the owner chose mpv on DRM/KMS.
`portapixeld` controls it over JSON IPC. There is no compositor and no browser.

Why. A lab spike measured mpv on pp-zero. This virtual machine has the limits of
a Raspberry Pi Zero 2 W: 512 MB, the speed of an SD card, and a CPU duty cycle.
It played the seven demo videos. The Chromium values come from earlier logs of
the same machine. The spike did not run them again.

| With the CPU limit | Chromium and cage | mpv `drm` | mpv `gpu` |
|---|---|---|---|
| Memory (RSS) | 430 to 475 MB | 87 to 136 MB | 108 to 215 MB |
| Swap in use | 167 to 178 MB | 23 to 31 MB | 23 to 62 MB |
| CPU on video, of one core | 281 to 299 % | 135 to 150 % | about 300 % |
| Dropped frames | about 28 % | 0 % | 40 to 65 % |

The `gpu` output ran on llvmpipe, which is software. It was not good on the
proxy, and this is why `video_output` has `drm`. Without the limit, mpv `drm`
used about 26 % of a core on video and dropped no frame. The picture came
0.05 s after a load, the gap between two items was 0.03 to 0.07 s, and no black
frame showed. The image shrank: PPROOT 1062 MB against 1558 MB, `.img.gz`
1035 MiB against 1297 MiB, and 282 packages against 334.

The cost. There are no web page items and no kiosk mode (D42). A Lua script in
mpv does the transitions. Go draws the fallback screen as a PNG. DPMS through
DRM switches the screen off.

The limit of the evidence. The spike had no VideoCore GPU and no hardware
decoder, so the absolute CPU and drop values do not transfer to a Pi. The
Chromium numbers of the old 512 MB tests no longer apply. A 512 MB Pi needs a
new measurement (`docs/release-checklist.md`).

## 4. M0 results

Proven in QEMU (KVM, virtio-gpu, llvmpipe) with a real image on 2026-09-19, and again with
the mpv image on 2026-10-08. Each line was checked with a screenshot of the virtual display
or with the ops log.

- The image boots with SeaBIOS and with OVMF. The boot test
  (`tests/qemu/boot-smoke.sh`) waits for `player_state` `running` and a
  `now_playing` item, and it reads the owner of the mpv process. Result with the
  0.5.0-lab5 image on 2026-10-08: PASS in 68 s (SeaBIOS) and 69 s (OVMF). mpv
  runs as `kiosk` and plays `01-meadow.mp4`.
- mpv runs as the unprivileged `kiosk` account, in the groups `video` and
  `audio`. The first process that opens `/dev/dri/card*` becomes the DRM master,
  also without root. Proven on pp-zero on 2026-10-08, with vo=drm and with
  vo=gpu.
- Screen off works in QEMU (pp-zero, virtio-gpu, mpv 0.40, 2026-10-08). Stop
  mpv, set DPMS off, and the VNC picture shows "Display output is not active"
  for the whole hold. DPMS on and a new mpv bring the picture back. Both
  outputs, vo=drm and vo=gpu.
- Rotation 90 turns the video and each transition (`--video-rotate`). Checked
  on pp-zero with a 1280x800 mode and 1280x720 media. The picture keeps its
  black bars.
- First boot runs one time. After a reboot the picture comes back.
- A DNS name in a packet dump is in label form. `strings | grep` does not find it. Parse
  the DNS questions.
- The output of mpv is in `/run/portapixel/player.log` (tmpfs, 1 MiB limit, new
  at each start of the player). Read it first when the screen is black.
- The `HOME` of mpv is `/run/portapixel/player` (`kiosk`, 0700). The daemon
  makes it at each start. A shader cache would fill this small tmpfs, so mpv runs
  with `--gpu-shader-cache=no` and `MESA_SHADER_CACHE_DISABLE=true`.
- A status API that answers does not prove a picture. Use
  `tests/qemu/boot-dev.sh shot` to see the screen. `tests/ci/js-check.sh` checks
  each admin UI module.
- Not proven without real hardware:
  - real GPU drivers
  - the Raspberry Pi video decoder (`v4l2m2m-copy`) and the CMA sizes
  - transitions and Ken Burns on vc4 and v3d
  - VA-API decode, CEC, DPMS, EDID and `video_mode`
  - HDMI audio and the 3.5 mm jack
  - WiFi, the aarch64 image, and the PPMEDIA grow on a real card

  `docs/release-checklist.md` lists each one.
- Alpine 3.23 has `mpv` 0.40.0 for x86_64 and aarch64. It brings FFmpeg,
  libplacebo, libass and LuaJIT as dependencies.
- `swclock` is part of the `openrc` package. `cec-ctl` is in `v4l-utils`. `sgdisk` is its
  own package. `intel-ucode`, `amd-ucode`, `syslinux`, `intel-media-driver` and
  `libva-intel-driver` are x86_64 only.
- Alpine 3.23 has apk-tools 3, not apk 2. `--initdb` is an option of `add`. `--root` is
  `-p`. For another architecture, apk needs the keys from `/usr/share/apk/keys/<arch>/`.
  The x86_64 keys refuse the aarch64 index as UNTRUSTED.
- Alpine 3.23 puts each OpenRC script in a `-openrc` subpackage, for example
  `openssh-server-common-openrc`, `busybox-openrc`, `chrony-openrc`,
  `acpid-openrc`. Without it, `rc-update add` fails.
- Firmware names: iwlwifi is in `linux-firmware-intel`. nouveau uses
  `linux-firmware-nvidia`. mt76 is in `linux-firmware-mediatek`. PCIe ath9k needs no
  firmware. `partx` is its own package. `partprobe` is in `parted`.
- The mkinitfs feature for SATA is `ata`. There is no `sata` feature.
- Do not add the `kms` mkinitfs feature. It copies all GPU firmware into the initramfs
  (167 MB against 18.5 MB). The root filesystem loads the GPU driver later.
- `mv -f` of a new link onto a link to a directory moves the new link INTO that directory.
  Use `mv -fT` to replace the link. busybox has `-T`. The health gate test found this.
- busybox `xargs -0` runs its command one time with no input. `find -exec ... +` does not.
- Do not trust the status of `cp -a` to exFAT. exFAT refuses chown, so a complete copy
  reports a fault. Check the copy by file count and byte sum.
- PPROOT has `commit=60`. A marker file is safe only after a `sync`. Write the marker and
  sync it before the step that destroys data.
- A size test must be good on the second boot. After a cut, the kernel already has the new
  partition size. Compare with the size in the GPT, not with the old size.
- OpenRC does not run `start_post` when the start fails. Arm a gate in `start_pre`.
- `supervise-daemon` sends KILL after about 5 s unless `retry` is set. The daemon
  needs more time to stop the player: `retry="TERM/25/KILL/5"`.
- The `acpid` package arrives as a dependency, but its service is not enabled.
- The initramfs of Alpine 3.23 is gzip. A kernel module file is `.ko.gz` and its name has
  hyphens: `xhci-pci.ko`, not `xhci_pci`.
- Alpine has no `render` group. eudev puts `renderD*` in `video`.
- `pkill -f <pattern>` also matches the SSH command that runs it. Stop a process by its
  recorded PID.
- In `expect`, send the answer in the block that matched. A `send` after an `expect` block
  that stands alone was not reliable.
- Two workers in one working tree see the edits of each other. A reviewer reported the
  work of another worker as damage. Check the content before you act on such a report.
- `ifupdown-ng` fails when `/etc/network/interfaces` does not exist. Then `networking`
  fails and OpenRC does not start `chronyd`. `install.sh` writes a safe DHCP default.
- `syslinux --install` changes the FAT boot sector but not its backup copy, so
  `fsck.vfat` reports a fault at each boot. The fstab pass number of PPBOOT is 0.
- busybox `blkid` takes no options. Use `findfs LABEL=...` in shell scripts.
- Name the clock service for each architecture: `hwclock` on x86_64, `swclock` on
  aarch64. The two provide `clock`. If it is implicit, OpenRC makes the choice.
- With UEFI, the firmware framebuffer takes `/dev/dri/card0` and the GPU is `card1`. Do
  not write `card0` into code.
- QEMU needs the package `qemu-hw-display-virtio-vga` to give the guest a DRM device.
- The x86_64 root filesystem is 1062 MB with mpv and the seven demo videos. That
  is 29 % of the 3.5 GB PPROOT, in 282 packages (lab5 build, 3c0744b). The
  `.img.gz` is 1035 MiB.
- The aarch64 build needs qemu-user binfmt on the build host (plan section 17). Without
  it the apk triggers fail with `Exec format error`. GitHub Actions has it. The test
  container does not.
- A container has no loop devices. `tests/qemu/builder-vm.sh` builds an image in a KVM
  guest.
- Test the Pi Zero 2 W (512 MB) on real hardware before we promise it. The pp-zero
  lab VM is a proxy and not a Pi. It has no VideoCore GPU, no hardware decode and
  no ARM code. Its `gpu` output runs on llvmpipe and says little about a Pi.

## 5. Lessons

- The daemon does not serve `*.toml` files or `_update/` from `/media/`. The first live
  run showed that `/media/portapixel.toml` gave the admin password and the WiFi key to
  the LAN. `/media/` has no session check.
- `--player-cmd none` sets the player off (`player_state: "disabled"`). Use it for
  development on a desktop. The health marker treats it as healthy.
- The reboot ladder counts crashes and watchdog restarts only. A restart from a person,
  from a settings change or from the nightly job does not count.
- Rule: the watchdog measures each duration with the monotonic clock
  (`time.Now()`), never with the wall clock. A device with no RTC gets a step of
  its wall clock at the first sync of chrony. The step is hours or days, and mpv
  already runs then. The first version used `time.Now().In(zone)`. `In` removes
  the monotonic reading. An image on the screen then looked hours old, and the
  watchdog restarted mpv and counted the restart. This happened on most cold
  boots (ab9b606). `Options.Local` gives a time of day in the zone of the device.
  Use it for the nightly restart and the clock of the fallback screen only.
- The device ID falls back to a hash of the host name when there is no Pi serial, no DMI
  UUID and no physical NIC. Only a development machine gets there.
- A day that is not in `power_days` has no on-period. The screen stays off that day.
- A `playlist.toml` with no items is not a fault. The editor makes one before the first
  item.
- Rule: the screen-off order is: stop the player, then switch the display off
  with DPMS. The screen-on order is the reverse. The player is the DRM master
  while it runs, and only the master may set the DPMS property.
  `internal/device/power` owns both steps for that one reason. The hold of the
  DRM device is the off state: the kernel puts the display on again when the last
  handle closes. The screen-on step must end the hold for every power method
  (0de6a4f), or the player cannot start its display.
- A manual `screen-on` or `screen-off` holds until the screen schedule crosses an EDGE.
  A hold that ended at the next tick made the button useless.
- Rule: a `[[schedule]]` rule has both times or neither. Both empty is the whole day,
  which is how "weekends: this playlist" is written. Before this, such a rule matched
  nothing at all and the person saw the default playlist with no word about why.
- `status.warnings` is a list of `{code, message}`. The UI matches the code. It matched
  the first words of the message before, and one better sentence broke a banner.
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
- A program never reads a line that a person reads. The updater took the last word of
  `portapixeld <version> <arch>` as the version, so each real update failed. Each binary
  answers `version --json`. `updater.BinaryInfo` is the writer and the reader.
- A test double for the one function that touches the world hides the only path that
  matters. Each updater test gave its own version reader, so the real one never ran. To
  cover a real reader, start the test binary again from `TestMain` as the program under
  test. This needs no compiler and works on Windows and Linux.
- Put a fact where its life is correct. The health marker of an update matters for one
  boot only, so its place is tmpfs: `/run/portapixel/health/<version>.ok`. A restart of
  the machine clears it, so an old marker cannot exist. The first fix kept the marker on
  flash. It then needed an arm step, an order rule and a loop that wrote the marker
  again, and that loop wrote to flash 17,000 times a day when the gate stayed armed. The
  `.bad` marker and `.swap-pending` stay on flash, because they must survive a restart.
- A tag rule must name the cause, not the symptom. The fault was two kernels in
  `/lib/modules`. The first `@image` tag also took the GPU, WiFi and network firmware
  from an on-box install. `@image` is only for a package that installs a kernel or
  writes the boot chain. QEMU needs no firmware, so CI cannot see this fault.
- Rule: a file that a user supplies is never active content. `/media/` has no
  session and has the same origin as the admin UI. Each answer with a user file
  has `Content-Security-Policy: sandbox` and `nosniff`. SVG is not a media kind
  now, and `/media/` answers 404 for it. A correct media type for a script
  carrier once made a sideloaded SVG able to run a script with the session of
  the admin. When you correct a type, ask what the wrong type protected.
- `os.ModeDevice` is also set for a character device. A block device has `ModeDevice`
  set and `ModeCharDevice` clear. Without the second test, `/dev/null` is a good target.
- `/api/status` needs no session, so each field on it is public. The first version told
  the LAN which devices had the default passwords: a port sweep gave a list of root
  shells. The report has three levels: loopback (the pairing code), trusted (a session,
  the fleet heartbeat) and everyone else.
- One rule, one function. Three passes over the fleet playlists asked three different
  questions. Two of them wrote to flash at each poll, and one deleted the object store
  when a playlist had the name `media`.
- Record a reboot before it happens. Everything in RAM goes away with it. `state.json`
  counts reboots, and a loop stops the automatic actions (plan 3.3, rung 4).
- The value on the wire and the value in the file are two values. A second Connect
  wrote the claim secret of a code pairing into `portapixel.toml`.
- A guard that protects two fields must not refuse the one route that owns them. The
  lock on `server.url` refused the write of the pairing that succeeded, and the UI
  showed a fault for a pairing that worked.
- An image build needs about 7 GB of free disk on the test box, and each earlier worker
  left its 4 GB raw images under `/root/ppwork`. Remove the raw images of a session
  when it ends. The `.img.gz` is sufficient: `gunzip` gives the raw image again.
- A device keeps the IDs of the commands that it ran. A server whose database was made
  again gives the same IDs to new commands, and the device then acknowledges them and
  does not run them. HEAD clears the list at each new pairing (a80fa15). The lab client
  of build 0.3.0-lab1 showed the fault.
- On the test container, `free -m` reports the RAM of the Proxmox host (64 GB). The
  real limit is in `/proc/meminfo` (4 GB). `builder-vm.sh` reads the second and
  refuses to start a builder VM that is larger than `MemAvailable`. The builder
  takes 3072 MB by default and keeps its root in a tmpfs of half that size. Stop
  the lab VMs first, or lower `--mem`.
- A job that PUBLISHES must wait for every gate. A job that only tests must not. The
  first pipeline pushed the container image before the boot test ran, and a push cannot
  be undone. `server-image` and `release` now need each gate.
- A value that a test only prints is not a check. The boot test read the image version
  and printed it. It read `0.` for `0.0.1-ci1`: `expect -re` with `\S+` returns on the
  first character that arrives. A captured value needs an end mark, and the test must
  compare it.
- A `[` in a Tcl string in double quotes is a command substitution. Use `<` and `>` as
  marks in an expect script.
- `rc-service X start` from inside another OpenRC service is a second rc inside the
  first. It answers "started" and does nothing. zram-init is in the boot runlevel, with
  its size computed in its conf.d file.
- A `const` that a page reads above its own line gives a blank page and an error in
  the console only. Drive each page in a browser after a change. No test found the
  blank Activity page.
- A cable in a dead network costs 10 s more at boot: one bounded DHCP attempt. A
  move of `networking` out of the boot runlevel would not help: `portapixeld` has
  `use net` and `rc_parallel` is off.
- The release also ships `portapixel-os-<version>.tar.gz`: `install.sh` needs
  `packages.list`, `packages-read.awk` and `overlay/`, so `install.sh` alone is not an
  install path.
- `grep -q "$VERSION"` reads a full stop as "any character". Use `grep -qF`, or compare
  the whole value.
- A contract with three fields needs three checks. The updater read the name, the version
  and the processor of a binary, and did not check the name.
- When the order of two processes decides the result, the design is wrong. The health
  gate removed the marker after the daemon started. The gate now removes an old marker in
  `start_pre` only. The daemon writes its marker again while `.swap-pending` names it.
- The first run on Linux found five tests that pass on Windows only. Run the tests on
  Linux before you trust them: `go test` on the Alpine test container works, also with
  `-race` after `apk add build-base`.
- Alpine and a distroless container have no `/etc/mime.types`.
  `mime.TypeByExtension` then knows almost no extension, and Go cannot identify
  AVIF or Matroska from the first bytes. Windows reads the registry and Ubuntu
  has mailcap, so the two hide it. `internal/playlist` has the one table. Call
  `playlist.MediaType` directly: a fix that depends on the `init` of an imported
  package fails when an import goes away.
- A `--root` rule must not run in the on-box mode. The installer counted the kernels of
  the host. The `@image` tag marks the packages that only an image gets.
- BuildKit ignores `ARG TARGETARCH` unless the stage starts with
  `FROM --platform=$BUILDPLATFORM`. Without it, a cross compile is an emulation.
- A console of 80 columns cuts a long typed line inside the value that a test reads. Send
  `stty cols 200` first and keep the line short.
- A public key for `-ldflags -X` is the base64 line only. A flag cannot hold a new line.
- A wrong path in `git add a b c` stops the whole command, and the next commit then holds
  other files. Check the list of a commit after each commit of a group.
- A `.part` file in a directory that the caller removes at each attempt is not a resume.
  The updater staged into `<version>.staging` and removed it in a defer, so a slow link
  started from zero each time. A download now goes to `releases/.download/`.
- `O_CREATE` on a device path is a bug. On devtmpfs it makes a file in RAM, and an
  install reports success on a disk that it did not change. `partx` and `partprobe` do
  not wait for the partition nodes: wait for them.
- Make the decision and the action one step. `power.Set` read the state outside the
  transition, answered OK and did nothing.
- The content of a fleet manifest decides a write to flash. Its rules go to the scheduler
  only. With one compare key, a schedule edit wrote each playlist again on each screen.
- Do not wrap `http.ResponseWriter` to change the 404 of the router. The wrapper cannot
  tell that 404 from the 404 of a handler, and it hides an interface that
  `http.MaxBytesReader` needs. Register a catch-all route.
- `internal/version.PublicKey` is a `var`. A build flag cannot set a `const`.
- A second `Apply` of one release made `previous` equal to `current`, and `prune` then
  removed the release that runs. `prune` never removes the running release now.
- Three packages each had a copy of "how to call the fleet server". They are one package
  now, `internal/fleet`. Ask this question early for each new client.
- The clone rule must not read `last_seen`. The manifest poll of the same device sets it
  some seconds before the heartbeat. The first server saw each hardware repair as a
  clone, and the repair path was never used. The test passed because it did not poll and
  it moved the clock 26 h. Write a fleet test with the real call order: enroll or
  manifest first, then heartbeat. Now a new hardware ID sets `needs_confirm`. A conflict
  needs the two IDs to take turns in 10 minutes.
- `internal/server.New` builds the one route stack. The command and the tests use it.
  Before this, the test harness built a copy, so a guard could leave the real server and
  each security test still passed. Check a guard test with a mutation: remove the guard,
  see the test fail, put the guard back.
- With an empty `public_url`, the server permits only loopback names. In Docker that
  made the first run impossible: the admin could not open Settings to set the URL. Set
  `PORTAPIXEL_PUBLIC_URL`. The environment has priority over `server.toml`.
- The server reads `X-Forwarded-For` and `X-Forwarded-Proto` only from an address in
  `trusted_proxies`. Without this, behind a proxy, one bad card stops enrollment for the
  fleet and five bad logins lock out the admin.
- Before release 1, a change to schema version 1 in place is permitted. `db.Open` reports
  an old development database in one clear sentence. From release 1, migrations are
  append-only.
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
- `config.ChangeClass` has three classes: `live`, `player` (restart the player)
  and `reboot`. The reasons are in `internal/config/change.go`.
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
- Rule: a remote command that saves the config obeys the same no-write rule as
  `provision`. The rename command does not write when the config came from the shadow
  copy, the defaults or a repair, or when a hand edit has a fault. It reads an unread
  hand edit first. One lock (`cfgWriteMu`) holds each read, change, save and adopt.
- One DNS label holds 63 octets. A name of 64 characters gives an mDNS name that
  answers no query, and the announcer still logs success. So the name limit is 63.
- The object name of a fleet item on a device is `<sha8>-<name>`, not the upload
  name. Match the item on screen by hash, never by name.
- A video preview sets the `.muted` property, not only the attribute. Use the
  fragment `#t=0.5` to get a first frame, and give a grid video its `src` only
  when the tile comes into view.
- `go test` keeps a result by the files that the test binary opens. A child `node`
  process opens nothing that counts. A wrapper must read the files that it tests.
- The Edit tool writes `\uXXXX` in new text as the real character. Write escapes
  with a script, then search the diff for U+200B to U+206F and U+FEFF.
- The Bash tool makes `\` into `\`. Write code that holds backslashes with Edit or
  Write.
- Before a push, run `go vet ./...` at EACH new commit, not only at HEAD. 37930f6
  passed at HEAD and failed alone.
- `git archive HEAD os | tar -x` over an old checkout does not remove files that HEAD
  deleted. `os/install.sh` copies every file in `os/default-media`, so an old checkout
  put the four old slides into the lab4 image. Extract into an empty directory.
- A rename that the device refuses still shows as acked on the server. The reason is
  only in the ops log and the status warning of the device.
- Rule: only "not found" may give the `token-revoked` answer. The first lookup of a device
  token gave it for each error. A busy database then told every screen that polled to
  forget its pairing. Now only `db.ErrNotFound` gives it (f195732). A request with no
  header gets a 401 with no code, and a fault of the database gets a 500. The device keeps
  its pairing for both.
- Rule: never save the config that the server runs. It holds the environment and the
  flags. A save wrote `PORTAPIXEL_TRUSTED_PROXIES` and the `--listen` flag into
  `server.toml`, and a proxy that a person removed later stayed trusted. `saveField`
  reads the file, changes one field and writes the file again (8342826).

### Lessons of the change to mpv

- The reports of the conversion stages 2A to 2c are not in the repository. The
  commit messages from `69259cc` to `d0feef1` hold their evidence: lab numbers,
  what was seen on screen, and what was refused. Run `git log 69259cc..d0feef1`.
  The contract is `docs/ARCHITECTURE.md`, sections 7 and 7a.
- A transition is a cover and not a live mix. When an item ends,
  `transitions.lua` takes `screenshot-raw window bgra`, shows it as an OSD
  overlay, lets the next item load under it, and moves the cover away. The load
  gap is hidden. It works with `gpu` and with `drm`, and every fault ends in a
  cut. A live mix was refused in the lab (2026-10-08):
  - lavfi `xfade` takes 4:4:4 frames only, so it converts each frame of the old
    video. It needs frames in memory, so hwdec must be a `-copy` mode for the
    whole item. A graph error makes the next item fail to load. On the Zero 2 W
    proxy, 2 s of decode and `xfade` took 2.9 s at 720p and 5.5 s at 1080p. That
    is not real time. The moving crossfade uses `overlay` and `fade` with alpha
    instead, and `playback.motion` limits it to fast devices.
  - Pre-rendered clips need one clip for each pair of items. A shuffle, a
    schedule or a playlist change makes new pairs, and a clip takes seconds to
    encode on a Zero.
  - A shader sees one video only. libplacebo needs GLSL 130 (GLES 3.0), and vc4
    on the Zero 2 W is GLES 2.0.
  - A second mpv or a compositor does not work: there is one DRM master.
- On vo=drm, `screenshot-raw window` scales the frame to the window and keeps
  no aspect. It also ignores `video-zoom` and `video-pan`. The copy of a
  transition then filled the black bars (seen in the lab), and a copy after a
  zoom jumped. The script scales the copy back into the video rectangle of
  `osd-dimensions` and makes the bars black. Ken Burns runs on `gpu` only for
  the same reason. On `drm` a zoom is also a software scale of the whole picture
  at each step. It took 131 % of a core on the Zero 2 W proxy.
- On vo=drm, `screenshot-raw window` scales the frame to the window in the
  format of the frame, and mpv does not check the result (player/screenshot.c).
  For a palette PNG (`pal8`) whose size is not the size of the window, the copy
  is all 0, the alpha too. A transition then started from black, with no fault.
  The bit depth is not the cause: an 8-bit palette PNG of 1280x720 failed, and a
  4-bit one of 1280x800 worked (lab7 fix). `screenshot-raw video` gives the right
  pixels at the size of the frame.
- On vo=drm, `screenshot-raw window` also draws the OSD into the copy. A copy of
  the next item under the cover of the old item must use `video`.
- On vo=drm, each change of `video-pan` sends `VOCTRL_SET_PANSCAN`, and vo_drm
  runs its whole reconfig: a new scaler, a new frame buffer and a full software
  draw. On the throttled Zero proxy a push or a slide-in into an image showed no
  step until the end, while the script ran 23 steps. A slide-out and a fade,
  which change only the overlay, moved. It was not a regression: the scripts of
  4c5e688 and 0917424 did the same. A still picture as the next item is now a
  copy that moves in the overlay of the old item.
- Stacked overlays blend wrongly on vo=drm: the colours wash out and pink and
  blue specks show. Overlays that sit side by side and do not touch are right.
  So `fade` puts one black square in the place of the copy, and `split` uses two
  halves that do not touch. Keep to one overlay for anything that overlaps.
- mpv 0.40 or later is necessary. An older mpv refuses `--load-commands` and
  `--load-positioning` and does not start. mpv also stops on an option that it
  does not know. Test each new option of `command.go` with the mpv of the image.
- `--hwdec=auto-safe` does not try `v4l2m2m`. It is not in the whitelist of mpv
  (`video/decode/vd_lavc.c`), so a Pi decoded H.264 in software. `command.go`
  gives `v4l2m2m-copy` when `/proc/device-tree/model` starts with `Raspberry Pi`.
  The HEVC decoder of a Pi 4 and a Pi 5 needs the V4L2 request API. The FFmpeg
  of Alpine aarch64 does not have it. So HEVC is software on every Pi.
- `vd-lavc-o=num_capture_buffers=8` goes in the per-file options of a video.
  Never put it on the command line. mpv gives the option to each decoder that it
  opens. A decoder that does not know it writes `AVOption ... not found`.
  On the command line that line came for each image and each redraw of the
  fallback screen. By default, the Pi H.264 decoder takes 20 buffers of about
  3.1 MB at 1080p from the CMA area. A 512 MB Pi has 128 MB of it.
- The kernel shows tty1 each time mpv stops, for example at a restart, until the
  next mpv takes the display. tty1 still holds the text of the BIOS and the boot
  loader, because vgacon copies the VGA text screen at boot. The kernel command
  line (`quiet vt.global_cursor_default=0 consoleblank=0 logo.nologo`) hides the
  messages, the cursor and the logo, but not this text. The init script writes
  `ESC[H ESC[2J ESC[3J` to `/dev/tty1` in `start_pre`. It tests for the character
  device first, because a redirect to a missing node makes a plain file in
  `/dev`. Measured in QEMU: after a kill of mpv the screen showed the SeaBIOS and
  SYSLINUX lines for about 1 s. With the clear, 187 screenshots in a row were
  black.
- When mpv ends on its own, read the connectors at once. If nothing is connected,
  the exit is part of the wait for a display (D44) and it does not count. The
  loop read them every 5 s before, and a television that drops its HDMI signal
  could reboot the device (92d8046).
- libdvdcss is in the image, and the file must stay. Alpine 3.23 builds mpv
  0.40.0-r8 with `libdvdnav.so.4` as a needed library. `libdvdnav` needs
  `libdvdread.so.8`, and `libdvdread` 6.1.3-r2 needs `libdvdcss.so.2` (checked with
  `readelf -d` on the packages on 2026-10-09). The library is not loaded with
  `dlopen`. The musl loader refuses `libdvdread` when the file is missing, so
  mpv does not start. The device never reads a DVD.
  The daemon gives mpv the path of a file in the media root, and the extension
  list has no disc format. `LICENSES-THIRD-PARTY.md` names the library.
- mpv sends a `property-change` event only for a value that changed. A report
  that can come again with the same text needs a count in its value, as
  `user-data/pptr/moved` and `user-data/pptr/fault` have. The fake mpv sends
  events the same way, so a test sees the fault.
- A message that says "something changed" must never go into a queue that drops
  a message when it is full. Nothing asks again. `PlaylistChanged` and
  `DisplayChanged` are flags that the loop reads.
- The key of a rate limit has few values: an event and a playlist, never the
  details. An item name or a temporary file name in the key made each line new,
  and 40 broken items pushed each other out of a memory of 32.
- The player compares two manifests to decide on a new list, and a new list
  starts at item 0. Each field of the manifest is a field that plays. A title in
  it restarted the playlist at each edit of the title.
- A rule change can name a playlist before the scan that finds it. The rename,
  the fleet manifest and the unpair all did that, and the screen showed the
  fallback for a moment. `scheduleChanged` scans first.
- A state that belongs to one mpv ends with it. The nightly grace survived a
  screen-off and stopped the next mpv. `clearList` is the one place.
- Since Go 1.25 a short slice that does not escape is on the stack, and Go can
  move a stack. An address that goes to the kernel as a plain integer needs the
  heap: `power.kernelBuffer` is not inlined, so its result escapes.
- `time-pos` is the time of the frame on the screen. It stands still while mpv
  waits for a next frame that is far ahead (a slideshow video). The first fix
  used `demuxer-cache-time`. mpv 0.40 gives it as unavailable when the queue of
  the demuxer is empty, and that is the normal state between two far frames. The
  lab7 device restarted mpv at each pass of such a video. The stall rule now
  waits two frame intervals: `estimated-vf-fps`, else `container-fps` (none
  below 0.1 fps), else `duration` divided by `estimated-frame-count`.
- A Go file that imports a package cannot share the package block with a
  function of that name. The test helper `playlist()` blocked the import of
  `internal/playlist`, so it is `manifestOf()`.
- Git Bash rewrites an argument that starts with a slash into a Windows path.
  It does this when the program is not a Git Bash program. `gh api /repos/...`
  became `C:/Program Files/Git/repos/...`. Drop the first slash
  (`gh api repos/...`) or set `MSYS_NO_PATHCONV=1`.
