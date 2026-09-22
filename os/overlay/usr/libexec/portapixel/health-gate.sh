#!/bin/sh
# Update health gate and rollback (plan section 15).
#
# The updater stages a release, flips the "current" link and leaves a pending
# marker. This script gives the new daemon PP_HEALTH_TIMEOUT seconds to write
#   $PP_RUN/health/<version>.ok
# The marker can fail to appear. This script then puts "current" back to
# "previous". It marks the release bad, so the updater never installs it again.
# Then it restarts the service.
#
# WHY THE MARKER IS IN TMPFS. A health marker is true for one boot only, so its
# place is /run, which the kernel empties at every start. A marker of an earlier
# boot cannot exist, so no step has to remove one, and no rule about the order of
# two processes is necessary. The first design kept the marker on the flash. It
# then needed an "arm" step in start_pre, an order rule between the gate and the
# daemon, and a loop in the daemon that wrote the marker again every few seconds.
# That loop wrote to the flash about 17,000 times a day while the gate stayed
# armed, against D2.
#
# THREE FILES STAY ON THE FLASH, because each must survive a restart:
#   $PP_RELEASES/.swap-pending        the version that must prove itself
#   $PP_RELEASES/health/<ver>.bad     the one channel to updater CheckRollback
#   $PP_RELEASES/health/gate-boots    how many starts this pending version got
# Each of the three gets a "sync" where it is written, in the rollback path.
#
# THIS SCRIPT REMOVES NO MARKER. The init script clears the tmpfs health
# directory in start_pre, which covers a restart of the service with no restart of
# the machine.
#
# With no pending marker it exits at once. The service starts it on every start of
# portapixeld, so it must stay cheap.
set -u

. /usr/libexec/portapixel/oplog.sh

# One mode, "wait". The old "arm" mode is gone with the marker on the flash.
MODE="${1:-wait}"
case "$MODE" in
wait) ;;
arm)
	printf 'health-gate.sh: the "arm" mode is gone. The health marker is in\n' >&2
	printf '  %s/health and the kernel empties that on every boot.\n' "${PP_RUN:-/run/portapixel}" >&2
	exit 2
	;;
*) printf 'health-gate.sh: unknown mode %s; the only mode is "wait"\n' "$MODE" >&2; exit 2 ;;
esac

PENDING="$PP_RELEASES/.swap-pending"     # holds the version that must prove itself
FLASH="$PP_RELEASES/health"              # .bad and the boot counter, on ext4
RUNHEALTH="${PP_RUN:-/run/portapixel}/health"  # the .ok marker, in tmpfs
BOOTS="$FLASH/gate-boots"                # how many starts this pending version got
TIMEOUT="${PP_HEALTH_TIMEOUT:-120}"
# How often to look for the marker. A test runs the whole gate many times, so the
# step is a knob and not a number in the loop.
POLL="${PP_HEALTH_POLL:-2}"

[ -f "$PENDING" ] || exit 0

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

# One gate at a time. The service starts this script in the background on every
# start of portapixeld. Without this lock, a restart during a pending update
# gives two gates one pending marker to share.
LOCK="${PP_RUN:-/run/portapixel}/health-gate.lock"
mkdir -p "${PP_RUN:-/run/portapixel}" 2>/dev/null || true
mkdir "$LOCK" 2>/dev/null || exit 0
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT INT TERM

# The two numbers come from /etc/conf.d/portapixeld, which a person can edit.
# ":-" only covers an empty value. A value such as "120s" makes every test below
# fail. The wait then does not happen, and the gate rolls back every update at
# once.
case "$TIMEOUT" in
''|*[!0-9]*) TIMEOUT=120 ;;
esac
case "$POLL" in
''|*[!0-9]*|0) POLL=2 ;;
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

oplog update.gate.start "version=$VER timeout=${TIMEOUT}s poll=${POLL}s start=$boots"

waited=0
while [ "$waited" -lt "$TIMEOUT" ]; do
	if [ -f "$RUNHEALTH/$VER.ok" ]; then
		# The marker names the run that wrote it: version, pid and start time. Put
		# it in the log, so a pass can be read back from the ops log alone.
		mark="$(head -n1 "$RUNHEALTH/$VER.ok" 2>/dev/null)"
		rm -f "$PENDING" "$BOOTS"
		sync
		oplog update.gate.pass "version=$VER after=${waited}s marker=$mark"
		exit 0
	fi
	sleep "$POLL"
	waited=$((waited + POLL))
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
if ! : >"$FLASH/$VER.bad"; then
	oplog update.gate.markfail "cannot write $FLASH/$VER.bad; the gate stays armed"
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
