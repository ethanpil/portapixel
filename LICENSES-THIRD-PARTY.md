# Third-party licences

PortaPixel is MIT licensed. See `LICENSE` in this repository.

This file lists the software that a PortaPixel image carries and the Go modules
that the two binaries use. It is a factual list. It is not legal advice, and it
is not a legal opinion about your own use of the product. It lists copyright
licences only. It does not cover patent licences.

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
Alpine package manager installed. PortaPixel does not patch or rebuild any
Alpine package.

The licence column of the Alpine tables is the `license` field of the Alpine 3.23
package.

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
| zram-init | GPL-2.0-only | Compressed swap in RAM on a device with less than 1 GiB of memory. |
| syslinux (x86_64 BIOS boot) | GPL-2.0-or-later | MBR boot code and the BIOS loader. |
| GRUB (x86_64 UEFI boot) | GPL-3.0-or-later | The UEFI loader. |
| Raspberry Pi firmware (`raspberrypi-bootloader`) | Broadcom proprietary licence for the boot blobs, plus GPL-2.0 for the Linux parts | Redistribution is permitted on Raspberry Pi hardware. See the `LICENCE.broadcom` file in the package. |
| linux-firmware (`linux-firmware-*`) | Mixed vendor licences, each in `WHENCE` | Redistributable binary blobs. Each family has its own terms. |
| wireless-regdb | ISC | The regulatory database. |

## Graphics, hardware video decode and audio

| Component | Licence | Notes |
|---|---|---|
| Mesa (`mesa-dri-gallium`, `mesa-gbm`, `mesa-egl`, `mesa-gles`, `mesa-va-gallium`) | MIT AND SGI-B-2.0 AND BSL-1.0 | The graphics drivers. mpv draws through them with `video_output = gpu`. |
| libdrm | MIT | Kernel mode setting. |
| libva, intel-media-driver, libva-intel-driver | MIT | Hardware video decode on x86_64 (VA-API). |
| ALSA (`alsa-lib`, `alsa-utils`, `alsa-ucm-conf`) | LGPL-2.1-or-later (library), GPL-2.0-or-later (tools) | Sound output. |
| v4l-utils (`cec-ctl`) | GPL-2.0-or-later, LGPL-2.1 (libraries) | The CEC screen power path (D31). The Raspberry Pi video decoder is a V4L2 device of the kernel. |

## The player: mpv and its libraries

The daemon starts mpv as a separate program and controls it over a socket. The
packages below come from the dependencies of the Alpine package `mpv`. Alpine
builds mpv and the FFmpeg libraries with GPL parts, so these programs are GPL
software. The source of each package is in the Alpine aports tree.

