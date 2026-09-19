#!/bin/sh
# tests/qemu/boot-smoke.sh -- boot a PortaPixel image in QEMU and check it.
#
# It proves four things:
#   1. the image boots to a login prompt (the boot chain works),
#   2. the daemon answers on port 80 through a host forwarded port,
#   3. /api/status returns something that looks like our status,
#   4. /api/status reports browser_state "running", so the cage session came up.
#
# It does NOT prove that a picture is on the screen. Use tests/qemu/boot-dev.sh
# for that: it keeps the guest up and takes a screenshot of the display.
#
# SeaBIOS is the default firmware. --uefi selects OVMF instead. Both must pass,
# because the image carries both boot paths (plan section 6).
#
# KVM is used when /dev/kvm is there. Without it QEMU falls back to TCG, which
# measures five to eight times slower, so every timeout scales by six
# (mountnas flakiness policy: scale the deadline, never tune one test).
set -eu

UEFI=0
PORT=18080
MEM=3072
IMAGE=""

die() { printf 'boot-smoke: %s\n' "$*" >&2; exit 1; }
say() { printf '==> %s\n' "$*"; }

usage() {
	cat <<'EOF'
Usage: boot-smoke.sh [--uefi] [--port N] [--mem MB] IMAGE(.img or .img.gz)
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--uefi) UEFI=1; shift ;;
	--port) PORT="${2:?}"; shift 2 ;;
	--mem) MEM="${2:?}"; shift 2 ;;
	-h|--help) usage; exit 0 ;;
	-*) usage >&2; die "unknown option: $1" ;;
	*) IMAGE="$1"; shift ;;
	esac
done
[ -n "$IMAGE" ] || { usage >&2; exit 1; }
[ -f "$IMAGE" ] || die "$IMAGE does not exist"

for t in qemu-system-x86_64 qemu-img expect curl; do
	command -v "$t" >/dev/null 2>&1 || die "$t is missing"
done

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT INT TERM

# A smoke test must never change the artefact it tests, and it must not need a
# second copy of a multi gigabyte image either: a qcow2 overlay keeps every write
# in a small file of its own. First boot writes very little.
RAW="$IMAGE"
case "$IMAGE" in
*.gz)
	say "unpack $IMAGE"
	gzip -dc "$IMAGE" >"$WORK/raw.img"
	RAW="$WORK/raw.img"
	;;
esac
case "$RAW" in /*) ;; *) RAW="$PWD/$RAW" ;; esac
qemu-img create -q -f qcow2 -b "$RAW" -F raw "$WORK/disk.qcow2" >/dev/null ||
	die "cannot make the overlay for $RAW"

ACCEL=""
SCALE=6
if [ -e /dev/kvm ] && [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
	ACCEL="-accel kvm -cpu host"
	SCALE=1
	say "KVM is available"
else
	say "no usable /dev/kvm: TCG, with timeouts scaled by $SCALE"
fi

BIOS=""
FW=bios
if [ "$UEFI" = 1 ]; then
	# Only COMBINED firmware images work with -bios. OVMF_CODE.fd is the split
	# build and needs a separate variable store through -drive if=pflash, so it
	# must not be in this list.
	for c in /usr/share/ovmf/bios.bin /usr/share/OVMF/OVMF.fd \
		/usr/share/ovmf/OVMF.fd /usr/share/edk2/x64/OVMF.4m.fd; do
		[ -f "$c" ] && { BIOS="-bios $c"; break; }
	done
	[ -n "$BIOS" ] || die "--uefi needs OVMF; install the ovmf package"
	FW=uefi
fi

# cage needs a real DRM device, so the guest needs a graphics card that Linux has
# a KMS driver for. Alpine keeps the QEMU display devices in packages of their
# own, so virtio-vga is often missing. Install it with
#   apk add qemu-hw-display-virtio-vga
# bochs-display is the fallback, because bochs_drm is a KMS driver too.
GPU=""
DEVS="$(qemu-system-x86_64 -device help 2>&1 || true)"
for d in virtio-vga bochs-display; do
	if printf '%s' "$DEVS" | grep -q "\"$d\""; then
		GPU="-device $d"
		break
	fi
done
if [ -n "$GPU" ]; then
	say "graphics: $GPU"
else
	say "WARNING: this QEMU has no KMS graphics device, so cage cannot start and"
	say "         the browser will not come up. apk add qemu-hw-display-virtio-vga"
fi

say "boot $FW, serial expect, host port $PORT -> guest 80"
# The disk is virtio on purpose: the cmdline module list carries virtio_blk.
# Attach it on a bus that the initramfs cannot probe and the test fails for the
# wrong reason (mountnas lesson).
#
# "expect", not "exec expect". An exec REPLACES this shell, so the EXIT trap
# above never runs and $WORK stays behind. With a .img.gz input that directory
# holds the unpacked image, so each run leaked four gigabytes. Measured: four
# work directories left on the test box, two of them 4.0 GB.
rc=0
expect "$(dirname "$0")/boot-smoke.exp" \
	"$WORK/disk.qcow2" "$PORT" "$MEM" "$SCALE" "$ACCEL" "$BIOS" "$GPU" || rc=$?
exit "$rc"
