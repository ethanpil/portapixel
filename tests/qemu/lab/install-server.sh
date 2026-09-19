#!/bin/sh
# install-server.sh -- make the disk of pp-server and install Alpine on it.
# Run it one time. start-server.sh boots the result.
#
# Put the public keys that the VM must accept in seed/authorized_keys first.
set -eu
. "$(dirname "$0")/lab.env"

ISO="${PP_ISO:-/root/.cache/portapixel/alpine-virt-3.23.2-x86_64.iso}"
SEED="$LAB/seed"

[ -f "$ISO" ] || die "no Alpine ISO at $ISO; set PP_ISO"
mkdir -p "$SRV_DIR" "$SEED"
cp "$(dirname "$0")/install-server-vm.sh" "$SEED/install-server-vm.sh"
[ -f "$SEED/authorized_keys" ] ||
	die "put the public keys of the lab in $SEED/authorized_keys"
if running "$SRV_DIR/qemu.pid"; then
	die "$SRV_NAME runs; stop it first"
fi

rm -f "$SRV_DISK"
qemu-img create -f qcow2 "$SRV_DISK" 8G >/dev/null

# The tap must exist before QEMU opens it, because QEMU runs with script=no.
tap_up "$SRV_TAP"

exec expect "$(dirname "$0")/mkserver.exp" "$ISO" "$SRV_DISK" "$SEED" \
	"$SRV_TAP" "$SRV_MAC"
