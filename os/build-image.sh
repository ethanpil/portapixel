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
	# The data files these two need. A host with grub but no grub-efi passes the
	# command test and then fails at grub-install, with the loop device attached
	# and both partitions mounted.
	for f in /usr/share/syslinux/ldlinux.c32 /usr/share/syslinux/gptmbr.bin; do
		[ -f "$f" ] || die "the build host needs $f (apk add syslinux)"
	done
	[ -d /usr/lib/grub/x86_64-efi ] ||
		die "the build host needs /usr/lib/grub/x86_64-efi (apk add grub-efi)"
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
	# A busy mount point makes umount fail, and then "losetup -d" fails as well
	# and the loop device leaks with no word about it. Try a lazy umount second,
	# and say so when even that leaves something behind.
	for m in "$BOOTMNT" "$MNT"; do
		[ -n "$m" ] || continue
		umount "$m" 2>/dev/null || umount -l "$m" 2>/dev/null
		mountpoint -q "$m" && printf 'build-image.sh: WARNING: %s is still mounted\n' "$m" >&2
		rmdir "$m" 2>/dev/null
	done
	for d in $LOOP $EXTRA_LOOPS; do
		[ -n "$d" ] || continue
		losetup -d "$d" 2>/dev/null ||
			printf 'build-image.sh: WARNING: the loop device %s is still attached\n' "$d" >&2
	done
	return 0
}
EXTRA_LOOPS=""
# A signal handler must END the script. With a plain "trap cleanup" the handler
# cleans up and the script then GOES ON. It has no loop device and no mounts,
# and gzip packs an image that only looks finished.
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

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
# A du that failed gives an empty value. Arithmetic reads that as 0, and the
# fullness gate below then passes any real size.
case "$USED_MB" in
''|*[!0-9]*) die "du gave no size for $MNT, so the PPROOT fullness gate cannot run" ;;
esac
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
# install.sh has already refused a tree with more than one kernel, so there is
# exactly one directory here. "find -maxdepth 1", not "ls | head -n1": ls sorts
# names and a second kernel would give the wrong one with no word about it.
KVER="$(find "$MNT/lib/modules" -mindepth 1 -maxdepth 1 -type d -exec basename {} \; 2>/dev/null)"
KCOUNT="$(printf '%s\n' "$KVER" | grep -c . || true)"
[ "$KCOUNT" = 1 ] || die "expected one kernel in $MNT/lib/modules, found $KCOUNT:
$KVER"
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
ext4
xhci-hcd|xhci_hcd'
# The virtio disk is a QEMU bus. Only the x86_64 image boots in QEMU, and the
# smoke test attaches its disk on that bus (plan section 17 item 4). The aarch64
# kernel is linux-rpi, which has no virtio at all: it boots from the Raspberry Pi
# firmware, and QEMU cannot boot that path (item 5). Asking for virtio there made
# the first aarch64 build fail for a driver that the image must never need.
if [ "$ARCH" = x86_64 ]; then
	REQUIRED="$REQUIRED
