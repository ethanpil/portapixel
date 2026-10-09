# Release checklist

This is the manual hardware checklist for a PortaPixel release. Check off
each line by hand, on real hardware, before a release ships. Nothing in
`docs/` or in CI can replace this list: CI proves that the daemon and mpv
come up; this list proves that a person can watch the screen play.

The checklist comes from the project plan, corrected for what the project
actually built: the player is mpv, directly on DRM/KMS. See
`docs/ARCHITECTURE.md` section 7 and `CONTEXT.md` section 3.

## Boot and display

- [ ] Boot and play on a Raspberry Pi Zero 2 W.
- [ ] Boot and play on a Raspberry Pi 3.
- [ ] Boot and play on a Raspberry Pi 4.
- [ ] Boot and play on a Raspberry Pi 5.
- [ ] Boot and play on one BIOS PC.
- [ ] Boot and play on one UEFI PC.
- [ ] Rotation 90 works on a Raspberry Pi and on an x86 machine.
- [ ] 4K output works where the display and the hardware allow it.
- [ ] The text console never shows: not at boot, not between two items, not
      when the player restarts, and not when the screen comes on. Test on a
      Raspberry Pi and on an x86 machine. Stop mpv by hand and watch the
      screen. The kernel command line has `quiet`,
      `vt.global_cursor_default=0`, `consoleblank=0` and `logo.nologo`, and the
      init script clears tty1.

## Audio and network

- [ ] HDMI audio works on a Raspberry Pi and on an x86 machine.
- [ ] The 3.5 mm jack plays sound on a Raspberry Pi 3 and a Pi 4 with
      `audio.output = "analog"`. The line `dtparam=audio=on` in
      `os/rpi/config.txt` stands above the first `dtoverlay` line. Before the
      fix, the line went to the `vc4-kms-v3d` overlay and never reached the
      jack.
- [ ] WiFi connects from the values in `portapixel.toml`.
- [ ] A static address, set in `portapixel.toml`, works.

## Playback

- [ ] A new image plays the seven demo videos of the `default` playlist at
      the first boot, each for its full length.
- [ ] The maintainer confirmed that the terms of the demo videos permit us to
      give them to other persons in the image. See `LICENSES-THIRD-PARTY.md`.
- [ ] A mixed playlist with images and videos plays. Try each of the 22
      transitions at least once. Set it as the default, as the transition of a
      playlist and as the transition of one item.
- [ ] A playlist of one image stays on the screen. A playlist of one video
      plays again and again with no gap.
- [ ] An item that the player cannot play is skipped, the ops log names it, and
      the rest of the playlist goes on.

## The player on real hardware

Everything in this section is untested outside the lab. The lab has a virtual
machine with the limits of a Pi Zero 2 W. It has no VideoCore GPU and no
hardware video decoder.

- [ ] H.264 decodes in hardware (`v4l2m2m-copy`) on a Raspberry Pi Zero 2 W,
      Pi 3 and Pi 4. `hwdec` in `/api/status` is not `no`. In a 10 minute run
      of the demo videos, `dropped_frames` is 0 or near 0. The CPU load is
      much lower than with software decode.
- [ ] The module `bcm2835-codec` loads by itself on a Pi Zero 2 W, Pi 3 and
      Pi 4. udev loads it by its modalias.
- [ ] The decoder takes its buffers from the CMA area. The daemon asks for 8
      capture buffers (`DecoderOptions` in `command.go`) instead of the default
      20. A 1080p video does not stall on a 512 MB Pi. If a Pi stalls in
      decode, raise the count.
- [ ] The CMA area of 128 MB (`dtparam=cma-128` for the Zero 2 W and the Pi 3
      A+) is large enough. Read `CmaTotal` and `CmaFree` in `/proc/meminfo`
      during a 1080p video and during a transition. A Pi 4 keeps 508 MB and a
      Pi 5 keeps 64 MB, the defaults of their overlays.
- [ ] HEVC video decodes in software on a Pi 4 and a Pi 5. Note the CPU load
      and the dropped frames, so that the docs can say what a Pi can play.
- [ ] A Pi 5 plays H.264 video in software with no stall.
- [ ] `display.video_output = "auto"` takes `gpu` on a Raspberry Pi (vc4 or v3d)
      and on a PC with an Intel or AMD GPU. It takes `drm` on a virtual machine.
- [ ] mpv runs as the `kiosk` account on every board. It opens `/dev/dri/card*`
      and `/dev/snd` and becomes the DRM master.
- [ ] LuaJIT works in the mpv of the aarch64 image. A `fade` shows, and the ops
      log holds no `player.transition.fault`.
- [ ] All 22 transitions look right on a vc4 and a v3d GPU with the `gpu` video
      output: no black frame, no wrong colours, no early frame of the next
      item. Note the number of steps of a 1080p transition on a Pi Zero 2 W.
      The lab numbers come from software rendering and do not apply.
- [ ] The copy of the screen (`screenshot-raw window`) and the texture upload of
      a transition step are fast enough on vc4. If a step takes more than
      about 50 ms, use a shorter transition or a 720p mode.
- [ ] Ken Burns is smooth on vc4 and v3d, and the picture shows no jump at the
      end of an image. It stays off with the `drm` output, and the ops log
      says `player.kenburns.off`.
- [ ] The moving crossfade works on a Pi 5 and on an x86 machine with VA-API:
      both items move and `player.motion.off` does not appear. On a Pi 4 with
      `playback.motion = "on"`, note the dropped frames.
- [ ] MemAvailable stays above about 100 MB on a 512 MB Pi in a 10 minute run
      with a crossfade. Nothing is killed for lack of memory. This is the new
      measurement that the README asks for.
