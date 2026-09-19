# Third-party licences

PortaPixel is MIT licensed. See `LICENSE` in this repository.

This file lists the software that a PortaPixel image carries and the Go modules
that the two binaries use. It is a factual list. It is not legal advice, and it
is not a legal opinion about your own use of the product.

Where the text lives:

- On a device: `/usr/share/portapixel/LICENSES-THIRD-PARTY.md`, and the admin UI
  links it at `/licenses`.
- Each Alpine package: `/usr/share/licenses/<package>/` when the package ships a
  copy, and always in the source package of the Alpine aports tree at
  <https://gitlab.alpinelinux.org/alpine/aports>.
- Each Go module: the `LICENSE` file of the module, inside the module cache at
  `$(go env GOMODCACHE)/<module path>@<version>/`.
- The installed package list of a release is the `packages.manifest` artifact of
  that release (D50). It names every package and its exact version.

PortaPixel links no third-party code into its own binaries except the Go modules
below. Every other component is a separate program or a shared library that the
Alpine package manager installed.

## Operating system and base tools

| Component | Licence | Notes |
|---|---|---|
| Alpine Linux base (`alpine-base`, `alpine-baselayout`) | MIT | The distribution. |
| Linux kernel (`linux-lts`, `linux-rpi`) | GPL-2.0-only WITH Linux-syscall-note | Separate program. PortaPixel makes no kernel module. |
| BusyBox (`busybox`, `busybox-suid`, `busybox-openrc`) | GPL-2.0-only | Shell, init helpers and the small tools. |
| OpenRC | BSD-2-Clause | The service manager. |
| musl libc | MIT | The C library of Alpine. |
| eudev, udev-init-scripts | GPL-2.0-or-later, LGPL-2.1-or-later | Device nodes and hotplug. |
| apk-tools | GPL-2.0-only | The package manager. Build time and OS updates only. |
| e2fsprogs | GPL-2.0-only, LGPL-2.0 (libraries) | `mkfs.ext4` for PPROOT. |
| exfatprogs | GPL-2.0-or-later | `mkfs.exfat` for PPMEDIA. |
| dosfstools | GPL-3.0-or-later | `fsck.vfat` for PPBOOT. |
| gptfdisk (`sgdisk`), sfdisk, parted, partx | GPL-2.0-or-later | Partition work of the first boot and of `install-to-disk`. |
| mkinitfs | GPL-2.0-only | Builds the initramfs. |
| zram-init | GPL-2.0-only | Compressed swap in RAM on the low tier. |
| syslinux (x86_64 BIOS boot) | GPL-2.0-or-later | MBR boot code and the BIOS loader. |
| GRUB (x86_64 UEFI boot) | GPL-3.0-or-later | The UEFI loader. |
| Raspberry Pi firmware (`raspberrypi-bootloader`) | Broadcom proprietary licence for the boot blobs, plus GPL-2.0 for the Linux parts | Redistribution is permitted on Raspberry Pi hardware. See the `LICENCE.broadcom` file in the package. |
| linux-firmware (`linux-firmware-*`) | Mixed vendor licences, each in `WHENCE` | Redistributable binary blobs. Each family has its own terms. |
| wireless-regdb | ISC | The regulatory database. |

## Display, media and audio

