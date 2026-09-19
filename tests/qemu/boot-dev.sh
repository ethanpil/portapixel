#!/bin/sh
# tests/qemu/boot-dev.sh -- boot a PortaPixel image and keep it running.
#
# boot-smoke.sh is the pass/fail test. This script is the opposite: it starts the
# same image, leaves it up, and gives you the four things that a display problem
# needs:
#
#   * a screenshot of the virtual display   (the only proof that a picture came)
#   * the serial log in a file
#   * port 80 and port 22 of the guest on the host loopback
#   * the QEMU monitor on a socket
#
# The graphics device is virtio-vga, so the guest has a real DRM device and cage
# can take DRM master. Alpine keeps that device in the package
# qemu-hw-display-virtio-vga.
#
# Usage:
#   boot-dev.sh start IMAGE(.img or .img.gz) [--uefi] [--mem MB] [--cpus N]
#                                            [--http PORT] [--ssh PORT]
#                                            [--netdump FILE.pcap]
#   boot-dev.sh shot FILE.png    write a screenshot of the display
#   boot-dev.sh mon "COMMAND"    send one QEMU monitor command
#   boot-dev.sh log [N]          show the last N lines of the serial log
#   boot-dev.sh stop             stop the guest
#
# PP_VM_DIR says where the run state goes. The default is ./ppvm.
set -eu

WORK="${PP_VM_DIR:-$PWD/ppvm}"
DISK="$WORK/disk.qcow2"
SERIAL_SOCK="$WORK/serial.sock"
SERIAL_LOG="$WORK/serial.log"
MON_SOCK="$WORK/monitor.sock"
PIDFILE="$WORK/qemu.pid"

die() { printf 'boot-dev: %s\n' "$*" >&2; exit 1; }
say() { printf '==> %s\n' "$*"; }

# ------------------------------------------------------------------ the monitor
# One monitor command. socat closes the connection after -T seconds of silence,
# which is how we know the answer is complete: the monitor never says "done".
mon() {
	[ -S "$MON_SOCK" ] || die "no monitor socket; is the guest running?"
	printf '%s\n' "$*" | socat -T 3 - "UNIX-CONNECT:$MON_SOCK" 2>/dev/null || true
}

