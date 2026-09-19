#!/bin/sh
# Update health gate and rollback (plan section 15).
#
# The updater stages a release, flips the "current" link and leaves a pending
# marker. This script gives the new daemon PP_HEALTH_TIMEOUT seconds to write
#   $PP_RELEASES/health/<version>.ok
# When the marker does not appear, it puts "current" back to "previous", marks
# the release bad so it is never tried again, and restarts the service.
#
# With no pending marker the script exits at once. It runs on every start of
# portapixeld, so it must stay cheap.
set -u

. /usr/libexec/portapixel/oplog.sh

PENDING="$PP_RELEASES/.swap-pending"     # holds the version that must prove itself
HEALTH="$PP_RELEASES/health"
FAILS="$HEALTH/gate-failures"            # one line per failed gate: epoch seconds
TIMEOUT="${PP_HEALTH_TIMEOUT:-120}"

[ -f "$PENDING" ] || exit 0
VER="$(cat "$PENDING" 2>/dev/null)"
[ -n "$VER" ] || { rm -f "$PENDING"; exit 0; }

oplog update.gate.start "version=$VER timeout=${TIMEOUT}s"

waited=0
while [ "$waited" -lt "$TIMEOUT" ]; do
	if [ -f "$HEALTH/$VER.ok" ]; then
		rm -f "$PENDING"
		oplog update.gate.pass "version=$VER after=${waited}s"
		exit 0
	fi
	sleep 2
	waited=$((waited + 2))
done

# ------------------------------------------------------------------ the rollback
oplog update.gate.fail "version=$VER no health marker in ${TIMEOUT}s"
touch "$HEALTH/$VER.bad"                 # never try this release again
rm -f "$PENDING"

if [ -L "$PP_RELEASES/previous" ]; then
	# A relative link, the same shape install.sh writes.
	prev="$(readlink "$PP_RELEASES/previous")"
	ln -sfn "$prev" "$PP_RELEASES/current"
	oplog update.rollback "back to $prev"
else
	oplog update.rollback.skip "no previous release to go back to"
	exit 1
fi

# Boot loop guard (plan 3.3 item 4). Count the gate failures of the last hour.
# Past three we stop restarting: a restart loop is worse than a stopped daemon,
# and the state is visible over SSH and on the serial console.
now="$(date -u +%s)"
echo "$now" >>"$FAILS"
recent=0
while read -r t; do
	case "$t" in ''|*[!0-9]*) continue ;; esac
	[ $((now - t)) -lt 3600 ] && recent=$((recent + 1))
done <"$FAILS"
# Keep the file small.
tail -n 20 "$FAILS" >"$FAILS.new" 2>/dev/null && mv "$FAILS.new" "$FAILS"

if [ "$recent" -gt 3 ]; then
	oplog update.gate.loopguard "$recent failures in one hour; no restart"
	exit 1
fi

oplog update.restart "restart portapixeld on the rolled back release"
# Detached, so we do not restart the service from inside its own start.
rc-service portapixeld restart
