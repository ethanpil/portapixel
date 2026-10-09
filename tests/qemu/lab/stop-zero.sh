#!/bin/sh
# stop-zero.sh -- stop pp-zero and its CPU throttle. It stops no other process.
set -eu
. "$(dirname "$0")/lab.env"
. "$(dirname "$0")/zero.env"

if ! running "$ZERO_DIR/qemu.pid"; then
	say "$ZERO_NAME does not run"
	rm -f "$ZERO_DIR/qemu.pid" "$ZERO_DIR/throttle.pid"
	exit 0
fi
PID="$(cat "$ZERO_DIR/qemu.pid")"
# The throttle first: a stopped QEMU cannot shut its guest down.
sh "$(dirname "$0")/throttle-zero.sh" off >/dev/null 2>&1 || kill -CONT "$PID" 2>/dev/null || true

say "stop $ZERO_NAME (pid $PID)"
# system_powerdown first: acpid in the image shuts the guest down cleanly.
mon "$ZERO_DIR/monitor.sock" system_powerdown >/dev/null 2>&1 || true
i=0
while [ "$i" -lt 40 ]; do
	kill -0 "$PID" 2>/dev/null || break
	sleep 1
	i=$((i + 1))
done
if kill -0 "$PID" 2>/dev/null; then
	say "$ZERO_NAME did not stop in 40 s; quit the monitor"
	mon "$ZERO_DIR/monitor.sock" quit >/dev/null 2>&1 || true
	sleep 2
	kill -0 "$PID" 2>/dev/null && kill "$PID" 2>/dev/null || true
fi
rm -f "$ZERO_DIR/qemu.pid" "$ZERO_DIR/monitor.sock" "$ZERO_DIR/serial.sock"
say "$ZERO_NAME stopped"