| Component | Licence | Notes |
|---|---|---|
| mpv | GPL-2.0-or-later | The player. Version 0.40 or later is necessary. |
| FFmpeg libraries (`ffmpeg-libavcodec`, `-libavformat`, `-libavfilter`, `-libavutil`, `-libavdevice`, `-libswscale`, `-libswresample`) | GPL-2.0-or-later AND LGPL-2.1-or-later | mpv reads and decodes the images and the videos through them. |
| libplacebo | LGPL-2.1-or-later | The video renderer of mpv. |
| shaderc, glslang-libs, spirv-tools | Apache-2.0 (shaderc, spirv-tools). BSD-3-Clause AND BSD-2-Clause AND MIT AND Apache-2.0 AND GPL-3.0-or-later (glslang-libs) | The shader compilers that libplacebo uses. |
| LuaJIT (`luajit`) | MIT | Runs the transition script `transitions.lua` of the daemon. The script needs its FFI. |
| libass | ISC | The text renderer of mpv. With FreeType (FTL OR GPL-2.0-or-later), HarfBuzz (MIT), FriBidi (LGPL-2.1-or-later) and fontconfig (MIT). |
| Video format libraries of FFmpeg: `x264-libs`, `x265-libs`, `xvidcore` | GPL-2.0-or-later | H.264, HEVC and MPEG-4 part 2. |
| Video format libraries of FFmpeg: `libvpx`, `libdav1d`, `aom-libs`, `libSvtAv1Enc`, `rav1e-libs`, `libjxl`, `libwebp`, `libtheora`, `libvpl` | BSD-3-Clause (libvpx, libwebp, libtheora), BSD-2-Clause (libdav1d), BSD-2-Clause AND custom (aom-libs), BSD-3-Clause-Clear (libSvtAv1Enc), BSD-2-Clause custom (rav1e-libs), Apache-2.0 (libjxl), MIT (libvpl) | VP8, VP9, AV1, JPEG XL, WebP, Theora. |
| Audio libraries of FFmpeg: `opus`, `libvorbis`, `libogg`, `libflac`, `mpg123-libs`, `lame-libs`, `libsndfile`, `libopenmpt`, `soxr`, `speexdsp`, `libsamplerate`, `rubberband-libs`, `webrtc-audio-processing-2`, `fftw-double-libs` | BSD-3-Clause (opus, libvorbis, libogg, libopenmpt, speexdsp), BSD-3-Clause AND GPL-2.0-or-later (libflac), LGPL-2.1-only (mpg123-libs), LGPL-2.0-or-later (lame-libs), LGPL-2.1-or-later (libsndfile, soxr), BSD-2-Clause (libsamplerate), GPL-2.0-only (rubberband-libs), custom (webrtc-audio-processing-2), GPL-2.0-or-later (fftw-double-libs) | Sound formats and audio filters. |
| Image and filter libraries: `libpng`, `libjpeg-turbo`, `lcms2`, `zimg`, `vidstab`, `libdovi`, `libdisplay-info`, `libarchive`, `uchardet-libs` | Libpng (libpng), BSD-3-Clause AND IJG AND Zlib (libjpeg-turbo), MIT (lcms2, libdovi, libdisplay-info), WTFPL (zimg), GPL-2.0-or-later (vidstab), BSD-2-Clause AND BSD-3-Clause AND Public-Domain (libarchive), MPL-1.1 (uchardet-libs) | Pictures, colour and archives. |
| Network protocol libraries of FFmpeg: `gnutls`, `mbedtls`, `libssh`, `libsrt`, `librist`, `libzmq` | LGPL-2.1-or-later (gnutls), Apache-2.0 OR GPL-2.0-or-later (mbedtls), LGPL-2.1-or-later BSD-2-Clause (libssh), MPL-2.0 (libsrt, libzmq), BSD-2-Clause (librist) | FFmpeg links them. The player reads local files only. |
| Window system and sound server client libraries: X11 (`libx11`, `libxext`, `libxrandr`, `libxv`, `libxscrnsaver`, `libxpresent` and others), Wayland (`wayland-libs-client`, `-cursor`, `-egl`), `libxkbcommon`, `pipewire-libs`, `libpulse`, `jack`, `sndio-libs`, `libvdpau`, `vulkan-loader` | MIT (X11 and Wayland libraries), X11 (libx11), MIT AND MIT-open-group AND HPND AND HPND-sell-variant AND LicenseRef-digital-equipment-corporation (libxkbcommon), LGPL-2.1-or-later (pipewire-libs, libpulse, jack), ISC (sndio-libs), Apache-2.0 (vulkan-loader) | mpv links these libraries. PortaPixel starts no window system and no sound server. mpv draws on DRM/KMS and plays through ALSA. |
| Terminal and text output libraries: `libsixel`, `libcaca` | MIT (libsixel), WTFPL (libcaca) | mpv links them for terminal video output. PortaPixel does not use that output. |
| Disc libraries: `libdvdnav`, `libdvdread`, `libdvdcss`, `libbluray`, `libudfread`, `libcdio`, `libcdio-paranoia` | GPL-2.0-or-later (libdvdnav, libdvdread, libdvdcss), LGPL-2.1-or-later (libbluray, libudfread), GPL-3.0-or-later (libcdio), GPL-2.0-or-later AND LGPL-2.0-or-later (libcdio-paranoia) | mpv links them. See "libdvdcss" below. |
| `libcamera`, `libcamera-ipa` | LGPL-2.1-or-later AND GPL-2.0-or-later | Needed by `pipewire-libs`. PortaPixel uses no camera. |
| General libraries of the packages above (`glib`, `harfbuzz`, `zlib`, `zstd-libs`, `xz-libs`, `libxml2`, `llvm21-libs` and others) | Mixed. See each package. | The `packages.manifest` of a release names all of them. |

### libdvdcss

`libdvdcss` is a library that reads encrypted DVDs. The Alpine package `mpv`
links `libdvdnav`. `libdvdnav` needs `libdvdread`, and `libdvdread` needs
`libdvdcss.so.2`. The loader of musl refuses to load `libdvdread` when that file
is missing. mpv then does not start. So the file is in the image, and PortaPixel
cannot remove it without a rebuilt mpv.

We checked this with `readelf -d` and with the musl loader on the Alpine 3.23
packages `mpv` 0.40.0-r8, `libdvdnav` 6.1.1-r1, `libdvdread` 6.1.3-r2 and
`libdvdcss` 1.4.3-r0.

PortaPixel never reads a disc. The daemon gives mpv the path of an image or a
video file in the media root. The list of extensions has no disc format. Some
countries limit software that decrypts DVDs. Check the law of your country if
this matters to you.

## Network and remote access

