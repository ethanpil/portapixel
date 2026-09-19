#!/bin/sh
# start-server.sh -- start the pp-server VM (the PortaPixel fleet server).
set -eu
. "$(dirname "$0")/lab.env"

[ -f "$SRV_DISK" ] || die "no disk at $SRV_DISK; run install-server.sh first"
if running "$SRV_DIR/qemu.pid"; then
	say "$SRV_NAME already runs (pid $(cat "$SRV_DIR/qemu.pid"))"
	exit 0
fi

mkdir -p "$SRV_DIR"
rm -f "$SRV_DIR/serial.sock" "$SRV_DIR/monitor.sock" "$SRV_DIR/qemu.pid"
tap_up "$SRV_TAP"

say "start $SRV_NAME (${SRV_MEM}M, $SRV_CPUS cpu, $SRV_MAC on $BRIDGE)"
qemu-system-x86_64 -accel kvm -cpu host -smp "$SRV_CPUS" -m "$SRV_MEM" \
	-drive "file=$SRV_DISK,format=qcow2,if=virtio" \
	-netdev "tap,id=n0,ifname=$SRV_TAP,script=no,downscript=no" \
	-device "virtio-net-pci,netdev=n0,mac=$SRV_MAC" \
	-display none \
	-chardev "socket,id=ser0,path=$SRV_DIR/serial.sock,server=on,wait=off" \
	-serial chardev:ser0 \
	-monitor "unix:$SRV_DIR/monitor.sock,server,nowait" \
	-pidfile "$SRV_DIR/qemu.pid" \
	-daemonize || die "QEMU did not start"

serial_log "$SRV_DIR/serial.sock" "$SRV_DIR/serial.log"
say "serial log: $SRV_DIR/serial.log"
