#!/bin/sh
# os/build-image.sh -- make a bootable PortaPixel image.
#
# The script is thin on purpose (D51). All the configuration work lives in
# os/install.sh, which an on-box install runs in the same way. This file only
# adds the partitions, the file systems and the boot loader.
#
# Run it as root on an Alpine box or in an Alpine container that has loop
# devices. GitHub Actions gives both.
#
# Build host tools: sgdisk, losetup, mkfs.vfat, mkfs.ext4, mkfs.exfat, apk,
# mkinitfs, cpio, gzip. x86_64 also needs syslinux and grub with grub-efi.
# The loader packages stay on the build host. They never enter the image
# (plan section 6).
set -eu

ARCH=""
BINARY=""
VERSION="0.0.0-dev"
ALPINE_RELEASE=""
OUT=""

IMAGE_MB=4096          # about 4 GB raw. "8 GB" sticks are the practical minimum.
P1_MB=256              # PPBOOT, FAT32, the ESP
P2_MB=3584             # PPROOT, ext4. 3.5 GB, per plan section 5
ROOT_FULL_PCT=85       # fail the build when PPROOT use goes over this

SRC="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"

die() { printf 'build-image.sh: %s\n' "$*" >&2; exit 1; }
say() { printf '==> %s\n' "$*"; }

usage() {
	cat <<'EOF'
Usage: build-image.sh --arch x86_64|aarch64 --alpine-release X.Y.Z [options]

  --arch ARCH              target architecture
  --alpine-release X.Y.Z   exact Alpine release to build from (never a branch)
  --binary PATH            portapixeld binary to install as the release
  --version VER            release version, for example 0.1.0
  --out PATH               output image; the default is
                           portapixel-<version>-<arch>.img
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--arch) ARCH="${2:?}"; shift 2 ;;
	--binary) BINARY="${2:?}"; shift 2 ;;
	--version) VERSION="${2:?}"; shift 2 ;;
	--alpine-release) ALPINE_RELEASE="${2:?}"; shift 2 ;;
	--out) OUT="${2:?}"; shift 2 ;;
	-h|--help) usage; exit 0 ;;
	*) usage >&2; die "unknown option: $1" ;;
	esac
done

[ "$(id -u)" = 0 ] || die "run this script as root"
case "$ARCH" in x86_64|aarch64) ;; *) die "--arch must be x86_64 or aarch64" ;; esac
case "$ALPINE_RELEASE" in
[0-9]*.[0-9]*.[0-9]*) ;;
*) die "--alpine-release must be an exact release like 3.23.2" ;;
esac
[ -n "$OUT" ] || OUT="portapixel-$VERSION-$ARCH.img"

for t in sgdisk losetup mkfs.vfat mkfs.ext4 mkfs.exfat apk mkinitfs cpio gzip; do
	command -v "$t" >/dev/null 2>&1 || die "the build host needs $t"
done
if [ "$ARCH" = x86_64 ]; then
	for t in syslinux grub-install; do
		command -v "$t" >/dev/null 2>&1 || die "the build host needs $t (syslinux, grub-efi)"
	done
fi

# Loop devices. An unprivileged LXC container has none at all, and there is no
# way around that: say so instead of failing later with a puzzling error.
[ -e /dev/loop-control ] || die \
	"no /dev/loop-control: this environment has no loop devices.
 An unprivileged LXC container cannot make them. Build in a VM, on real
 hardware, or on a GitHub Actions runner. tests/qemu/builder-vm.sh starts a
 throwaway VM for exactly this."

# ------------------------------------------------------------------- the clean up
LOOP=""
MNT=""
BOOTMNT=""
cleanup() {
	set +e
	[ -n "$BOOTMNT" ] && { umount "$BOOTMNT" 2>/dev/null; rmdir "$BOOTMNT" 2>/dev/null; }
	[ -n "$MNT" ] && { umount "$MNT" 2>/dev/null; rmdir "$MNT" 2>/dev/null; }
	[ -n "$LOOP" ] && losetup -d "$LOOP" 2>/dev/null
	for d in $EXTRA_LOOPS; do losetup -d "$d" 2>/dev/null; done
}
EXTRA_LOOPS=""
trap cleanup EXIT INT TERM