- [ ] A 4K screen on a Pi 4, a Pi 5 and an x86 machine shows a transition
      without a long stall. A copy of the screen is 33 MB at 4K.

## Power cuts and reliability

- [ ] Cut the power mid-video, then boot again. The device comes back
      cleanly with no manual step.
- [ ] Pull the stick, sideload media from a laptop, put the stick back, boot,
      and rescan. The new files play.
- [ ] Corrupt `portapixel.toml` on the media partition, then boot again. The
      shadow copy keeps the device on the network, and the web UI shows the
      warning.
- [ ] Unplug and replug the HDMI cable during video playback. The picture
      returns with no manual step (D44).
- [ ] Boot with the display in standby, then turn it on five minutes later.
      The picture appears, and the ops log shows a wait, not a restart.

## Screen power

- [ ] CEC turns a real television on and off, and follows a schedule.
- [ ] DPMS turns a real monitor on and off. The player stops first, the
      picture goes away, and the display comes on again at the on time with
      the picture of the playlist. No manual step is necessary.

## Fleet

- [ ] Both pairing flows: an enrollment token, and a pairing code approved on
      the server.
- [ ] Enroll three freshly flashed cards from one enrollment token in one
      `portapixel.toml` (D25).
- [ ] Fleet sync of a video of 1 GB or more completes.
- [ ] Interrupt a large fleet download partway through. It resumes from the
      partial file instead of starting again (D24).

## Updates

- [ ] An A/B update installs, and a forced rollback puts the previous
      release back.
- [ ] Apply an update from `PPMEDIA/_update/` with the network unplugged.
- [ ] Apply a tampered update bundle from `PPMEDIA/_update/`. The device
      rejects it (D52).

## Change-me nags and config resilience

- [ ] The change-me nags for the web password and the root password clear
      correctly once each password is changed.

## Storage medium and install-to-disk

- [ ] Boot the x86 image from a SATA or an NVMe disk.
- [ ] Boot the x86 image from eMMC on a thin client.
- [ ] Trial a thin client from a USB stick, run `install-to-disk`, remove the
      stick, and boot from the internal disk (D54).

## Not yet proven on hardware

Everything below is untested outside QEMU as of this release. Do not mark a
release ready until each line above that depends on one of these has passed
on real hardware.

- [ ] Real GPU drivers under load, on each supported board.
- [ ] VA-API hardware video decode on x86_64. `hwdec` in `/api/status` shows a
      VA-API decoder and not `no`. In QEMU the player log shows `libva ... init
      failed` for the decoder probe; on real hardware that line must be absent.
- [ ] CEC, on a real television. The player stops, the television goes to
      standby, and the picture comes back at the on time.
- [ ] DPMS, on a real monitor, with the DRM calls of the daemon.
- [ ] A display with a misleading EDID, and the `video_mode` override that
      corrects it.
- [ ] HDMI audio output, end to end, on both architectures.
- [ ] The card that `audio.output` selects. `internal/device/audio` reads
      `/proc/asound/cards` and matches a name pattern: `hdmi` takes the first
      card whose text holds HDMI, `usb` the first that holds USB, and `analog`
      the first that holds neither. No real sound card has answered that match
      yet. Set each of the four values on each architecture, and listen.
- [ ] WiFi, end to end, on both architectures.
- [ ] The aarch64 image, on every named Raspberry Pi model. No model has
      started this image yet.
- [ ] The media partition growth step, on a real SD card, eMMC module, and
      USB stick, not only in a virtual disk.
- [ ] Rotation, on real GPU drivers rather than the virtual GPU that CI uses.
      Test the images, the videos, a transition and the fallback screen.
- [ ] `install-to-disk` onto a real SATA disk and a real eMMC module.

### The commands of `install-to-disk` that no test has ever run

Every test of `install-to-disk` gives the installer a double of the runner, so
these command lines have never touched a real disk. Read the output of each one
on the first real run, in this order (`internal/device/installer/install.go`):

- [ ] `sgdisk --zap-all <disk>`
- [ ] `sgdisk -n 1:0:+<boot>K -t 1:ef00 -c 1:PPBOOT -n 2:0:+<root>K
      -t 2:8300 -c 2:PPROOT -n 3:0:0 -t 3:0700 -c 3:PPMEDIA <disk>`
- [ ] `sgdisk --attributes=1:set:2 <disk>` (x86_64 only: the legacy BIOS
      bootable bit, which the MBR boot code of syslinux needs)
- [ ] `partx -u <disk>`, and `partprobe <disk>` when the first one fails
- [ ] `mkfs.exfat -L PPMEDIA <media partition>`
- [ ] The first 440 bytes of the MBR, which carry the GPT-aware boot code of
      syslinux on x86_64 and nothing on a Raspberry Pi.
- [ ] `mount` and `umount` of each new partition.

### The update path that no test has ever run

- [ ] `rc-service portapixeld restart` after an update, on a real device. The
      health gate ends its rollback with that command, and every test of the
      gate runs a program that only answers instead.
- [ ] The daemon writes `<run>/health/<version>.ok` in tmpfs, and
      `os/overlay/etc/init.d/portapixeld` clears that directory in `start_pre`.
      Prove both on a device that takes a real update.

## The 30-day soak gate

Before a 1.0 release, run two devices for 30 days with no manual
intervention: one Raspberry Pi Zero 2 W and one x86_64 machine. Give them a
mixed playlist with images, videos and several transitions.

Pass criteria:

- [ ] Nobody touched either device during the 30 days.
- [ ] Memory use stays stable across every nightly restart.
- [ ] The ops log holds no entry that nobody can explain.