virtio_blk
virtio_pci"
fi
# A driver that the kernel holds INSIDE itself needs no file in the initramfs.
# The Raspberry Pi kernel is built that way for usb-storage and for others, and
# the first aarch64 build in CI stopped here for that reason. What D53 asks is
# that the kernel can find the root on every medium, so a built-in driver is a
# pass and the message says which of the two it was.
BUILTIN="$MNT/lib/modules/$KVER/modules.builtin"
echo "$REQUIRED" | while IFS= read -r want; do
	found=0
	how=""
	oldifs="$IFS"; IFS='|'
	for alt in $want; do
		# "|| :" matters: without it a miss on the FIRST name makes the loop
		# return non-zero, set -e ends the subshell, and the second name is
		# never even tried.
		echo "$LIST" | grep -q "/$alt\.ko" && { found=1; how="a module in the initramfs"; } || :
		if [ "$found" = 0 ] && [ -f "$BUILTIN" ]; then
			grep -q "/$alt\.ko\$" "$BUILTIN" && { found=1; how="built into the kernel"; } || :
		fi
	done
	IFS="$oldifs"
	if [ "$found" != 1 ]; then
		# Say where the driver is NOT, so the next step is clear: another
		# mkinitfs feature, another kernel, or a name that this kernel changed.
		printf 'the kernel tree holds these files for %s:\n' "$want" >&2
		oldifs="$IFS"; IFS='|'
		for alt in $want; do
			find "$MNT/lib/modules" -name "$alt.ko*" >&2 2>/dev/null || :
			grep "/$alt\.ko\$" "$BUILTIN" >&2 2>/dev/null || :
		done
		IFS="$oldifs"
		die "the image can neither load nor hold the $want driver (D53)"
	fi
	printf '    ok: %s (%s)\n' "$want" "$how"
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
	# syslinux.cfg.in and grub.cfg.in name /vmlinuz-lts and /initramfs-lts. The
	# real file names come from the kernel flavour. With another flavour, the
	# boot loader names a file that is not there, and the build still reports
	# success. Check instead of trusting.
	for cfg in "$SRC/x86_64/syslinux.cfg.in" "$SRC/x86_64/grub.cfg.in"; do
		grep -q "vmlinuz-$FLAVOR" "$cfg" ||
			die "$cfg does not name vmlinuz-$FLAVOR, but the kernel is $KVER.
 Update the boot loader template, or install the kernel flavour it names."
		grep -q "initramfs-$FLAVOR" "$cfg" ||
			die "$cfg does not name initramfs-$FLAVOR, but the kernel is $KVER."
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
	# syslinux --install takes a DEVICE, not a directory. Give it a directory and
	# it writes no boot code at all. The FAT then keeps its dummy VBR, and the
	# firmware says "This is not a bootable disk" (mountnas lesson).
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
	# Copy a GROUP of files and count what arrived. A glob that matches nothing
	# stays as the literal pattern in POSIX sh. "[ -f ] && cp || :" then passes
	# over it with no word at all. A Pi image can lose every fixup file, every
	# device tree or every overlay that way. The result is a black screen with no
	# diagnostic, so each group below fails the build when it is empty.
	#   $1 = a name for the message, $2.. = the paths or globs
	copy_group() {
		_name="$1"; shift
		_n=0
		for _f in "$@"; do
			[ -f "$_f" ] || continue
			cp "$_f" "$BOOTMNT/" || die "cannot copy $_f to PPBOOT"
			_n=$((_n + 1))
		done
		[ "$_n" -gt 0 ] || die "no $_name in $MNT/boot.
 Is raspberrypi-bootloader installed, and does it still use this layout?"
		printf '    %s: %s file(s)\n' "$_name" "$_n"
	}

	# The Pi firmware looks for the device trees and the overlays beside
	# config.txt on the boot partition. Alpine's linux-rpi has moved this
	# directory between releases, so look in both places and fail when neither
	# holds anything.
	DTBDIR=""
	for d in "$MNT/boot" "$MNT/boot/dtbs-rpi" "$MNT/boot/dtbs-rpi/broadcom"; do
		if [ -n "$(find "$d" -maxdepth 1 -name 'bcm2*.dtb' 2>/dev/null | head -n1)" ]; then
			DTBDIR="$d"
			break
		fi
	done
	[ -n "$DTBDIR" ] || die "no bcm2*.dtb anywhere under $MNT/boot.
 The Pi firmware needs the device trees on PPBOOT. Look at what linux-rpi
 installed and update this script."
	OVLDIR=""
	for d in "$MNT/boot/overlays" "$MNT/boot/dtbs-rpi/overlays"; do
		[ -d "$d" ] && { OVLDIR="$d"; break; }
	done
	[ -n "$OVLDIR" ] || die "no overlays directory under $MNT/boot.
 config.txt asks for the overlay vc4-kms-v3d. Without it there is no picture."

	copy_group "the second stage firmware" "$MNT"/boot/bootcode.bin "$MNT"/boot/*.elf
	copy_group "the fixup files" "$MNT"/boot/fixup*.dat
	copy_group "the kernel" "$MNT/boot/vmlinuz-$FLAVOR"
	copy_group "the initramfs" "$INITRAMFS"
	copy_group "the device trees" "$DTBDIR"/bcm2*.dtb
	mkdir -p "$BOOTMNT/overlays"
	cp "$OVLDIR"/* "$BOOTMNT/overlays/" || die "cannot copy the overlays"
	[ -f "$BOOTMNT/overlays/vc4-kms-v3d.dtbo" ] ||
		die "PPBOOT has no overlays/vc4-kms-v3d.dtbo, which config.txt asks for."
	cp "$SRC/rpi/config.txt" "$BOOTMNT/config.txt"
	printf '%s\n' "$CMDLINE" >"$BOOTMNT/cmdline.txt"
	# config.txt names the kernel and the initramfs by flavour, the same drift
	# the x86_64 branch checks above.
	grep -q "vmlinuz-$FLAVOR" "$SRC/rpi/config.txt" ||
		die "os/rpi/config.txt does not name vmlinuz-$FLAVOR, but the kernel is $KVER."
	grep -q "initramfs-$FLAVOR" "$SRC/rpi/config.txt" ||
		die "os/rpi/config.txt does not name initramfs-$FLAVOR, but the kernel is $KVER."
	umount "$BOOTMNT"; rmdir "$BOOTMNT"; BOOTMNT=""
fi

# --------------------------------------------------------------- 9. the manifest
say "copy the package manifest next to the image"
cp "$MNT/usr/share/portapixel/packages.manifest" \
	"$(dirname "$OUT")/$(basename "$OUT" .img)-packages.manifest"

umount "$MNT"; rmdir "$MNT"; MNT=""
# An "if", not "[ ... ] && ...". With the fallback loop devices $LOOP is empty.
# The test then returns non-zero, and set -e would stop the build right here,
# just before the compress step.
if [ -n "$LOOP" ]; then
	losetup -d "$LOOP"
	LOOP=""
fi
for d in $EXTRA_LOOPS; do losetup -d "$d" 2>/dev/null || true; done
EXTRA_LOOPS=""

# ------------------------------------------------------------------ 10. compress
say "compress"
# -n leaves the name and the time out of the gzip header. Two builds of one
# release must give the same bytes (D50), and a time stamp in the header would
# make every build different.
gzip -9n "$OUT"
say "done: $OUT.gz"
ls -l "$OUT.gz"
