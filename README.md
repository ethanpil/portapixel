# PortaPixel

PortaPixel is a digital signage appliance. Write one image to a USB stick.
Plug the stick into a PC or a Raspberry Pi that has an HDMI port. The screen
plays images and videos in a loop.

You manage one screen from a web browser on the same network. You do not need
an account and you do not need the internet. For many screens, pair each one
to a central fleet server. The server then holds the playlists, the times and
the update approvals for the whole fleet.

PortaPixel is free and open source software, built on Alpine Linux and the
mpv video player. One Go module builds the device software and the fleet
server. See "How it is built" below.

## Status

**PortaPixel is pre-release software.**

- The project is proven in QEMU only. Nobody has run it on real hardware yet.
- The lab has a virtual machine with the limits of a Raspberry Pi Zero 2 W. It
  has 512 MB of memory, the speed of an SD card and a slow CPU. It is a proxy.
  It has no VideoCore GPU and no hardware video decoder.
- The Raspberry Pi image builds in CI. It has never started on a real
  Raspberry Pi.
- The fleet server container builds and answers in CI. No fleet has run on it.
- Treat every hardware claim below as untested until the release checklist
  says otherwise. See `docs/release-checklist.md`.
- The project has no minimum memory size yet. A 512 MB device needs a fresh
  measurement on real hardware. In the lab proxy, mpv used 87 to 136 MB of RAM
  with the `drm` video output. It used 108 to 215 MB with the `gpu` output. A PC
  with no GPU made these numbers, not a Pi.
- The first boot of a machine with less than 1 GB makes a zram swap device as
  large as the memory.

## Hardware

This list is the plan's hardware target, adjusted for what the project has
built and confirmed so far.

| Platform | Hardware | Image | Confirmed |
|---|---|---|---|
| x86_64 | A PC, a NUC or a thin client, BIOS or UEFI | `portapixel-<version>-x86_64.img.gz` | Boots and plays in QEMU only. |
| aarch64 | Raspberry Pi 3, 4, 5, Zero 2 W, Pi 2 v1.2, CM4, CM5 | `portapixel-<version>-aarch64.img.gz` | Builds in CI, and CI reads the boot files inside it. No Raspberry Pi has started it. |

The Pi Zero 2 W and the Pi 2 v1.2 are low-RAM devices. Treat the Pi Zero 2 W
as untested until a real device passes the checklist.

### Video decode

- On a Pi Zero 2 W, Pi 3 and Pi 4, the V4L2 decoder of the board decodes H.264
  video. This is untested on real hardware.
- A Pi 5 has no H.264 decoder. It decodes H.264 in software.
- HEVC (H.265) decodes in software on every Raspberry Pi. A Pi 4 and a Pi 5
  have an HEVC decoder, but the FFmpeg of Alpine cannot use it.
- Use H.264 for video on a Raspberry Pi.
- On an x86_64 PC, VA-API decodes in hardware when the graphics driver has it.
- Software decode is the fallback on every board.

## Quick start

1. Download a release image and write it to a USB stick of 8 GB or more.
2. To set up WiFi before the first boot, open `portapixel.toml` on the `PPMEDIA`
   partition of the stick, on any computer. Every setting is there, turned off
   with a `#`. Remove the `#` from the lines that you want and from their
   `[table]` line. See `docs/install.md`.
3. Put the stick into the PC or the Raspberry Pi and turn the machine on.
4. Connect the machine to a screen over HDMI.
5. Find the address of the device. A device with nothing to play shows its
   address and a QR code on the screen. A new device plays the demo videos
   instead, so read the address from your router, or use
   `portapixel-<last4>.local`, where `<last4>` is the last four characters
   of the device ID. See `docs/install.md`.
6. Open that address in a browser on the same network.
7. Sign in with the password `portapixel`. The web UI tells you to change it.
8. The default root password for SSH is also `portapixel`. Change this too.

For full steps, including static addresses and headless setup, read
`docs/install.md`.

## Two ways to install