cmd_start() {
	IMAGE=""
	UEFI=0
	MEM=2048
	CPUS=2
	HTTP_PORT=18080
	SSH_PORT=18022
	NETDUMP=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--uefi) UEFI=1; shift ;;
		--mem) MEM="${2:?}"; shift 2 ;;
		--cpus) CPUS="${2:?}"; shift 2 ;;
		--http) HTTP_PORT="${2:?}"; shift 2 ;;
		--ssh) SSH_PORT="${2:?}"; shift 2 ;;
		--netdump) NETDUMP="${2:?}"; shift 2 ;;
		-*) die "unknown option: $1" ;;
		*) IMAGE="$1"; shift ;;
		esac
	done
	[ -n "$IMAGE" ] || die "start needs an image"
	[ -f "$IMAGE" ] || die "$IMAGE does not exist"

	for t in qemu-system-x86_64 qemu-img socat; do
		command -v "$t" >/dev/null 2>&1 || die "$t is missing"
	done
	[ -e /dev/kvm ] || die "no /dev/kvm: the guest under TCG is too slow for a display test"
	[ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null &&
		die "a guest already runs; stop it first"

	mkdir -p "$WORK"
	rm -f "$SERIAL_LOG" "$SERIAL_SOCK" "$MON_SOCK" "$PIDFILE"

	# The image itself is never written to. A qcow2 overlay keeps every write of
	# the guest in a small file, so the same image can be booted again from the
	# start after a test changes it.
	RAW="$IMAGE"
	case "$IMAGE" in
	*.gz)
		RAW="$WORK/raw.img"
		if [ ! -f "$RAW" ] || [ "$IMAGE" -nt "$RAW" ]; then
			say "unpack $IMAGE"
			gzip -dc "$IMAGE" >"$RAW.part"
			mv "$RAW.part" "$RAW"
		fi
		;;
	esac
	case "$RAW" in /*) ;; *) RAW="$PWD/$RAW" ;; esac
	qemu-img create -q -f qcow2 -b "$RAW" -F raw "$DISK" >/dev/null ||
		die "cannot make the overlay for $RAW"

	BIOS=""
	if [ "$UEFI" = 1 ]; then
		# A COMBINED firmware file only. OVMF_CODE.fd is the split build and
		# needs its own variable store through -drive if=pflash.
		for c in /usr/share/OVMF/OVMF.fd /usr/share/ovmf/OVMF.fd \
			/usr/share/edk2/x64/OVMF.4m.fd /usr/share/ovmf/bios.bin; do
			[ -f "$c" ] && { BIOS="-bios $c"; break; }
		done
		[ -n "$BIOS" ] || die "--uefi needs OVMF; apk add ovmf"
	fi

	# filter-dump writes every frame of the guest network to a pcap file on the
	# host. It is the proof that the device talks to nobody: the file holds the
	# name in each DNS question and in each TLS hello, so a plain grep finds a
	# host that the browser called. The guest needs no tool at all for this.
	DUMP=""
	if [ -n "$NETDUMP" ]; then
		case "$NETDUMP" in /*) ;; *) NETDUMP="$PWD/$NETDUMP" ;; esac
		rm -f "$NETDUMP"
		DUMP="-object filter-dump,id=netdump,netdev=n0,file=$NETDUMP"
	fi

	# virtio-vga, not the default VGA card: the default has no KMS driver in
	# Linux, so /dev/dri stays empty and cage cannot start.
	qemu-system-x86_64 -device help 2>&1 | grep -q '"virtio-vga"' ||
		die "this QEMU has no virtio-vga; apk add qemu-hw-display-virtio-vga"

	say "start the guest (${MEM}M, $CPUS cpus, http $HTTP_PORT, ssh $SSH_PORT)"
	# shellcheck disable=SC2086  # $BIOS and $DUMP are deliberate word lists
	qemu-system-x86_64 -accel kvm -cpu host -smp "$CPUS" -m "$MEM" $BIOS $DUMP \
		-device virtio-vga \
		-display none \
		-drive "file=$DISK,format=qcow2,if=virtio" \
		-netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$HTTP_PORT-:80,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22" \
		-device virtio-net-pci,netdev=n0 \
		-chardev "socket,id=ser0,path=$SERIAL_SOCK,server=on,wait=off" \
		-serial chardev:ser0 \
		-monitor "unix:$MON_SOCK,server,nowait" \
		-pidfile "$PIDFILE" \
		-daemonize ||
		die "QEMU did not start"

	# The serial console goes to a socket, not straight to a file, so that a
	# person can also attach to it:
	#   socat -,raw,echo=0 UNIX-CONNECT:$SERIAL_SOCK
	# Only one reader at a time. Stop this logger first.
	setsid socat -u "UNIX-CONNECT:$SERIAL_SOCK" "OPEN:$SERIAL_LOG,creat,trunc" \
		>/dev/null 2>&1 &

	cat <<EOF
    serial log : $SERIAL_LOG
    monitor    : $MON_SOCK
    web        : http://127.0.0.1:$HTTP_PORT/
    ssh        : ssh -p $SSH_PORT root@127.0.0.1
    screenshot : $0 shot FILE.png
    stop       : $0 stop
EOF
	[ -z "$NETDUMP" ] || printf '    network    : %s\n' "$NETDUMP"
}

cmd_shot() {
	OUT="${1:?shot needs an output file}"
	case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac
	PPM="$WORK/shot.ppm"
	rm -f "$PPM"
	# screendump writes from the working directory of QEMU, so the path must be
	# absolute. PPM, not PNG: not every QEMU build has the PNG writer.
	mon "screendump $PPM" >/dev/null
	i=0
	while [ "$i" -lt 10 ]; do
		[ -s "$PPM" ] && break
		sleep 1
		i=$((i + 1))
	done
	[ -s "$PPM" ] || die "the monitor wrote no screenshot"
	case "$OUT" in
	*.png)
		command -v pnmtopng >/dev/null 2>&1 || die "pnmtopng is missing; apk add netpbm"
		pnmtopng "$PPM" >"$OUT" 2>/dev/null || die "cannot convert the screenshot"
		;;
	*) cp "$PPM" "$OUT" ;;
	esac
	say "$OUT"
	ls -l "$OUT"
}

cmd_stop() {
	if [ -f "$PIDFILE" ]; then
		PID="$(cat "$PIDFILE")"
		mon "quit" >/dev/null 2>&1 || true
		i=0
		while [ "$i" -lt 10 ]; do
			kill -0 "$PID" 2>/dev/null || break
			sleep 1
			i=$((i + 1))
		done
		kill -0 "$PID" 2>/dev/null && kill -9 "$PID" 2>/dev/null || true
		rm -f "$PIDFILE"
	fi
	rm -f "$MON_SOCK" "$SERIAL_SOCK"
	say "the guest is stopped"
}

[ $# -gt 0 ] || { sed -n '3,25p' "$0"; exit 1; }
SUB="$1"; shift
case "$SUB" in
start) cmd_start "$@" ;;
shot)  cmd_shot "$@" ;;
mon)   mon "$@" ;;
log)   tail -n "${1:-80}" "$SERIAL_LOG" ;;
stop)  cmd_stop ;;
*)     die "unknown command: $SUB" ;;
esac
