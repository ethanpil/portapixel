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

The aarch64 image builds in GitHub Actions (the test container has no qemu-user binfmt),
and CI reads the boot files inside it. No Raspberry Pi has started this image.

## 3. Divergences from the plan

| Plan | What we did | Why |
|---|---|---|
| D3, D4, D5: WPE WebKit + cog on DRM/KMS, no compositor, three navigation rungs | Chromium in kiosk mode inside `cage`. Two navigation rungs: CDP, then relaunch. The `--autoplay-policy=no-user-gesture-required` flag is now REQUIRED. `seatd` and `wlr-randr` are in the image. | Alpine removed `cog`, `wpewebkit` and `wpebackend-fdo` after 3.21 (checked with `apk` against 3.22, 3.23 and edge on 2026-09-18). The maintainer chose Chromium + cage on 2026-09-18. `cage` shows one fullscreen window. It is not a desktop. Weston, sway and labwc stay out of scope. |
| Section 7: GStreamer packages | Not installed. | Chromium has its own media stack. |
| D39: browser tmpfs about 96 MB | About 256 MB. | A Chromium profile is larger than a WebKit one. |
| D6, D7: vendored Bootstrap for the two admin UIs | One hand-written stylesheet, `web/shared/pp.css`. No Bootstrap. | The wireframes give a full custom design (IBM Plex, moss green, `oklch` colours). Bootstrap below it would be more code, not less. Open decision for the maintainer. |
| Fonts from Google Fonts (wireframes) | IBM Plex Sans and Mono as local `woff2` files in `web/shared/fonts/` | A device on a closed network cannot get remote fonts. The licence is OFL. |
| D52: a sideload bundle names its version | The version comes from the staged binary, after the signature check. | The release assets are `portapixeld-<arch>`, `<name>.minisig` and `SHA256SUMS`. Not one of the three names holds a version, and a person who downloads them from GitHub has three loose files. Running a binary that minisign already proved is safe; a naming rule would be one more thing to get wrong. |
| D37: a CC0 image set, 1920x1080 | Seven H.264 720p demo videos, 59 MB in total, in `os/default-media/`. Five come from Mixkit (Mixkit Stock Video Free License). Two have no recorded source. | The maintainer supplied them on 2026-09-22. They are not CC0. The maintainer must confirm that the terms permit them in a public image. |
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
- `cage` 0.2.1 does not read `XCURSOR_THEME`. It gives wlroots no theme name, and wlroots
  then asks for the theme `default`. With no such theme, wlroots draws its own arrow in
  the middle of the screen. The image has a transparent theme, `portapixel-blank`, and
  `/usr/share/icons/default/index.theme` inherits it. Chromium does read the `XCURSOR_*`
  variables for the pointer in a page, so they stay.
- The cursor files are copies, not links. A checkout on Windows changes a link into a
  text file.
- Chromium 149 calls Google at start unless flags stop it. Measured with a QEMU packet
  dump: `OptimizationHints` stops the optimization guide calls,
  `NetworkTimeServiceQuerying` stops `clients2.google.com`, and only the three `--gcm-*`
  endpoint flags stop Google Cloud Messaging. `--disable-background-networking` and
  `--disable-sync` do not stop it. With the flags, an idle screen makes no DNS request
  in 185 s. Two TLS connections stay, to `www.google.com` and `accounts.google.com`,
  one time at each browser start. No flag stops them. Use a firewall if that matters.
- `--disable-client-side-phishing-detection` does not exist in Chromium 149. Nor do
  `--disable-pdf-extension` and `--enable-oop-rasterization`. Chromium ignores a switch
  that it does not know, with no message. Check a new switch against the binary with
  `strings -a /usr/lib/chromium/chrome | grep -x -- '--name'` before you add it.
- Chromium 149 keeps the page of a URL item in memory after the daemon goes back to the
  player. Three URL items made three more renderer processes and about 380 MB more
  RSS, and it never came back. The name `BackForwardCache` in `--disable-features`
  stops it: the process count does not grow and the renderer goes back to 109 MB
  after each item. The count at rest depends on the RAM: 8 at 1 GB, 9 at 2 GB, because
  Chromium keeps a spare renderer when it has room. Judge the fix by growth, not by
  the number. Measured in QEMU on 2026-09-22 (BIOS, 1 GB, 2 CPUs). A 30 minute soak
  with a URL item in each loop showed no growth. On a slide-only playlist the flag
  shows nothing, so measure with URL items.
- Flags measured on 2026-09-22 and refused. The noise of the measurement is 5 MB of
  PSS and 0.2 points of CPU, from three baseline runs. Inside the noise:
  `--renderer-process-limit=1`, `--disable-extensions`, `--disk-cache-size`,
  `--disable-gpu-shader-disk-cache`, `--num-raster-threads=1` (Chromium already sets
  it on 2 CPUs), `--force-device-scale-factor=1` and the group `--disable-notifications
  --disable-speech-api --disable-print-preview --no-pings --disable-hang-monitor
  --disable-prompt-on-repost`. `--enable-low-end-device-mode` saved 6.5 MB and cost
  0.2 points of CPU: a hardware checklist item.