# --------------------------------------------------------------- 1. the image file
say "make $OUT ($IMAGE_MB MB)"
rm -f "$OUT" "$OUT.gz"
truncate -s "${IMAGE_MB}M" "$OUT"

# --------------------------------------------------------------- 2. the partitions
# p1 PPBOOT FAT32 ESP, p2 PPROOT ext4, p3 PPMEDIA exFAT to the end.
# The geometry is frozen once we ship: an update replaces files, never
# partitions (plan section 5).
say "write the GPT"
sgdisk --zap-all "$OUT" >/dev/null
sgdisk -n "1:0:+${P1_MB}M" -t 1:ef00 -c 1:PPBOOT \
	-n "2:0:+${P2_MB}M" -t 2:8300 -c 2:PPROOT \
	-n 3:0:0 -t 3:0700 -c 3:PPMEDIA "$OUT" >/dev/null
if [ "$ARCH" = x86_64 ]; then
	# The legacy BIOS bootable attribute, GPT bit 2. SeaBIOS chainloads through
	# gptmbr.bin, which only boots a GPT partition that carries this bit.
	# Without it: "Booting from Hard Disk..." and then a hang (mountnas lesson).
	sgdisk --attributes=1:set:2 "$OUT" >/dev/null
fi

# --------------------------------------------------------------- 3. the loop device
say "attach the loop device"
# "losetup -f" then "losetup -P DEV FILE", not "losetup -f --show": busybox
# losetup has no --show, and an Alpine live system has only the busybox one.
LOOP="$(losetup -f)"
[ -n "$LOOP" ] || die "losetup -f gave no free device"
losetup -P "$LOOP" "$OUT" || die "cannot attach $OUT to $LOOP"
P1="${LOOP}p1"; P2="${LOOP}p2"; P3="${LOOP}p3"
i=0
while [ "$i" -lt 10 ]; do
	[ -b "$P1" ] && [ -b "$P2" ] && [ -b "$P3" ] && break
	sleep 1
	i=$((i + 1))
done
if ! { [ -b "$P1" ] && [ -b "$P2" ] && [ -b "$P3" ]; }; then
	# Some containers give loop devices but no partition scanning. Fall back to
	# one loop device per partition, using the offsets from the GPT.
	say "no partition nodes: attach each partition on its own loop device"
	losetup --help 2>&1 | grep -q sizelimit ||
		die "this fallback needs the losetup of util-linux (apk add losetup).
 The busybox losetup cannot limit the size, so mkfs would write past the end of
 the partition and into the next one."
	losetup -d "$LOOP"; LOOP=""
	SS=512
	for n in 1 2 3; do
		start="$(sgdisk -i "$n" "$OUT" | awk '/First sector/ { print $3; exit }')"
		end="$(sgdisk -i "$n" "$OUT" | awk '/Last sector/ { print $3; exit }')"
		[ -n "$start" ] && [ -n "$end" ] || die "cannot read partition $n from the GPT"
		dev="$(losetup -o "$((start * SS))" --sizelimit "$(((end - start + 1) * SS))" \
			-f --show "$OUT")" || die "losetup failed for partition $n"
		EXTRA_LOOPS="$EXTRA_LOOPS $dev"
		eval "P$n=\$dev"
	done
fi
say "p1=$P1 p2=$P2 p3=$P3"

# ------------------------------------------------------------ 4. the file systems
say "make the file systems"
mkfs.vfat -F32 -n PPBOOT "$P1" >/dev/null
mkfs.ext4 -q -F -L PPROOT "$P2"
mkfs.exfat -L PPMEDIA "$P3" >/dev/null
# PPMEDIA stays empty. The first boot and "portapixeld provision" fill it
# (plan section 14). An image that shipped content would fight the user's own
# files on the card.

