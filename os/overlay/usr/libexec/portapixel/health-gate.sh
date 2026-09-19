#!/bin/sh
# Update health gate and rollback (plan section 15).
#
# The updater stages a release, flips the "current" link and leaves a pending
# marker. This script gives the new daemon PP_HEALTH_TIMEOUT seconds to write
#   $PP_RELEASES/health/<version>.ok
# The marker can fail to appear. This script then puts "current" back to
# "previous". It marks the release bad, so the updater never installs it again.
# Then it restarts the service.
#
# With no pending marker the script exits at once. It runs on every start of
# portapixeld, so it must stay cheap.
set -u

. /usr/libexec/portapixel/oplog.sh

PENDING="$PP_RELEASES/.swap-pending"     # holds the version that must prove itself
HEALTH="$PP_RELEASES/health"
BOOTS="$HEALTH/gate-boots"               # how many starts this pending version got
TIMEOUT="${PP_HEALTH_TIMEOUT:-120}"

[ -f "$PENDING" ] || exit 0

# One gate at a time. The service starts this script in the background on every
# start of portapixeld. Without this lock, a restart during a pending update
# gives two gates one pending marker to share.
LOCK="${PP_RUN:-/run/portapixel}/health-gate.lock"
mkdir -p "${PP_RUN:-/run/portapixel}" 2>/dev/null || true
mkdir "$LOCK" 2>/dev/null || exit 0
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT INT TERM

VER="$(head -n1 "$PENDING" 2>/dev/null)"
# A version becomes a file name below, so hold it to the shape internal/updater
# accepts. A value with a "/" in it would write outside the health directory.
case "$VER" in
''|*/*|*[!A-Za-z0-9._+-]*)
	oplog update.gate.badversion "the pending marker holds no usable version"
	rm -f "$PENDING"
	exit 0
	;;
esac

# The timeout comes from /etc/conf.d/portapixeld, which a person can edit.
# ":-" only covers an empty value. A value such as "120s" makes every test below
# fail. The wait then does not happen, and the gate rolls back every update at
# once.
case "$TIMEOUT" in
''|*[!0-9]*) TIMEOUT=120 ;;
esac

# --------------------------------------------------------- the boot loop guard
# Count the starts this pending version got, BEFORE the wait. A version that
# needs the gate more than twice does not come up. Leave it rolled back and stop
# restarting. A restart loop is worse than a stopped daemon. An operator can see
# the state over SSH and on the serial console.
boots=0
if [ -f "$BOOTS" ]; then
	read -r _bver _bn <"$BOOTS" 2>/dev/null || true
	[ "$_bver" = "$VER" ] && case "$_bn" in ''|*[!0-9]*) ;; *) boots="$_bn" ;; esac
fi
boots=$((boots + 1))
printf '%s %s\n' "$VER" "$boots" >"$BOOTS"
sync

oplog update.gate.start "version=$VER timeout=${TIMEOUT}s start=$boots"

# A stale marker from an earlier install of this same version would let the gate
# pass at once, with nothing proved. The new binary has to write it again.
rm -f "$HEALTH/$VER.ok"

waited=0
while [ "$waited" -lt "$TIMEOUT" ]; do
	if [ -f "$HEALTH/$VER.ok" ]; then
		rm -f "$PENDING" "$BOOTS"
		sync
		oplog update.gate.pass "version=$VER after=${waited}s"
		exit 0
	fi
	sleep 2
	waited=$((waited + 2))
done

# ------------------------------------------------------------------ the rollback
# ORDER MATTERS. The pending marker is the only thing that arms this gate, so it
# is the LAST thing to go. Flip the link first, prove the flip, then record the
# release as bad, then disarm. The other order left a box that ran the bad
# release with the gate disarmed, and supervise-daemon respawned it for ever.
oplog update.gate.fail "version=$VER no health marker in ${TIMEOUT}s"

prev=""
if [ -e "$PP_RELEASES/previous" ]; then
	# A relative link, the same shape install.sh and internal/updater write.
	prev="$(readlink "$PP_RELEASES/previous" 2>/dev/null || true)"
fi
if [ -z "$prev" ] || [ ! -x "$PP_RELEASES/$prev/portapixeld" ]; then
	# Nothing to go back to. Do NOT disarm the gate and do NOT mark the release
	# bad: the running release is all this device has, and the next start must
	# get the same chance. A fresh image reaches this only if somebody wrote a
	# pending marker by hand.
	oplog update.rollback.skip \
		"no previous release with a binary; the device stays on $VER and the gate stays armed"
	exit 1
fi

# An atomic swap, the same rule internal/updater/apply.go FlipSymlink follows.
# Make the new link beside the old one, then rename it over. "ln -sfn" unlinks
# first. A power cut in that window leaves no "current" at all. The box then has
# no daemon and no way back.
#
# "-T" is NOT optional. "current" is a symbolic link to a DIRECTORY, so a plain
# "mv -f" moves the new link INTO that directory and leaves "current" as it was.
# Measured on the test box: the new link landed in releases/<bad>/.current.new
# and the rollback did nothing. busybox mv has -T.
if ! ln -sfn "$prev" "$PP_RELEASES/.current.new" ||
	! mv -fT "$PP_RELEASES/.current.new" "$PP_RELEASES/current"; then
	rm -f "$PP_RELEASES/.current.new"
	oplog update.rollback.fail "cannot put current back to $prev; the gate stays armed"
	exit 1
fi
sync
if [ "$(readlink "$PP_RELEASES/current" 2>/dev/null)" != "$prev" ]; then
	oplog update.rollback.fail "current is not $prev after the flip; the gate stays armed"
	exit 1
fi
oplog update.rollback "back to $prev"

# Only now record the release as bad. This marker is the one channel to
# internal/updater CheckRollback, so a lost write would make the updater install
# the same broken release again and again. Put it on the disk before we disarm.
if ! : >"$HEALTH/$VER.bad"; then
	oplog update.gate.markfail "cannot write $HEALTH/$VER.bad; the gate stays armed"
	exit 1
fi
sync
rm -f "$PENDING"
sync

if [ "$boots" -gt 2 ]; then
	oplog update.gate.loopguard \
		"$VER had $boots starts and never came up; rolled back, no restart"
	rm -f "$BOOTS"
	exit 1
fi

oplog update.restart "restart portapixeld on the rolled back release"
# The service starts this script with "start-stop-daemon --background", so this
# runs in a process of its own and not inside the start of portapixeld.
rc-service portapixeld restart
