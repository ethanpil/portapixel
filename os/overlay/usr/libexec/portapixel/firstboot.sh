#!/bin/sh
# PortaPixel first boot (plan section 14, D36).
#
# Every step has its own marker file, so a power cut in the middle is safe: the
# next boot goes on from the step that did not finish. Nothing here parses TOML.
# The daemon does that work in "portapixeld provision".
set -u

. /usr/libexec/portapixel/oplog.sh

DONE="$PP_STATE/.provisioned"
STAGE="$PP_STATE/.stage-ppmedia"
M_START="$PP_STATE/.fb-p3-start"          # first sector of p3, saved before we delete it
M_STAGED="$PP_STATE/.fb-p3-staged"
M_RECREATED="$PP_STATE/.fb-p3-recreated"
M_GROWN="$PP_STATE/.fb-p3-grown"
M_HOSTKEYS="$PP_STATE/.fb-hostkeys"
M_ROOTPW="$PP_STATE/.fb-rootpw"
M_ZRAM="$PP_STATE/.fb-zram"

# Memory under which this device gets zram swap, in kB. It is the same 1 GiB that
# internal/device/health lowRAMBytes uses for the low tier, so one machine is
# never "low tier with no swap".
LOW_RAM_KB=1048576

# The password the manual documents. The web UI nags until it changes (D23).
DEFAULT_ROOT_PASSWORD="portapixel"

# Free sectors we accept at the end of the disk before we call p3 "full".
# 65536 sectors is 32 MiB with 512 byte sectors. Under that the dance is not
# worth the risk.
SLACK_SECTORS=65536

mkdir -p "$PP_STATE"

if [ -f "$DONE" ]; then
	exit 0
fi

# One run at a time. The marker file is not a lock. A long copy looks like a
# hang, so an operator can start the service a second time. Two runs would then
# race on the staging directory and on sgdisk. /run is a tmpfs, so a power cut
# leaves no stale lock.
LOCK="${PP_RUN:-/run/portapixel}/firstboot.lock"
mkdir -p "${PP_RUN:-/run/portapixel}" 2>/dev/null || true
if ! mkdir "$LOCK" 2>/dev/null; then
	oplog firstboot.busy "another first boot run holds $LOCK"
	exit 0
fi
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT INT TERM

oplog firstboot.start "version=$(tr '\n' ' ' </etc/portapixel-release 2>/dev/null)"

# --------------------------------------------------------------------- helpers
# Write a marker and put it on the disk before the caller goes on. PPROOT has
# the mount option commit=60. A marker that is only in the page cache can be
# lost for up to a minute. Each marker guards a step that destroys data, so the
# marker must reach the disk FIRST.
mark() {
	: >"$1"
	sync
}

# Count the regular files in a tree and add up their sizes. The result compares
# a copy with its source across two file systems, because a size in bytes does
# not depend on the block size. "find -exec ... +" and not "xargs -0": busybox
# xargs runs the command one time even when the input is empty.
tree_sig() {
	find "$1" -type f -exec stat -c '%s' {} + 2>/dev/null |
		awk '{ n++; b += $1 } END { printf "%d %d\n", n + 0, b + 0 }'
}

# Split a partition device into its disk and its partition number.
#   /dev/sda3       -> /dev/sda      3
#   /dev/vda3       -> /dev/vda      3
#   /dev/mmcblk0p3  -> /dev/mmcblk0  3
#   /dev/nvme0n1p3  -> /dev/nvme0n1  3
# The rule: when a digit comes before the "p", the "p" belongs to the name.
split_part() {
	_dev="$1"
	case "$_dev" in
	*[0-9]p[0-9]*)
		PART_DISK="${_dev%p*}"
		PART_NUM="${_dev##*p}"
		;;
	*)
		PART_NUM="$(echo "$_dev" | sed 's/.*[^0-9]\([0-9][0-9]*\)$/\1/')"
		PART_DISK="${_dev%"$PART_NUM"}"
		;;
	esac
	case "$PART_NUM" in ''|*[!0-9]*) return 1 ;; esac
	[ -b "$PART_DISK" ] || return 1
	return 0
}

# How many sectors the KERNEL thinks a partition has. This is not the same as
# what the GPT says: the root is on the same disk, so the disk is busy and the
# kernel can refuse to re-read the table.  $1 = the partition node.
kernel_sectors() {
	cat "/sys/class/block/$(basename "$1")/size" 2>/dev/null || echo 0
}

