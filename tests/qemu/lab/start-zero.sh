#!/bin/sh
# start-zero.sh -- start pp-zero, the Raspberry Pi Zero 2 W proxy VM.
# Usage: start-zero.sh [throttle|nothrottle] [RA_KB|stock]
#
#   throttle    512 MB, 4 CPUs, an SD card disk speed and the CPU duty cycle of
#               zero.env. This is the full proxy, and the default.
#   nothrottle  the same with the CPUs at full speed.
#   RA_KB       the read-ahead of the disk in the guest, in KB. The default is
#               128, the usual value of an SD card (mmcblk). Kernel 6.18 sets
#               the read-ahead to twice the optimal I/O size of the device, or to
#               8192 KB when virtio gives none. "stock" gives no value.
#
# make-zero-disk.sh makes the disk first.
set -eu
. "$(dirname "$0")/lab.env"
. "$(dirname "$0")/zero.env"

MODE="${1:-throttle}"
case "$MODE" in throttle|nothrottle) ;; *) die "usage: start-zero.sh [throttle|nothrottle] [RA_KB|stock]" ;; esac
RA="${2:-128}"
OPT=""
[ "$RA" = stock ] || OPT=",opt_io_size=$((RA * 512))"

[ -f "$ZERO_DISK" ] || die "no disk at $ZERO_DISK; run make-zero-disk.sh first"
if running "$ZERO_DIR/qemu.pid"; then
	say "$ZERO_NAME already runs (pid $(cat "$ZERO_DIR/qemu.pid"))"
	exit 0
fi
# virtio-vga, not the default card: the guest needs a DRM device, or mpv cannot
# start and the screen stays black.
qemu-system-x86_64 -device help 2>&1 | grep -q '"virtio-vga"' ||
	die "this QEMU has no virtio-vga; apk add qemu-hw-display-virtio-vga"

rm -f "$ZERO_DIR/serial.sock" "$ZERO_DIR/monitor.sock" "$ZERO_DIR/qemu.pid" "$ZERO_DIR/throttle.pid"
tap_up "$ZERO_TAP"
# About 30 Mbit/s towards the guest, like 2.4 GHz WiFi. On this tap only, never
# on the bridge.
tc qdisc replace dev "$ZERO_TAP" root tbf rate 30mbit burst 64kb latency 100ms 2>/dev/null ||
	say "no tc shaper on $ZERO_TAP; the network runs at full speed"

say "start $ZERO_NAME (${ZERO_MEM}M, $ZERO_CPUS cpus, $ZERO_MAC on $BRIDGE, vnc $ZERO_VNC, $MODE)"
qemu-system-x86_64 -accel kvm -cpu host -smp "$ZERO_CPUS" -m "$ZERO_MEM" \
	-device virtio-vga \
	-drive "file=$ZERO_DISK,format=qcow2,if=none,id=zd0,throttling.bps-read=$ZERO_BPS_RD,throttling.bps-write=$ZERO_BPS_WR,throttling.iops-read=$ZERO_IOPS_RD,throttling.iops-write=$ZERO_IOPS_WR" \
	-device "virtio-blk-pci,drive=zd0$OPT" \
	-netdev "tap,id=n0,ifname=$ZERO_TAP,script=no,downscript=no" \
	-device "virtio-net-pci,netdev=n0,mac=$ZERO_MAC" \
	-vnc "$ZERO_VNC" \
	-chardev "socket,id=ser0,path=$ZERO_DIR/serial.sock,server=on,wait=off" \
	-serial chardev:ser0 \
	-monitor "unix:$ZERO_DIR/monitor.sock,server,nowait" \
	-pidfile "$ZERO_DIR/qemu.pid" \
	-daemonize || die "QEMU did not start"
serial_log "$ZERO_DIR/serial.sock" "$ZERO_DIR/serial.log"

if [ "$MODE" = throttle ]; then
	sh "$(dirname "$0")/throttle-zero.sh" on
fi
say "serial log: $ZERO_DIR/serial.log"
say "see the screen with an SSH tunnel: ssh -L 5902:127.0.0.1:5902 root@<this host>"
