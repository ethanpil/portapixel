#!/bin/sh
# make-zero-disk.sh -- make the disk of pp-zero.
# Usage: make-zero-disk.sh [IMAGE.img.gz]
#
# With an image, the script first makes the base disk from it. Then it always
# makes a new overlay of 16 GB on the base, so each run of pp-zero can start
# from a clean first boot. The base itself never changes after this script.
set -eu
. "$(dirname "$0")/lab.env"
. "$(dirname "$0")/zero.env"

IMG="${1:-}"
if running "$ZERO_DIR/qemu.pid"; then
	die "$ZERO_NAME runs; stop it first"
fi
mkdir -p "$ZERO_DIR"

if [ -n "$IMG" ]; then
	[ -f "$IMG" ] || die "$IMG does not exist"
	say "unpack $IMG"
	case "$IMG" in
	*.gz) gzip -dc "$IMG" >"$ZERO_DIR/raw.img.part" ;;
	*) cp "$IMG" "$ZERO_DIR/raw.img.part" ;;
	esac
	say "convert to the qcow2 base"
	rm -f "$ZERO_BASE"
	qemu-img convert -f raw -O qcow2 "$ZERO_DIR/raw.img.part" "$ZERO_BASE"
	rm -f "$ZERO_DIR/raw.img.part"
fi
[ -f "$ZERO_BASE" ] || die "no base disk at $ZERO_BASE; give an image"

say "make a new 16 GB overlay"
rm -f "$ZERO_DISK"
qemu-img create -q -f qcow2 -b "$ZERO_BASE" -F qcow2 "$ZERO_DISK" 16G
qemu-img info "$ZERO_DISK" | sed -n '1,4p'