# How many sectors the GPT gives a partition. This is the size we want the
# kernel to agree with.  $1 = the disk, $2 = the partition number
gpt_sectors() {
	sgdisk -i "$2" "$1" 2>/dev/null |
		awk '/Partition size/ { print $3; exit }'
}

# Tell the kernel about the new partition table and WAIT until it agrees with
# the GPT.
#   $1 = the disk, $2 = the partition node, $3 = the partition number
# "partx -u --nr N" first: it resizes one partition and works on a busy disk,
# where partprobe refuses the whole table. The others are fallbacks.
# The node itself is not the thing to wait for: "partx -u" never removes it, so
# a test for the node is true at once and proves nothing. Wait for the SIZE.
reread_table() {
	_want="$(gpt_sectors "$1" "$3")"
	_i=0
	while [ "$_i" -lt 20 ]; do
		partx -u --nr "$3" "$1" 2>/dev/null ||
			partx -u "$1" 2>/dev/null ||
			partprobe "$1" 2>/dev/null || true
		[ -b "$2" ] && [ -n "$_want" ] &&
			[ "$(kernel_sectors "$2")" = "$_want" ] && return 0
		sleep 1
		_i=$((_i + 1))
	done
	return 1
}

# ------------------------------------------------- 1. grow PPMEDIA (D36)
# exFAT has no grow tool. The partition is deleted and made again at full size.
# A plain mkfs would delete the WiFi credentials of the user. A headless user
# copies those onto the card before the first boot. The contents go to ext4
# first and come back after.
grow_media() {
	if [ -f "$M_GROWN" ]; then
		return 0
	fi

	# Take the device that is MOUNTED at the media root, never a label lookup.
	# "findfs LABEL=PPMEDIA" answers with the first match in the whole system.
	# Leave the install stick in an on-box machine, and that answer is the STICK.
	# The dance would then delete and re-make the partition of the stick.
	MEDIA_DEV="$(awk -v m="$PP_MEDIA" '$2 == m { print $1 }' /proc/mounts | tail -n1)"
	if [ -z "$MEDIA_DEV" ] || [ ! -b "$MEDIA_DEV" ]; then
		oplog firstboot.grow.skip \
			"nothing is mounted at $PP_MEDIA: this is an on-box install"
		mark "$M_GROWN"
		return 0
	fi
	if ! split_part "$MEDIA_DEV"; then
		oplog firstboot.grow.skip "cannot read the disk of $MEDIA_DEV"
		mark "$M_GROWN"
		return 0
	fi
	DISK="$PART_DISK"
	NUM="$PART_NUM"

	# An image install has the root on the same disk. When it does not, somebody
	# gave us a media partition by hand and the layout is not ours to change.
	# The root comes from /proc/mounts too, for the same reason as above.
	ROOT_DEV="$(awk '$2 == "/" { print $1 }' /proc/mounts | tail -n1)"
	if ! split_part "$ROOT_DEV"; then
		oplog firstboot.grow.skip "cannot read the disk of the root $ROOT_DEV"
		mark "$M_GROWN"
		return 0
	fi
	# An exact match on the disk. A prefix test would let /dev/sda match
	# /dev/sdaa and send us to the wrong disk.
	if [ "$PART_DISK" != "$DISK" ]; then
		oplog firstboot.grow.skip "the root is on $PART_DISK, not on $DISK"
		mark "$M_GROWN"
		return 0
	fi

	# The backup GPT header sits at the end of the IMAGE, not at the end of the
	# medium. Move it first, or every sector number below is wrong. A failure
	# here is not something to pass over: the numbers below would then describe
	# the image and we would re-write the live GPT for no gain.
	if ! sgdisk -e "$DISK" >/dev/null 2>&1; then
		oplog firstboot.grow.fail "sgdisk -e cannot move the backup header of $DISK"
		return 1
	fi

	LAST_USABLE="$(sgdisk -E "$DISK" 2>/dev/null || echo 0)"
	P3_END="$(sgdisk -i "$NUM" "$DISK" 2>/dev/null | awk '/Last sector/ { print $3; exit }')"
	P3_START="$(sgdisk -i "$NUM" "$DISK" 2>/dev/null | awk '/First sector/ { print $3; exit }')"
	# Test each number ON ITS OWN. A test of the three joined together is not
	# safe. Give the first the value "0" and leave the other two empty: the join
	# is then the single digit "0", which passes. The arithmetic below reads an
	# empty value as 0. The next test then says "p3 already fills the disk", the
	# marker goes down, and the card never grows.
	# A transient read error must not give up the grow for ever: no marker here.
	for _n in "$LAST_USABLE" "$P3_END" "$P3_START"; do
		case "$_n" in
		''|*[!0-9]*)
			oplog firstboot.grow.fail \
				"cannot read the sector numbers of $MEDIA_DEV (got '$LAST_USABLE' '$P3_END' '$P3_START')"
			return 1
			;;
		esac
	done

	if [ $((LAST_USABLE - P3_END)) -lt "$SLACK_SECTORS" ] && [ ! -f "$M_STAGED" ]; then
		oplog firstboot.grow.skip "p3 already fills the disk"
		mark "$M_GROWN"
		return 0
	fi

	# --- phase A: contents to ext4 -------------------------------------------
	if [ ! -f "$M_STAGED" ]; then
		mountpoint -q "$PP_MEDIA" || mount "$PP_MEDIA" 2>/dev/null || true
		if ! mountpoint -q "$PP_MEDIA"; then
			oplog firstboot.grow.fail "cannot mount $PP_MEDIA to stage it"
			return 1
		fi
		SRC_SIG="$(tree_sig "$PP_MEDIA")"
		SRC_KB=$(( ${SRC_SIG#* } / 1024 + 1 ))
		# The image p3 is small, but the number is an invariant of the build and
		# not a law. Ask before the copy instead of filling PPROOT, which would
		# also take the ops log and "portapixeld provision" with it.
		FREE_KB="$(df -k "$PP_STATE" | awk 'NR == 2 { print $4 }')"
		case "$FREE_KB" in ''|*[!0-9]*) FREE_KB=0 ;; esac
		if [ "$FREE_KB" -lt $((SRC_KB + 65536)) ]; then
			oplog firstboot.grow.fail \
				"staging needs ${SRC_KB} kB plus room to spare, $PP_STATE has ${FREE_KB} kB"
			return 1
		fi
		printf '%s\n' "$P3_START" >"$M_START"
		# Last line of defence. We are about to delete an old staging directory,
		# and phase A only runs when the marker says staging is not finished. If
		# that directory holds files and the live PPMEDIA does not, the marker is
		# wrong and the staging copy is the only one left. Keep it and stop.
		if [ -d "$STAGE" ]; then
			OLD_SIG="$(tree_sig "$STAGE")"
			if [ "${OLD_SIG% *}" -gt 0 ] && [ "${SRC_SIG% *}" = 0 ]; then
				oplog firstboot.grow.fail \
					"$STAGE holds ($OLD_SIG) and $PP_MEDIA is empty; staging is kept for a person to look at"
				return 1
			fi
		fi
		rm -rf "$STAGE"
		mkdir -p "$STAGE"
		cp -a "$PP_MEDIA/." "$STAGE/" 2>/dev/null || true
		sync
		# PROVE the copy before the marker goes down, because the marker lets the
		# next step delete the partition. The exit status of cp is not the test:
		# "cp -a" also preserves the owner and the mode, which exFAT does not
		# carry, so cp reports a failure on a copy that is complete. Count the
		# files and add up their sizes instead.
		DST_SIG="$(tree_sig "$STAGE")"
		if [ "$SRC_SIG" != "$DST_SIG" ]; then
			oplog firstboot.grow.fail \
				"staging does not match: $PP_MEDIA has ($SRC_SIG), $STAGE has ($DST_SIG)"
			return 1
		fi
		mark "$M_STAGED"
		oplog firstboot.grow.staged "from $MEDIA_DEV to $STAGE, verified ($SRC_SIG)"
	fi

	# --- phase B: make p3 again at full size ----------------------------------
	if [ ! -f "$M_RECREATED" ]; then
		umount "$PP_MEDIA" 2>/dev/null || true
		if mountpoint -q "$PP_MEDIA"; then
			oplog firstboot.grow.fail "cannot unmount $PP_MEDIA"
			return 1
		fi
		START="$(cat "$M_START" 2>/dev/null || echo "$P3_START")"
		# 0700 is the Microsoft basic data type, which is right for exFAT.
		if ! sgdisk -d "$NUM" -n "$NUM:$START:0" -t "$NUM:0700" -c "$NUM:PPMEDIA" \
			"$DISK" >/dev/null 2>&1; then
			oplog firstboot.grow.fail "sgdisk could not make p$NUM again"
			return 1
		fi
		# The GPT is right now, but the kernel may still hold the old size,
		# because the root is on this same disk. mkfs on the old size would make
		# a small file system and set the marker, and the chance would be gone.
		# Stop here instead: the new table is on the disk, the old contents are
		# still in staging, and the next boot reads the new size and finishes.
		#
		# The test is the KERNEL size against the size the GPT now states. A test
		# against the size from before the sgdisk call cannot be re-run. On the
		# second boot the kernel already holds the new size. The two numbers are
		# then equal, and the dance stops here on every boot. PPMEDIA stays
		# empty, and the only copy of the files of the user stays in staging.
		if ! reread_table "$DISK" "$MEDIA_DEV" "$NUM"; then
			# Put the old file system back in place, so the device still works
			# for this one boot.
			mount "$PP_MEDIA" 2>/dev/null || true
			oplog firstboot.grow.deferred \
				"the kernel keeps the old size for $MEDIA_DEV; the next boot finishes the job"
			return 1
		fi
		if ! mkfs.exfat -L PPMEDIA "$MEDIA_DEV" >/dev/null 2>&1; then
			oplog firstboot.grow.fail "mkfs.exfat failed on $MEDIA_DEV"
			return 1
		fi
		sync
		mark "$M_RECREATED"
		oplog firstboot.grow.recreated "$MEDIA_DEV from sector $START to the end"
	fi

	# --- phase C: contents back ----------------------------------------------
	mountpoint -q "$PP_MEDIA" || mount "$PP_MEDIA" 2>/dev/null || true
	if ! mountpoint -q "$PP_MEDIA"; then
		oplog firstboot.grow.fail "cannot mount the new $PP_MEDIA"
		return 1
	fi
	if [ -d "$STAGE" ]; then
		cp -a "$STAGE/." "$PP_MEDIA/" 2>/dev/null || true
		sync
		# PROVE the restore before the staging copy goes away. Until this test
		# passes, staging holds the only copy of the user's files. The same rule
		# as phase A applies: compare the files, never the status of cp.
		SRC_SIG="$(tree_sig "$STAGE")"
		DST_SIG="$(tree_sig "$PP_MEDIA")"
		if [ "$SRC_SIG" != "$DST_SIG" ]; then
			oplog firstboot.grow.fail \
				"restore does not match: $STAGE has ($SRC_SIG), $PP_MEDIA has ($DST_SIG); staging is kept"
			return 1
		fi
		oplog firstboot.grow.restored "$PP_MEDIA verified ($DST_SIG)"
		rm -rf "$STAGE"
		sync
	fi
	rm -f "$M_START" "$M_STAGED" "$M_RECREATED"
	mark "$M_GROWN"
	oplog firstboot.grow.done "PPMEDIA now fills $DISK"
	return 0
}

# --------------------------------------------------------- 2. ssh host keys
ssh_host_keys() {
	[ -f "$M_HOSTKEYS" ] && return 0
	if command -v ssh-keygen >/dev/null 2>&1; then
		# "ssh-keygen -A" makes only the keys that are missing. A re-run after a
		# lost marker cannot replace a key. A saved known_hosts entry stays
		# correct.
		ssh-keygen -A >/dev/null 2>&1 || true
		mark "$M_HOSTKEYS"
		oplog firstboot.sshkeys "host keys made"
	fi
	return 0
}

# ------------------------------------------------------------ 3. zram swap
# A machine with less than 1 GiB of memory cannot run Chromium with no swap.
# Measured: 512 MB and no swap gives Chromium error code 4 and a restart loop.
# 512 MB with 512 MB of zram works, and the fallback screen alone puts about
# 190 MB in the swap. zram costs no flash wear, which a swap file on an SD card
# would (D2).
#
# Why the first boot and not the daemon: the swap must be there before the browser
# starts, and the browser is what the daemon starts. A machine does not change its
# memory, so the answer is true for the life of the box.
#
# A host that already has swap keeps what its owner set. An on-box install must
# never add a second swap device behind the back of the owner.
zram_swap() {
	[ -f "$M_ZRAM" ] && return 0

	_kb="$(awk '/^MemTotal:/ { print $2; exit }' /proc/meminfo 2>/dev/null)"
	case "$_kb" in ''|*[!0-9]*)
		oplog firstboot.zram.skip "cannot read MemTotal"
		mark "$M_ZRAM"
		return 0
		;;
	esac
	if [ "$_kb" -ge "$LOW_RAM_KB" ]; then
		oplog firstboot.zram.skip "${_kb} kB of memory, so no swap is necessary"
		mark "$M_ZRAM"
		return 0
	fi
	# /proc/swaps always has a header line. More than one line means swap is on.
	if [ "$(wc -l <"/proc/swaps" 2>/dev/null || echo 1)" -gt 1 ]; then
		oplog firstboot.zram.skip "this machine already has swap"
		mark "$M_ZRAM"
		return 0
	fi
	if [ ! -f /etc/init.d/zram-init ]; then
		oplog firstboot.zram.fail "zram-init is not installed and this machine needs swap"
		mark "$M_ZRAM"
		return 0
	fi

	# One device, as large as the memory. That is the pair that was measured.
	# The file of the package makes TWO devices and mounts the second on /tmp,
	# which PortaPixel already has as a tmpfs (D2), so write our own file.
	_mb=$((_kb / 1024))
	cat >/etc/conf.d/zram-init <<EOF
# PortaPixel writes this file on the first boot of a machine with less than
# 1 GiB of memory. Chromium cannot run on 512 MB with no swap.
# ONE device, and it is swap. Do not add a second device on /tmp: /tmp is
# already a size capped tmpfs (D2).
load_on_start=yes
unload_on_stop=yes
num_devices=1
type0=swap
flag0=
size0=$_mb
mlim0=
algo0=zstd
labl0=zram_swap
EOF
	rc-update add zram-init boot >/dev/null 2>&1 || true
	if ! rc-service zram-init start >/dev/null 2>&1; then
		oplog firstboot.zram.fail "zram-init did not start; first boot runs again"
		return 1
	fi
	mark "$M_ZRAM"
	oplog firstboot.zram "${_mb} MB of zram swap for a machine with ${_kb} kB of memory"
	return 0
}