- `--disable-dev-shm-usage` BREAKS the picture. It moves the shared memory of Chromium
  from `/dev/shm` (483 MB) to `/tmp` (64 MB). The renderer dies again and again. Both
  paths are RAM, so the flag saves nothing.
- `--js-flags=--expose-gc` with a `window.gc()` call at each item COSTS memory: 589 MB
  against 553 MB of PSS, a 211 MB against a 177 MB renderer, and 0.4 points more CPU.
- `--js-flags=--max-old-space-size=256` makes a runaway page worse. V8 stops the
  renderer in the middle of the item, CDP stops, the watchdog restarts the browser, and
  four restarts started the reboot ladder. With no cap the dwell timer ends the item,
  the memory goes back and the slides come back each time.
- `boot-dev.sh shot` cannot show a URL item: the screendump gives the last player frame
  for the whole dwell. Prove the page with the CDP target list, the ops log and the
  CPU of the gpu process.
- A DNS name in a packet dump is in label form. `strings | grep` does not find it. Parse
  the DNS questions.
- The browser output is in `/var/cache/kiosk/browser.log` (tmpfs, 1 MiB limit). Read it
  first when the screen is black.
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
- `supervise-daemon` sends KILL after about 5 s unless `retry` is set. The daemon needs
  more time to stop the browser: `retry="TERM/25/KILL/5"`.
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
- `install.sh` makes the `seat` group. The post-install script of `seatd` cannot run for
  another architecture.
- With UEFI, the firmware framebuffer takes `/dev/dri/card0` and the GPU is `card1`. Do
  not write `card0` into code.
- QEMU needs the package `qemu-hw-display-virtio-vga` to give the guest a DRM device.
- The x86_64 root filesystem is 1558 MB with Chromium and the seven demo videos, 43 %
  of the 3.5 GB PPROOT (lab4 build, 41332bf). It was 1488 MB with the old slides.
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
- Rule: a file that a user supplies is never active content. An SVG can hold a script.
  `/media/` has no session and has the same origin as the admin UI. A correct media type
  for `.svg` made a sideloaded SVG able to run a script with the session of the admin.
  Each answer with a user file has `Content-Security-Policy: sandbox` and `nosniff`. An
  SVG in an `<img>` element still shows, because that policy does not apply to an image
  load. The old `text/plain` answer was safe only by accident. When you correct a type,
  ask what the wrong type protected.
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
- The rung 1 "control session dead" check runs only in a URL window or a kiosk page. A
  dead CDP socket under a live player page is found by the heartbeat rule, not by CDP.
- Any local process can drive the CDP port 9222. A page cannot: the
  `--remote-allow-origins` flag names the loopback endpoint only. The player secret
  never reaches a URL item page: `Page.navigate` sends no referrer, the page has
  `Referrer-Policy: no-referrer`, and a `?k=` GET from another origin executes but is
  not readable.
- An image build needs about 7 GB of free disk on the test box, and each earlier worker
  left its 4 GB raw images under `/root/ppwork`. Remove the raw images of a session
  when it ends. The `.img.gz` is sufficient: `gunzip` gives the raw image again.
- A device keeps the IDs of the commands that it ran. A server whose database was made
  again gives the same IDs to new commands, and the device then acknowledges them and
  does not run them. HEAD clears the list at each new pairing (a80fa15). The lab client
  of build 0.3.0-lab1 showed the fault.
- On the test container, `free -m` reports the RAM of the Proxmox host (64 GB). The
  real limit is in `/proc/meminfo` (4 GB). `builder-vm.sh` reads the second.
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
- A device with no network reaches its picture in 16.6 s, the same as a device with a
  network. A cable in a dead network costs 10 s more: one bounded DHCP attempt. A move
  of `networking` out of the boot runlevel would not help: `portapixeld` has `use net`
  and `rc_parallel` is off.
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
- Alpine and a distroless container have no `/etc/mime.types`. `mime.TypeByExtension`
  then knows almost no extension, and Go cannot identify SVG, AVIF or Matroska from the
  first bytes. Windows reads the registry and Ubuntu has mailcap, so the two hide it.
  `internal/playlist` has the one table. Call `playlist.MediaType` directly: a fix that
  depends on the `init` of an imported package fails when an import goes away.
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
- `--browser-cmd` gives the browser no environment, and the browser runs in the
  working directory of the daemon.
- Before a push, run `go vet ./...` at EACH new commit, not only at HEAD. 37930f6
  passed at HEAD and failed alone.
- `git archive HEAD os | tar -x` over an old checkout does not remove files that HEAD
  deleted. `os/install.sh` copies every file in `os/default-media`, so an old checkout
  put the four old slides into the lab4 image. Extract into an empty directory.
- A rename that the device refuses still shows as acked on the server. The reason is
  only in the ops log and the status warning of the device.
