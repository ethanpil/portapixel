#!/bin/sh
# tests/ci/smoke-arm.sh -- the reduced gate for the aarch64 image.
#
# QEMU cannot boot the Raspberry Pi firmware boot path, so this test does not
# boot the image (plan section 17 item 5). It mounts the two partitions of the
# image and proves what a boot needs:
#
#   1. the daemon binary in the image starts and passes "selftest" under
#      qemu-user, so the embedded web assets and the templates are there;
#   2. "portapixeld version" prints the release that CI built;
#   3. the packages of the display stack are in the manifest (D50);
#   4. the overlay files and the release layout are in place;
#   5. PPBOOT holds every file that the Pi firmware needs.
#
# Run it as root. It needs losetup, mount and qemu-user binfmt for aarch64.
#
# Usage: smoke-arm.sh IMAGE.img.gz VERSION
set -eu

IMAGE="${1:?the aarch64 image}"
VERSION="${2:?the release version}"

say() { printf '==> %s\n' "$*"; }
die() { printf 'smoke-arm: FAIL: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run this script as root"
[ -f "$IMAGE" ] || die "$IMAGE does not exist"

WORK="$(mktemp -d)"
LOOP=""
cleanup() {
	set +e
	umount "$WORK/root/proc" 2>/dev/null
	umount "$WORK/root" 2>/dev/null || umount -l "$WORK/root" 2>/dev/null
	umount "$WORK/boot" 2>/dev/null || umount -l "$WORK/boot" 2>/dev/null
	[ -n "$LOOP" ] && losetup -d "$LOOP" 2>/dev/null
	rm -rf "$WORK"
	return 0
}
trap cleanup EXIT INT TERM

RAW="$WORK/image.img"
case "$IMAGE" in
*.gz) say "unpack $IMAGE"; gzip -dc "$IMAGE" >"$RAW" ;;
*) cp "$IMAGE" "$RAW" ;;
esac

say "attach the image"
LOOP="$(losetup -P -f --show "$RAW")" || die "cannot attach $RAW"
for i in 1 2 3 4 5 6 7 8 9 10; do
	[ -b "${LOOP}p1" ] && [ -b "${LOOP}p2" ] && break
	sleep 1
done
[ -b "${LOOP}p2" ] || die "no partition nodes for $LOOP"

mkdir -p "$WORK/root" "$WORK/boot"
mount "${LOOP}p2" "$WORK/root" || die "cannot mount PPROOT"
mount "${LOOP}p1" "$WORK/boot" || die "cannot mount PPBOOT"
R="$WORK/root"

# ------------------------------------------------------- 1. the release identity
say "the release identity"
[ -f "$R/etc/portapixel-release" ] || die "no /etc/portapixel-release"
cat "$R/etc/portapixel-release"
grep -qx "PORTAPIXEL_VERSION=$VERSION" "$R/etc/portapixel-release" ||
	die "/etc/portapixel-release does not name the version $VERSION"
grep -qx "ARCH=aarch64" "$R/etc/portapixel-release" || die "the image does not say ARCH=aarch64"

# ---------------------------------------------------------- 2. the release layout
say "the release layout"
[ -L "$R/opt/portapixel/current" ] || die "no /opt/portapixel/current link"
say "current -> $(readlink "$R/opt/portapixel/current")"
[ -x "$R/opt/portapixel/current/portapixeld" ] || die "no daemon under current"
[ -d "$R/opt/portapixel/health" ] || die "no health directory"

# ---------------------------------------------------------------- 3. the overlay
say "the overlay files"
for f in \
	etc/init.d/portapixeld \
	etc/init.d/portapixel-firstboot \
	etc/init.d/portapixel-net \
	etc/conf.d/portapixeld \
	usr/libexec/portapixel/health-gate.sh \
	usr/libexec/portapixel/firstboot.sh \
	usr/libexec/portapixel/oplog.sh \
	usr/share/icons/default/index.theme \
	usr/share/portapixel/packages.manifest \
	etc/fstab \
	etc/inittab; do
	[ -f "$R/$f" ] || die "the image has no /$f"
done
for f in etc/init.d/portapixeld usr/libexec/portapixel/health-gate.sh; do
	[ -x "$R/$f" ] || die "/$f is not executable"
done
grep -q 'LABEL=PPROOT' "$R/etc/fstab" || die "the fstab does not find the root by label (D53)"
[ -L "$R/etc/runlevels/boot/swclock" ] ||
	die "swclock is not enabled; a Pi has no real time clock (D40)"
[ -L "$R/etc/runlevels/default/portapixeld" ] || die "portapixeld is not enabled"

# --------------------------------------------------------------- 4. the packages
say "the packages of the display stack"
MANIFEST="$R/usr/share/portapixel/packages.manifest"
[ -s "$MANIFEST" ] || die "the package manifest is empty (D50)"
say "the manifest holds $(wc -l <"$MANIFEST" | tr -d ' ') packages"
for p in chromium cage seatd wlr-randr alpine-base openrc busybox mesa-dri-gallium \
	linux-rpi raspberrypi-bootloader chrony openssh-server exfatprogs; do
	grep -q "^$p-[0-9]" "$MANIFEST" || die "the image has no $p package"
	printf '    ok: %s\n' "$p"
done

# ------------------------------------------------------------- 5. the boot files
say "the files on PPBOOT"
B="$WORK/boot"
ls -l "$B"
for f in config.txt cmdline.txt; do
	[ -f "$B/$f" ] || die "PPBOOT has no $f"
done
count() { find "$B" -maxdepth 1 -name "$1" | grep -c . || true; }
[ "$(count 'start*.elf')" -gt 0 ] || die "PPBOOT has no start*.elf firmware"
[ "$(count 'fixup*.dat')" -gt 0 ] || die "PPBOOT has no fixup*.dat"
[ "$(count 'bcm2*.dtb')" -gt 0 ] || die "PPBOOT has no bcm2*.dtb device tree"
[ -f "$B/overlays/vc4-kms-v3d.dtbo" ] || die "PPBOOT has no overlays/vc4-kms-v3d.dtbo"
[ "$(count 'vmlinuz-*')" -gt 0 ] || die "PPBOOT has no kernel"
[ "$(count 'initramfs-*')" -gt 0 ] || die "PPBOOT has no initramfs"
say "device trees: $(count 'bcm2*.dtb'), overlays: $(find "$B/overlays" -type f | grep -c . || true)"

# -------------------------------------------------- 6. the binary under qemu-user
# A chroot, so the binary runs with the libraries of the image. binfmt with the
# "fix binary" flag finds the interpreter from outside the chroot, so no copy of
# qemu goes into the image.
say "run the daemon of the image under qemu-user"
mkdir -p "$R/proc"
mount -t proc proc "$R/proc" 2>/dev/null || say "note: no /proc in the chroot"

out="$(chroot "$R" /opt/portapixel/current/portapixeld version 2>&1)" ||
	die "the aarch64 daemon does not run under qemu-user:
$out"
say "version: $out"
printf '%s' "$out" | grep -q "portapixeld $VERSION aarch64" ||
	die "the binary says \"$out\" and CI built $VERSION for aarch64"

out="$(chroot "$R" /opt/portapixel/current/portapixeld selftest 2>&1)" || {
	printf '%s\n' "$out"
	die "portapixeld selftest failed in the aarch64 image"
}
printf '%s\n' "$out"
if printf '%s' "$out" | grep -q '^FAIL'; then
	die "portapixeld selftest reported a fault in the aarch64 image"
fi

say "PASS: the aarch64 image holds a daemon that runs, and every boot file"