# ------------------------------------------------------------------ 5. install.sh
MNT="$(mktemp -d)"
mount "$P2" "$MNT"
say "run install.sh --root (the same script an on-box install runs)"
set -- --root "$MNT" --arch "$ARCH" --version "$VERSION" \
	--alpine-release "$ALPINE_RELEASE" --image
[ -n "$BINARY" ] && set -- "$@" --binary "$BINARY"
sh "$SRC/install.sh" "$@"

# ----------------------------------------------------------- 6. the size report
say "size report"
USED_MB="$(du -sm "$MNT" | awk '{ print $1 }')"
PCT=$(( USED_MB * 100 / P2_MB ))
printf '    PPROOT: %s MB of %s MB (%s%%)\n' "$USED_MB" "$P2_MB" "$PCT"
if [ "$PCT" -gt "$ROOT_FULL_PCT" ]; then
	die "PPROOT is $PCT% full, over the $ROOT_FULL_PCT% limit.
 Take a package out of os/packages.list or make P2_MB bigger. Do not ship an
 image with no room for a second release directory (A/B updates need it)."
fi

# ------------------------------------------------- 7. assert the initramfs (D53)
# The classic bug is "boots from USB but not from the internal disk". It must be
# a build failure here, not a field report.
KVER="$(ls "$MNT/lib/modules" | head -n1)"
FLAVOR="${KVER##*-}"
INITRAMFS="$MNT/boot/initramfs-$FLAVOR"
[ -f "$INITRAMFS" ] || die "no $INITRAMFS: install.sh did not build the initramfs"
say "assert the initramfs module set"
LIST="$(gzip -dc "$INITRAMFS" | cpio -t 2>/dev/null || true)"
[ -n "$LIST" ] || die "cannot read $INITRAMFS"
# One name per line. "a|b" means either one is enough.
REQUIRED='usb-storage
sd_mod
nvme
mmc_block|sdhci
ahci|libahci
virtio_blk'
echo "$REQUIRED" | while IFS= read -r want; do
	found=0
	oldifs="$IFS"; IFS='|'
	for alt in $want; do
		# "|| :" matters: without it a miss on the FIRST name makes the loop
		# return non-zero, set -e ends the subshell, and the second name is
		# never even tried.
		echo "$LIST" | grep -q "/$alt\.ko" && found=1 || :
	done
	IFS="$oldifs"
	[ "$found" = 1 ] || die "the initramfs has no $want module (D53)"
	printf '    ok: %s\n' "$want"
done || exit 1

# ------------------------------------------------------------- 8. the boot loader
BOOTMNT="$(mktemp -d)"
mount "$P1" "$BOOTMNT"
CMDLINE="$(head -n1 "$SRC/$( [ "$ARCH" = x86_64 ] && echo x86_64 || echo rpi )/cmdline.base")"
[ -n "$CMDLINE" ] || die "the cmdline.base file is empty"