| Component | Licence | Notes |
|---|---|---|
| Chromium | BSD-3-Clause, with many bundled components under their own licences | The browser. Its own `about:credits` page lists every bundled component. |
| cage | MIT | The single-window Wayland kiosk compositor. |
| wlroots | MIT | The compositor library under cage. |
| wlr-randr | MIT | Sets the output mode and the rotation. |
| seatd, libseat | MIT | Seat management, so the browser takes DRM master without root. |
| Wayland (`wayland-libs-*`) | MIT | The display protocol libraries. |
| Mesa (`mesa-dri-gallium`, `mesa-gbm`, `mesa-egl`, `mesa-gles`, `mesa-va-gallium`) | MIT | The graphics drivers. |
| libdrm | MIT | Kernel mode setting and DPMS calls. |
| libva, intel-media-driver, libva-intel-driver | MIT | Hardware video decode on x86_64. |
| ALSA (`alsa-lib`, `alsa-utils`, `alsa-ucm-conf`) | LGPL-2.1-or-later (library), GPL-2.0-or-later (tools) | Sound output. |
| v4l-utils (`cec-ctl`) | GPL-2.0-or-later, LGPL-2.1 (libraries) | The CEC screen power path (D31). |
| FFmpeg libraries | LGPL-2.1-or-later, and GPL-2.0-or-later for some builds | Chromium decodes through them. |

## Network and remote access

| Component | Licence | Notes |
|---|---|---|
| OpenSSH (`openssh-server`) | BSD-2-Clause and ISC, with some public domain parts | SSH is on by default (D23). |
| chrony | GPL-2.0-only | Time synchronisation (D40). |
| wpa_supplicant | BSD-3-Clause | WiFi. |
| ifupdown-ng | GPL-2.0-or-later | `/etc/network/interfaces`. |
| dbus | AFL-2.1 OR GPL-2.0-or-later | Chromium looks for a bus. |
| curl | curl licence (MIT/X derivative) | Field debugging and the documented rescan hook. |
| ca-certificates | MPL-2.0 (the Mozilla CA bundle) | TLS trust for the fleet server and the release check. |

## Fonts

| Component | Licence | Notes |
|---|---|---|
| Noto (`font-noto`, `font-noto-cjk`, `font-noto-emoji`) | SIL Open Font License 1.1 | The system fonts. Live web pages use them. |
| IBM Plex Sans and IBM Plex Mono | SIL Open Font License 1.1 | Shipped as local `woff2` files in `web/shared/fonts/`. A device on a closed network cannot fetch a remote font. |

## Go modules

These are the only third-party components that are compiled into `portapixeld`
and `portapixel-server`. The versions are in `go.mod`.

| Module | Licence | Why it is here |
|---|---|---|
| `github.com/BurntSushi/toml` | MIT | Parses `portapixel.toml` and `playlist.toml`. |
| `aead.dev/minisign` | MIT | Verifies the minisign signature of a release (D47). |
| `modernc.org/sqlite` | BSD-3-Clause | Pure-Go SQLite for the fleet server. |
| `modernc.org/libc`, `modernc.org/mathutil`, `modernc.org/memory` | BSD-3-Clause | Dependencies of `modernc.org/sqlite`. |
| `github.com/dustin/go-humanize` | MIT | Dependency of `modernc.org/sqlite`. |
| `github.com/google/uuid` | BSD-3-Clause | Dependency of `modernc.org/sqlite`. |
| `github.com/mattn/go-isatty` | MIT | Dependency of `modernc.org/sqlite`. |
| `github.com/ncruces/go-strftime` | MIT | Dependency of `modernc.org/sqlite`. |
| `github.com/remyoudompheng/bigfft` | BSD-3-Clause | Dependency of `modernc.org/libc`. |
| `golang.org/x/net` | BSD-3-Clause | The WebSocket client of the browser control path. |
| `golang.org/x/crypto` | BSD-3-Clause | bcrypt for the server admin password. |
| `golang.org/x/sys` | BSD-3-Clause | Indirect, through minisign. |
| `github.com/hashicorp/mdns` | MIT | The mDNS announcement (D20). |
| `github.com/miekg/dns` | BSD-3-Clause | Dependency of `github.com/hashicorp/mdns`. |
| `github.com/skip2/go-qrcode` | MIT | The QR code on the fallback screen (D18). |
| The Go standard library | BSD-3-Clause | Everything else. |

## Default content

The default playlist images are CC0 1.0 (public domain dedication). The
maintainer supplied them (D37).
