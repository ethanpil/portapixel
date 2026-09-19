# Install PortaPixel

This page covers both ways to put PortaPixel onto a machine: flash a release
image to a stick, or run `install.sh` on a machine that already runs Alpine
Linux. It also covers WiFi setup before the first boot, and moving a trial
install onto an internal disk.

## Before you start

Turn off Secure Boot in the firmware of the target machine (D32). PortaPixel
does not sign its boot loader yet, so a machine with Secure Boot on refuses
to start it.

Get a USB stick of 8 GB or more. A 4 GB stick technically works, but it
leaves little room for media.

## 1. Flash the image

Download the release image for your hardware:

- `portapixel-<version>-x86_64.img.gz` for a PC, a NUC or a thin client.
- `portapixel-<version>-rpi-aarch64.img.gz` for a Raspberry Pi.

Unzip the file. You get one `.img` file. Write this file to the stick. **The
write erases everything already on the stick.**

### Windows

Use a disk imaging tool that writes a raw `.img` file to a USB stick. Balena
Etcher is one free tool that does this job; other tools of this kind also
work. Point the tool at the `.img` file and at the stick, and start the
write.

### macOS and Linux

Find the device name of the stick first. On macOS, run `diskutil list`. On
Linux, run `lsblk`. Then run `dd` as root:

```sh
sudo dd if=portapixel-<version>-x86_64.img of=/dev/diskN bs=4M status=progress
sync
```

Replace `/dev/diskN` with the real device name of the stick, not a partition
of it. A wrong device name overwrites the wrong disk.

## 2. Set up WiFi before the first boot (optional)

Do this step only when the machine has no wired network and no monitor.

1. Put the stick into a computer of any kind. Do not put it into the target
   machine yet.
2. Open the partition named `PPMEDIA`. Every operating system can read it.
3. Open `portapixel.toml` in a text editor.
4. Set the `[network]` keys:

   ```toml
   [network]
   mode = "dhcp"
   wifi_ssid = "your network name"
   wifi_psk = "your network password"
   wifi_country = "US"
   ```

5. Save the file and eject the stick properly.

`wifi_country` is the two-letter radio region of your country, for example
`US` or `DE`. Leave it out and some WiFi radios show fewer channels, so a
5 GHz network can stay invisible.

### A static address

To give the device a fixed address instead of DHCP, set these keys instead:

```toml
[network]
mode = "static"
address = "192.168.1.50/24"
gateway = "192.168.1.1"
dns = ["192.168.1.1", "1.1.1.1"]
```

## 3. Boot the device

PortaPixel boots from a USB stick, an SD card, eMMC, or an internal SATA or
NVMe disk (D53). Put the stick into the target machine, connect a screen
over HDMI, and turn the machine on.

**Do not cut the power during the first boot.** The first boot grows the
media partition to fill the rest of the stick, and this takes about one
minute. If you cut the power anyway, the growth step picks up where it left
off at the next boot, so nothing is lost. Waiting for it to finish once is
still the simple path.

## 4. Find the device

The screen shows the device name, its address, and a QR code. Scan the QR
code, or type the address into a browser on the same network.

Every device also answers at `portapixel-<last4>.local`, where `<last4>` is
the last four characters of the device ID shown on the screen. Use this name
when the device has not been given its own name yet.

## 5. Sign in

The web password is `portapixel`. The root password for SSH is also
`portapixel`. The web UI shows a loud notice until you change both. Open
Settings to change the web password. Open Settings and connect over SSH to
change the root password, or see `docs/troubleshooting.md`.

## Install onto an existing Alpine host

Instead of the image, you can put PortaPixel onto a machine that already
runs a stock, sys-mode install of Alpine Linux, at the exact release that a
PortaPixel release pins.

Run this as root:

```sh
sh install.sh --binary portapixeld --version 0.1.0
```

`install.sh` puts the media, the playlists and `portapixel.toml` under
`/var/lib/portapixel/media`. To keep the sideload workflow of a removable
media partition, name a spare partition instead:

```sh
sh install.sh --binary portapixeld --version 0.1.0 --media-partition /dev/sdb1
```

**`--media-partition` erases everything on that partition.** `install.sh`
refuses a partition that is mounted, that is the running root file system, or
that already carries a PortaPixel label. For every other partition, it asks
you to type the device path back before it formats it:

```
WARNING: install.sh is about to write a new exFAT file system on
  /dev/sdb1
Every file on that partition is lost. This cannot be undone.
Type the device name again to go on:
```

The full set of flags:

| Flag | What it does |
|---|---|
| `--root DIR` | Build a PortaPixel tree at `DIR` instead of the running system. Used to build a release image. |
| `--arch x86_64\|aarch64` | The target architecture. Defaults to the architecture of the host. |
| `--binary PATH` | The `portapixeld` binary to install as the first release. |
| `--version VER` | The version string of that release, for example `0.1.0`. |
| `--alpine-release X.Y.Z` | The exact Alpine release to pin the package repositories to. Required with `--root`. |
| `--media-partition DEV` | Format `DEV` as exFAT and mount it as the media partition. On-box installs only. |
| `--image` | Write the boot, system and media partition entries. Used to build a release image. |

Run `install.sh` again to update the daemon in place, or use the update
paths in `docs/updates.md`.

## Move a trial install onto an internal disk

`install-to-disk` (D54) copies the running system from the stick onto an
internal disk. This lets you try PortaPixel from a stick, then commit to it
without flashing a second time.

**This is not yet proven on real hardware.** The release checklist has the
step of installing onto a SATA disk and onto an eMMC module and booting from
each one. Until that step passes, treat `install-to-disk` as unverified.

To run it from the admin UI, open About, look for disks, and pick a target.
To run it from the command line:

```sh
portapixeld install-to-disk /dev/sda --confirm /dev/sda
```

**This erases everything on the target disk.** `install-to-disk` refuses:

- the disk that the machine is running from,
- a disk smaller than the stick,
- a confirmation string that does not match the device path exactly.

There is no undo. When it finishes, do these three steps, in this order:

1. Power the machine off.
2. Take the USB stick out.
3. Start the machine again and let it boot from the internal disk.

Both the stick and the disk carry the same three partition labels after the
copy. Taking the stick out is what tells the machine which one to boot from.
