#!/bin/sh
# netup.sh -- put eth0 into the bridge and move the host address onto it.
#
# A change of the network over SSH can cut the connection that makes it. So the
# script has a dead-man timer: it applies the change, waits 90 s, and puts the
# old state back if the file $LAB/net-ok does not exist. You make that file over
# a NEW SSH connection, which proves that the access survived.
#
#   nohup setsid /root/ppwork/lab/netup.sh >/dev/null 2>&1 &
#   ssh root@HOST touch /root/ppwork/lab/net-ok
#
# Record the state first: ip -d addr, ip route, /etc/network/interfaces.
. "$(dirname "$0")/lab.env"

exec >>"$LAB/netup.log" 2>&1
set -x
date
rm -f "$LAB/net-ok"

rollback() {
	date
	echo "ROLLBACK"
	ip route del default via "$HOST_GW" dev "$BRIDGE" 2>/dev/null
	ip addr flush dev "$BRIDGE" 2>/dev/null
	ip link set eth0 nomaster 2>/dev/null
	ip link set "$BRIDGE" down 2>/dev/null
	ip link del "$BRIDGE" 2>/dev/null
	ip addr add "$HOST_ADDR" broadcast "$HOST_BCAST" dev eth0 2>/dev/null
	ip link set eth0 up
	ip route add default via "$HOST_GW" dev eth0 metric 202 2>/dev/null
	ip addr show eth0
	ip route
	ping -c 2 -W 2 "$HOST_GW"
	echo "ROLLBACK DONE"
}

ip link add name "$BRIDGE" type bridge || { echo "no bridge support"; exit 1; }
ip link set "$BRIDGE" address "$HOST_MAC"
ip link set "$BRIDGE" up
ip addr flush dev eth0
ip link set eth0 master "$BRIDGE" || { echo "cannot enslave eth0"; rollback; exit 1; }
ip addr add "$HOST_ADDR" broadcast "$HOST_BCAST" dev "$BRIDGE"
ip route add default via "$HOST_GW" dev "$BRIDGE" metric 202
sleep 3
ip -d addr show "$BRIDGE"
ip route
ping -c 3 -W 2 "$HOST_GW" || echo "PING FAILED"

i=0
while [ "$i" -lt 90 ]; do
	if [ -f "$LAB/net-ok" ]; then
		echo "net-ok seen after ${i}s: keep the bridge"
		exit 0
	fi
	sleep 1
	i=$((i + 1))
done
echo "no net-ok after 90 s"
rollback
exit 1
