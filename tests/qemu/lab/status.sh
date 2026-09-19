#!/bin/sh
# status.sh -- show what the lab does: the VMs, their addresses and their APIs.
set -eu
. "$(dirname "$0")/lab.env"

printf 'bridge %s:\n' "$BRIDGE"
ip -br addr show "$BRIDGE" 2>/dev/null || printf '  the bridge is missing\n'
printf '  ports: %s\n' "$(ls /sys/class/net/"$BRIDGE"/brif 2>/dev/null | tr '\n' ' ')"

report() {
	_name="$1"
	_dir="$2"
	_mac="$3"
	_port="$4"
	printf '\n%s:\n' "$_name"
	if running "$_dir/qemu.pid"; then
		printf '  qemu pid : %s\n' "$(cat "$_dir/qemu.pid")"
		_rss="$(awk '/VmRSS/ { print $2 " kB" }' /proc/"$(cat "$_dir/qemu.pid")"/status 2>/dev/null)"
		printf '  qemu rss : %s\n' "${_rss:-unknown}"
	else
		printf '  qemu pid : not running\n'
		return 0
	fi
	_ip="$(vm_ip "$_mac")"
	printf '  address  : %s (%s)\n' "${_ip:-unknown}" "$_mac"
	if [ -n "$_ip" ]; then
		printf '  http     : '
		curl -fsS -m 5 -o /dev/null -w '%{http_code}\n' "http://$_ip:$_port/" 2>/dev/null ||
			printf 'no answer\n'
	fi
	printf '  serial   : %s\n' "$_dir/serial.log"
}

report "$SRV_NAME" "$SRV_DIR" "$SRV_MAC" 8080
report "$CLI_NAME" "$CLI_DIR" "$CLI_MAC" 80
