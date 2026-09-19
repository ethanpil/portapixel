#!/bin/sh
# stop.sh -- stop one lab VM or both. It stops no other QEMU process.
# Usage: stop.sh [server|client|all]
set -eu
. "$(dirname "$0")/lab.env"

stop_one() {
	_name="$1"
	_dir="$2"
	if ! running "$_dir/qemu.pid"; then
		say "$_name does not run"
		rm -f "$_dir/qemu.pid"
		return 0
	fi
	_pid="$(cat "$_dir/qemu.pid")"
	say "stop $_name (pid $_pid)"
	# system_powerdown first: the guest then writes its file systems out.
	mon "$_dir/monitor.sock" system_powerdown >/dev/null 2>&1 || true
	_i=0
	while [ "$_i" -lt 30 ]; do
		kill -0 "$_pid" 2>/dev/null || break
		sleep 1
		_i=$((_i + 1))
	done
	if kill -0 "$_pid" 2>/dev/null; then
		say "$_name did not stop in 30 s; quit the monitor"
		mon "$_dir/monitor.sock" quit >/dev/null 2>&1 || true
		sleep 2
		kill -0 "$_pid" 2>/dev/null && kill "$_pid" 2>/dev/null || true
	fi
	rm -f "$_dir/qemu.pid" "$_dir/monitor.sock" "$_dir/serial.sock"
}

case "${1:-all}" in
server) stop_one "$SRV_NAME" "$SRV_DIR" ;;
client) stop_one "$CLI_NAME" "$CLI_DIR" ;;
all)    stop_one "$SRV_NAME" "$SRV_DIR"; stop_one "$CLI_NAME" "$CLI_DIR" ;;
*)      die "usage: stop.sh [server|client|all]" ;;
esac