| Component | Licence | Notes |
|---|---|---|
| OpenSSH (`openssh-server`) | BSD-2-Clause and ISC, with some public domain parts | SSH is on by default (D23). |
| chrony | GPL-2.0-only | Time synchronisation (D40). |
| wpa_supplicant | BSD-3-Clause | WiFi. |
| ifupdown-ng | GPL-2.0-or-later | `/etc/network/interfaces`. |
| dbus-libs | AFL-2.1 OR GPL-2.0-or-later | A library that the sound libraries of mpv need. No D-Bus daemon runs. |
| curl | curl licence (MIT/X derivative) | Field debugging and the documented rescan hook. |
| ca-certificates | MPL-2.0 (the Mozilla CA bundle) | TLS trust for the fleet server and the release check. |

## Fonts

The image has no system font package. The daemon draws the fallback screen with
fonts inside its binary.

| Component | Licence | Notes |
|---|---|---|
| IBM Plex Sans and IBM Plex Mono | SIL Open Font License 1.1 | Shipped as local `woff2` files in `web/shared/fonts/`. A device on a closed network cannot fetch a remote font. |
| Go Regular, Go Bold and Go Mono (Bigelow & Holmes) | BSD-3-Clause | Compiled into `portapixeld` through `golang.org/x/image/font/gofont`. The fallback screen uses them. |

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
| `golang.org/x/net` | BSD-3-Clause | Dependency of `github.com/miekg/dns`. |
| `golang.org/x/crypto` | BSD-3-Clause | bcrypt for the server admin password. |
| `golang.org/x/sys` | BSD-3-Clause | The DRM ioctls of the DPMS screen power (`internal/device/power`). Also a dependency of minisign. |
| `github.com/hashicorp/mdns` | MIT | The mDNS announcement (D20). |
| `github.com/miekg/dns` | BSD-3-Clause | Dependency of `github.com/hashicorp/mdns`. |
| `github.com/skip2/go-qrcode` | MIT | The QR code on the fallback screen (D18). |
| `golang.org/x/image` | BSD-3-Clause | Draws the text of the fallback screen (D18): the OpenType reader and the Go fonts. |
| `golang.org/x/text` | BSD-3-Clause | Dependency of `golang.org/x/image`. |
| The Go standard library | BSD-3-Clause | Everything else. |

## Default content

The image carries seven demo videos in `/opt/portapixel/default-media/`. A new
device copies them into the `default` playlist at the first boot (D37).

Five of the videos are clips from Mixkit (<https://mixkit.co/>). We thank Mixkit
and the people who made the clips. The clips use the **Mixkit Stock Video Free
License**:

- The licence: <https://mixkit.co/license/#videoFree>
- The Mixkit terms: <https://mixkit.co/terms/>

The licence permits free commercial use. It lets a person copy, modify,
distribute, perform and broadcast the clips. It is non-exclusive, worldwide and
sub-licensable. It does not require attribution, but Mixkit asks for it. Read the
licence and the terms for the exact words.

These clips are NOT under the MIT licence of PortaPixel, and they are NOT CC0.
The Mixkit terms still apply to everyone who takes the clips from this
repository or from an image. A person who wants the clips for another use should
get them from Mixkit.

| File | Source | Licence |
|---|---|---|
| `01-meadow.mp4` | Mixkit, clip 4075 (`mixkit-countryside-meadow-4075-hd-ready.mp4`) | Mixkit Stock Video Free License |
| `02-creek.mp4` | Mixkit, clip 51585 (`mixkit-flying-over-a-relaxing-creek-full-of-rock-on-the-51585-hd-ready.mp4`) | Mixkit Stock Video Free License |
| `03-forest.mp4` | Mixkit, clip 50847 (`mixkit-the-camera-slowly-slides-into-the-tranquil-forest-on-a-50847-hd-ready.mp4`) | Mixkit Stock Video Free License |
| `04-beach.mp4` | Mixkit, clip 5016 (`mixkit-waves-coming-to-the-beach-5016-hd-ready.mp4`) | Mixkit Stock Video Free License |
| `05-mountain-road.mp4` | Mixkit, clip 41576 (`mixkit-going-down-a-curved-highway-through-a-mountain-range-41576-hd-ready.mp4`) | Mixkit Stock Video Free License |
| `06-waterfall.mp4` | Source unknown, to be confirmed by the maintainer. The maintainer supplied it as `100195-video-720.mp4`. | Source unknown, to be confirmed by the maintainer |
| `07-roses.mp4` | Source unknown, to be confirmed by the maintainer. The maintainer supplied it as `100899-video-720.mp4`. | Source unknown, to be confirmed by the maintainer |

Plan D37 asks for CC0 content, and these files do not meet that rule. The owner
decided on 2026-10-09 to keep all seven videos in the repository and in the
image as they are. The maintainer must confirm the source and the licence of
`06-waterfall.mp4` and `07-roses.mp4`.
`os/default-media/README.md` has the same list, with the length of each file.
