#!/bin/sh
# throttle-zero.sh -- switch the CPU duty cycle of the pp-zero QEMU on or off.
# Usage: throttle-zero.sh on|off [RUN_US STOP_US]
#
# "on" starts dutythrottle on the QEMU process, with the times of zero.env or
# the two times given. "off" stops it and lets QEMU run at full speed.
set -eu
. "$(dirname "$0")/lab.env"
. "$(dirname "$0")/zero.env"

running "$ZERO_DIR/qemu.pid" || die "$ZERO_NAME does not run"
QPID="$(cat "$ZERO_DIR/qemu.pid")"

# Stop a throttle that runs. Send a signal only to a process that is our
# throttle: a PID file can be old, and its number can belong to another process.
if [ -f "$ZERO_DIR/throttle.pid" ]; then
	TPID="$(cat "$ZERO_DIR/throttle.pid")"
	if [ "$(cat "/proc/$TPID/comm" 2>/dev/null)" = dutythrottle ]; then
		kill "$TPID" 2>/dev/null || true
		sleep 1
	fi
	rm -f "$ZERO_DIR/throttle.pid"
fi
# The throttle sends SIGCONT when it stops. Send it again: a throttle that was
# killed with KILL leaves QEMU stopped.
kill -CONT "$QPID"

[ "${1:-on}" = on ] || { say "throttle off"; exit 0; }

RUN_US="${2:-$ZERO_RUN_US}"
STOP_US="${3:-$ZERO_STOP_US}"
[ -x "$ZERO_THROTTLE" ] || die "no $ZERO_THROTTLE. Build it with:
  CGO_ENABLED=0 go build -o $ZERO_THROTTLE ./tests/qemu/lab/dutythrottle"
setsid "$ZERO_THROTTLE" "$QPID" "$RUN_US" "$STOP_US" >/dev/null 2>&1 &
echo $! >"$ZERO_DIR/throttle.pid"
say "throttle on: run $RUN_US us, stop $STOP_US us (pid $(cat "$ZERO_DIR/throttle.pid"))"