**The image.** Flash a release image to a stick. The image carries a boot
partition, a system partition and a media partition. This is the fastest way
to try PortaPixel.

**`install.sh`.** Run `os/install.sh` as root on a stock installation of the
pinned Alpine release. This puts PortaPixel onto a system that you already
run. Media and configuration then live under `/var/lib/portapixel/`, unless
you name a spare partition with `--media-partition`.

Both paths give the same daemon, the same services and the same fleet
behaviour. See `docs/install.md` for the exact steps and flags.

## The fleet server

`portapixel-server` manages many screens from one place: playlists, the
default playlist, schedule rules, and screen on and off times. Each screen
still works fully on its own; a server is only for running many screens
together. Run the server as one static binary, or with the Docker image in
`deploy/`. See `deploy/README.md` for setup, including the first-run password
and the public address.

## How it is built

- One Go module makes two binaries: `portapixeld` (the device) and
  `portapixel-server` (the fleet server).
- The build always sets `CGO_ENABLED=0`.
- The web assets have no build step and no npm dependency. The file in the
  repository is the file that ships, embedded with `go:embed`.
- The player is mpv. The daemon starts it on the display, with no desktop,
  and controls it over a socket. mpv runs as a separate program.
- `docs/ARCHITECTURE.md` is the contract for package names, paths and wire
  formats between the two binaries.

## Repository layout

```
cmd/            entry points for portapixeld and portapixel-server
internal/       the packages that both binaries share, and the device
                and server subpackages
web/            the two admin UIs and their shared code
os/             packages.list, install.sh, the boot files, the image builder
deploy/         server install files: Docker, OpenRC, systemd
docs/           this documentation
scripts/        ci-lint.sh
tests/          Go tests, QEMU tools, the two-machine lab, dev servers
```

## Development

Run the checks that CI runs, before every commit:

```sh
gofmt -l .
go vet ./...
go test ./...
scripts/ci-lint.sh
```

The tests run on Windows and on Linux. Code that only Linux can run, such as
disk and CEC calls, sits behind a build tag or a runtime check.

To try the player and the two admin UIs without real hardware:

- `tests/device-admin/devserver` runs the device admin UI against a mock
  device.
- `tests/server-admin/devserver` runs the fleet admin UI against a mock
  server, with `tests/server-admin/seed` to fill it with sample screens.

To run the device daemon on a desktop, give it `--player-cmd none` (no player)
or the path of an mpv program. See `docs/ARCHITECTURE.md`, section 7.

To try a real image, use the QEMU tools in `tests/qemu`. `tests/qemu/lab`
holds a permanent lab of three virtual machines: a fleet server, a device, and
a proxy for a Raspberry Pi Zero 2 W. It tests pairing and sync between two real
network peers.

## Documentation

- `docs/install.md` — flash the image, set up WiFi, install onto an existing
  Alpine host, or move a trial install onto an internal disk.
- `docs/content.md` — playlists, images, videos, transitions, Ken Burns,
  schedules and sideloading media.
- `docs/settings.md` — every key of `portapixel.toml`.
- `docs/fleet.md` — pairing, what the fleet server manages, and security.
- `docs/updates.md` — how updates work, and how to apply one from a stick.
- `docs/troubleshooting.md` — symptoms, causes and actions.
- `docs/release-checklist.md` — the manual hardware checklist for a release.
- `docs/ARCHITECTURE.md` — the contract between packages, for contributors.
- `docs/README.md` — a one-page index of all of the above.

## Licence

PortaPixel is MIT licensed. See `LICENSE`.

The image and the two binaries also carry third-party software: Alpine
Linux, mpv with FFmpeg and libplacebo, and a small number of Go modules.
Alpine builds mpv and FFmpeg under the GPL. PortaPixel runs mpv as a separate
program and links none of it. See `LICENSES-THIRD-PARTY.md` for the full list
and its licences.

The image also carries seven demo videos for the first boot. They are not
MIT licensed and not CC0. Five come from Mixkit and use the Mixkit Stock Video
Free License. `LICENSES-THIRD-PARTY.md` gives the source and the licence of
each one.
