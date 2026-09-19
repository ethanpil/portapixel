# Release checklist

This is the manual hardware checklist for a PortaPixel release. Check off
each line by hand, on real hardware, before a release ships. Nothing in
`docs/` or in CI can replace this list: CI proves that the daemon and the
browser come up; this list proves that a person can watch the screen play.

The checklist comes from the project plan, corrected for what the project
actually built: the display stack is Chromium inside `cage`, not the
WebKit-based browser the plan first named, and there are two navigation
rungs (the DevTools protocol, then a relaunch), not three. See
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

## Audio and network

- [ ] HDMI audio works on a Raspberry Pi and on an x86 machine.
- [ ] WiFi connects from the values in `portapixel.toml`.
- [ ] A static address, set in `portapixel.toml`, works.

## Playback

- [ ] A mixed playlist with an image, a video and a web page item plays, with
      every transition tried at least once.
- [ ] A web page item on a slow site shows an acceptable gap and always
      recovers.
- [ ] Single-URL kiosk mode with `refresh_seconds` runs for 24 hours with no
      manual step.

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
- [ ] DPMS turns a real monitor on and off.

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
- [ ] VA-API hardware video decode on x86_64.
- [ ] V4L2 hardware video decode on a Raspberry Pi.
- [ ] CEC, on a real television, in a real `cage` session.
- [ ] DPMS, on a real monitor, in a real `cage` session.
- [ ] A display with a misleading EDID, and the `video_mode` override that
      corrects it.
- [ ] HDMI audio output, end to end, on both architectures.
- [ ] WiFi, end to end, on both architectures.
- [ ] The aarch64 image, on every named Raspberry Pi model. No model has
      started this image yet.
- [ ] The media partition growth step, on a real SD card, eMMC module, and
      USB stick, not only in a virtual disk.
- [ ] Rotation, on real GPU drivers rather than the virtual GPU that CI uses.
- [ ] `install-to-disk` onto a real SATA disk and a real eMMC module.
- [ ] The Chromium start-up network connections, measured against a real
      firewall rather than a packet capture in a test network.

## The 30-day soak gate

Before a 1.0 release, run two devices, one Raspberry Pi Zero 2 W and one
x86_64 machine, on a mixed playlist that includes web page items, for 30
days with no manual intervention.

Pass criteria:

- [ ] Nobody touched either device during the 30 days.
- [ ] Memory use stays stable across every nightly restart.
- [ ] The ops log holds no entry that nobody can explain.