# ------------------------------------------------------- 4. root password
root_password() {
	[ -f "$M_ROOTPW" ] && return 0
	# Report the failure to the caller. A silent "return 0" let the run finish,
	# the .provisioned marker go down, and the box keep the locked root account
	# of a fresh Alpine: SSH then refuses the documented password and the
	# recovery path of D23 is gone.
	if ! printf 'root:%s\n' "$DEFAULT_ROOT_PASSWORD" | chpasswd >/dev/null 2>&1; then
		oplog firstboot.rootpw.fail "chpasswd failed; first boot runs again"
		return 1
	fi
	# The daemon compares this hash with the live one. A difference shows that
	# the user has set a new password. The UI asks for a new password until then
	# (D23). Make the file private BEFORE the hash goes into it. With a umask of
	# 022, everybody could read a root password hash for a moment.
	_h="$PP_STATE/.root-default-hash"
	: >"$_h"
	chmod 0600 "$_h"
	awk -F: '$1 == "root" { print $2 }' /etc/shadow >"$_h"
	mark "$M_ROOTPW"
	oplog firstboot.rootpw "default root password set"
	return 0
}

# ----------------------------------------------------------------- 5. run it
rc=0
grow_media || rc=1
ssh_host_keys
# The swap comes before "portapixeld provision" and before the daemon starts the
# browser: a 512 MB machine with no swap cannot hold Chromium.
zram_swap || rc=1
root_password || rc=1

# Steps 2, 4 and 5 of plan section 14 belong to the daemon: the device id, the
# default portapixel.toml and the default playlist (ARCHITECTURE section 4).
if [ -x /opt/portapixel/current/portapixeld ]; then
	prc=0
	/opt/portapixel/current/portapixeld provision \
		--media "$PP_MEDIA" --state "$PP_STATE" --releases "$PP_RELEASES" || prc=$?
	if [ "$prc" = 0 ]; then
		oplog firstboot.provision "ok"
	else
		oplog firstboot.provision.fail "portapixeld provision returned $prc"
		rc=1
	fi
else
	oplog firstboot.provision.skip "no daemon binary"
	rc=1
fi

if [ "$rc" = 0 ]; then
	mark "$DONE"
	oplog firstboot.done "provisioned"
else
	oplog firstboot.incomplete "a step did not finish; it runs again on the next boot"
fi
exit "$rc"