if [ "$ARCH" = x86_64 ]; then
	say "boot loader: syslinux (BIOS) and grub (UEFI)"
	cp "$MNT/boot/vmlinuz-$FLAVOR" "$BOOTMNT/"
	cp "$INITRAMFS" "$BOOTMNT/"
	# CPU microcode. The loader puts these first in the initrd chain.
	for u in intel-ucode.img amd-ucode.img; do
		[ -f "$MNT/boot/$u" ] || die "no $MNT/boot/$u: is the ucode package installed?"
		cp "$MNT/boot/$u" "$BOOTMNT/"
	done
	sed "s|@CMDLINE@|$CMDLINE|" "$SRC/x86_64/syslinux.cfg.in" >"$BOOTMNT/syslinux.cfg"
	# ldlinux.c32 must sit next to ldlinux.sys on the FAT root, and it must be
	# put there while the partition is mounted. syslinux 6 chains
	# ldlinux.sys -> ldlinux.c32 and the boot stops without it.
	cp /usr/share/syslinux/ldlinux.c32 "$BOOTMNT/"
	mkdir -p "$BOOTMNT/boot/grub"
	sed "s|@CMDLINE@|$CMDLINE|" "$SRC/x86_64/grub.cfg.in" >"$BOOTMNT/boot/grub/grub.cfg"
	# --removable puts the loader at the UEFI fallback path
	# /EFI/BOOT/BOOTX64.EFI. --no-nvram is needed because this is a removable
	# medium, not a fixed disk with a boot manager entry.
	grub-install --target=x86_64-efi --efi-directory="$BOOTMNT" \
		--boot-directory="$BOOTMNT/boot" --removable --no-nvram >/dev/null
	umount "$BOOTMNT"; rmdir "$BOOTMNT"; BOOTMNT=""
	# syslinux --install takes a DEVICE, not a directory. Given a directory it
	# writes no boot code at all and the FAT keeps its dummy VBR, which reads as
	# "This is not a bootable disk" (mountnas lesson).
	syslinux --install "$P1"
	# GPT aware MBR boot code. bs=440 count=1 touches the boot code only and
	# leaves the partition table alone.
	if [ -n "$LOOP" ]; then
		dd bs=440 count=1 conv=notrunc if=/usr/share/syslinux/gptmbr.bin of="$LOOP" 2>/dev/null
	else
		dd bs=440 count=1 conv=notrunc if=/usr/share/syslinux/gptmbr.bin of="$OUT" 2>/dev/null
	fi
else
	say "boot loader: the Raspberry Pi firmware"
	# The firmware, the kernel, the initramfs, the device trees and the
	# overlays. raspberrypi-bootloader puts the firmware files in /boot.
	for f in "$MNT"/boot/bootcode.bin "$MNT"/boot/*.elf "$MNT"/boot/fixup*.dat \
		"$MNT/boot/vmlinuz-$FLAVOR" "$INITRAMFS" "$MNT"/boot/*.dtb; do
		# "|| :" so a glob that matches nothing cannot make the loop return
		# non-zero, which set -e would turn into an exit.
		[ -f "$f" ] && cp "$f" "$BOOTMNT/" || :
	done
	if [ -d "$MNT/boot/overlays" ]; then
		mkdir -p "$BOOTMNT/overlays"
		cp "$MNT"/boot/overlays/* "$BOOTMNT/overlays/"
	fi
	cp "$SRC/rpi/config.txt" "$BOOTMNT/config.txt"
	printf '%s\n' "$CMDLINE" >"$BOOTMNT/cmdline.txt"
	[ -f "$BOOTMNT/bootcode.bin" ] || [ -f "$BOOTMNT/start4.elf" ] ||
		die "no Pi firmware in $MNT/boot: is raspberrypi-bootloader installed?"
	umount "$BOOTMNT"; rmdir "$BOOTMNT"; BOOTMNT=""
fi

# --------------------------------------------------------------- 9. the manifest
say "copy the package manifest next to the image"
cp "$MNT/usr/share/portapixel/packages.manifest" \
	"$(dirname "$OUT")/$(basename "$OUT" .img)-packages.manifest"

umount "$MNT"; rmdir "$MNT"; MNT=""
# An "if", not "[ ... ] && ...": with the fallback loop devices $LOOP is empty,
# the test then returns non-zero and set -e would stop the build right here,
# just before the compress step.
if [ -n "$LOOP" ]; then
	losetup -d "$LOOP"
	LOOP=""
fi
for d in $EXTRA_LOOPS; do losetup -d "$d" 2>/dev/null || true; done
EXTRA_LOOPS=""

# ------------------------------------------------------------------ 10. compress
say "compress"
gzip -9 "$OUT"
say "done: $OUT.gz"
ls -l "$OUT.gz"
