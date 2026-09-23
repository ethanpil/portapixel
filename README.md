# PortaPixel

PortaPixel is a digital signage appliance. Write one image to a USB stick.
Plug the stick into a PC or a Raspberry Pi that has an HDMI port. The screen
plays images, videos and web pages in a loop.

You manage one screen from a web browser on the same network. You do not need
an account and you do not need the internet. For many screens, pair each one
to a central fleet server. The server then holds the playlists, the times and
the update approvals for the whole fleet.

PortaPixel is free and open source software, built on Alpine Linux and
Chromium. One Go module builds the device software and the fleet server. See
"How it is built" below.

## Status

**PortaPixel is pre-release software.**

- The project is proven in QEMU only. Nobody has run it on real hardware yet.
- The Raspberry Pi image builds in CI. It has never started on a real
  Raspberry Pi.
- The fleet server container builds and answers in CI. No fleet has run on it.
- Treat every hardware claim below as untested until the release checklist
  says otherwise. See `docs/release-checklist.md`.
- 1 GB of RAM is the practical minimum. 512 MB fails without swap: this is
  measured, not a guess. The first boot of a machine with less than 1 GB now
  makes a zram swap device as large as the memory. A 512 MB machine then
  starts, but it uses swap for the fallback screen alone.

## Hardware

This list is the plan's hardware target, adjusted for what the project has
built and confirmed so far.

| Tier | Hardware | Image | Confirmed |
|---|---|---|---|
| x86_64 | A PC, a NUC or a thin client, BIOS or UEFI | `portapixel-<version>-x86_64.img.gz` | Boots and plays in QEMU only. |
| aarch64 | Raspberry Pi 3, 4, 5, Zero 2 W, Pi 2 v1.2, CM4, CM5 | `portapixel-<version>-aarch64.img.gz` | Builds in CI, and CI reads the boot files inside it. No Raspberry Pi has started it. |

The Pi Zero 2 W and the Pi 2 v1.2 are low-RAM devices. Chromium uses more RAM
than the browser engine that the plan first named. Treat the Pi Zero 2 W as
untested until a real device passes the checklist.

## Quick start

1. Download a release image and write it to a USB stick of 8 GB or more.
2. To set up WiFi before the first boot, open the `PPMEDIA` partition of the
   stick on any computer and edit `portapixel.toml`. See `docs/install.md`.
3. Put the stick into the PC or the Raspberry Pi and turn the machine on.
4. Connect the machine to a screen over HDMI.
5. Read the address from the screen, or scan the QR code on it.
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
- `docs/ARCHITECTURE.md` is the contract for package names, paths and wire
  formats between the two binaries.

## Repository layout

```
cmd/            entry points for portapixeld and portapixel-server
internal/       the packages that both binaries share, and the device
                and server subpackages
web/            the player, the two admin UIs, and their shared code
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

- `tests/player/mockd` is a mock device daemon for the player SPA.
- `tests/device-admin/devserver` runs the device admin UI against a mock
  device.
- `tests/server-admin/devserver` runs the fleet admin UI against a mock
  server, with `tests/server-admin/seed` to fill it with sample screens.

To try a real image, use the QEMU tools in `tests/qemu`. `tests/qemu/lab`
holds a permanent two-machine lab: one virtual device and one virtual fleet
server, for testing pairing and sync between two real network peers.

## Documentation

- `docs/install.md` — flash the image, set up WiFi, install onto an existing
  Alpine host, or move a trial install onto an internal disk.
- `docs/content.md` — playlists, items, schedules and sideloading media.
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
Linux, Chromium, `cage`, and a small number of Go modules. See
`LICENSES-THIRD-PARTY.md` for the full list and its licences.

The image also carries seven demo videos for the first boot. They are not
MIT licensed and not CC0. `LICENSES-THIRD-PARTY.md` gives the source and the
licence of each one.
