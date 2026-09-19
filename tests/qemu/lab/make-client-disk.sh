#!/bin/sh
# make-client-disk.sh -- make the 16 GB disk of pp-client from a PortaPixel image.
# Usage: make-client-disk.sh IMAGE.img.gz
#
# The disk is 16 GB, so the first boot grows PPMEDIA for real. The disk keeps
# every write: pp-client is a long-lived machine, not a test run.
set -eu
. "$(dirname "$0")/lab.env"

IMG="${1:?usage: make-client-disk.sh IMAGE.img.gz}"
[ -f "$IMG" ] || die "$IMG does not exist"
if running "$CLI_DIR/qemu.pid"; then
	die "$CLI_NAME runs; stop it first"
fi

mkdir -p "$CLI_DIR"
say "unpack $IMG"
case "$IMG" in
*.gz)
	gzip -dc "$IMG" >"$CLI_DIR/raw.img.part"
	mv "$CLI_DIR/raw.img.part" "$CLI_DIR/raw.img"
	;;
*) cp "$IMG" "$CLI_DIR/raw.img" ;;
esac
say "convert to qcow2 and grow to 16 GB"
rm -f "$CLI_DISK"
qemu-img convert -f raw -O qcow2 "$CLI_DIR/raw.img" "$CLI_DISK"
qemu-img resize "$CLI_DISK" 16G
rm -f "$CLI_DIR/raw.img"
qemu-img info "$CLI_DISK" | sed -n '1,6p'
