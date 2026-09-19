#!/bin/sh
# start-client.sh -- start the pp-client VM (a signage device).
set -eu
. "$(dirname "$0")/lab.env"

[ -f "$CLI_DISK" ] || die "no disk at $CLI_DISK; run make-client-disk.sh first"
if running "$CLI_DIR/qemu.pid"; then
	say "$CLI_NAME already runs (pid $(cat "$CLI_DIR/qemu.pid"))"
	exit 0
fi

# virtio-vga, not the default card: the guest needs a DRM device, or cage cannot
# start and the screen stays black.
qemu-system-x86_64 -device help 2>&1 | grep -q '"virtio-vga"' ||
	die "this QEMU has no virtio-vga; apk add qemu-hw-display-virtio-vga"

mkdir -p "$CLI_DIR"
rm -f "$CLI_DIR/serial.sock" "$CLI_DIR/monitor.sock" "$CLI_DIR/qemu.pid"
tap_up "$CLI_TAP"

say "start $CLI_NAME (${CLI_MEM}M, $CLI_CPUS cpus, $CLI_MAC on $BRIDGE, vnc $CLI_VNC)"
qemu-system-x86_64 -accel kvm -cpu host -smp "$CLI_CPUS" -m "$CLI_MEM" \
	-device virtio-vga \
	-drive "file=$CLI_DISK,format=qcow2,if=virtio" \
	-netdev "tap,id=n0,ifname=$CLI_TAP,script=no,downscript=no" \
	-device "virtio-net-pci,netdev=n0,mac=$CLI_MAC" \
	-vnc "$CLI_VNC" \
	-chardev "socket,id=ser0,path=$CLI_DIR/serial.sock,server=on,wait=off" \
	-serial chardev:ser0 \
	-monitor "unix:$CLI_DIR/monitor.sock,server,nowait" \
	-pidfile "$CLI_DIR/qemu.pid" \
	-daemonize || die "QEMU did not start"

serial_log "$CLI_DIR/serial.sock" "$CLI_DIR/serial.log"
say "serial log: $CLI_DIR/serial.log"
say "see the screen with an SSH tunnel: ssh -L 5901:127.0.0.1:5901 root@<this host>"
