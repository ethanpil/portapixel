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

oplog firstboot.start "version=$(cat /etc/portapixel-release 2>/dev/null | tr '\n' ' ')"

# --------------------------------------------------------------------- helpers
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

# Tell the kernel about the new partition table.
#   $1 = the disk, $2 = the partition node, $3 = the partition number
# "partx -u --nr N" first: it resizes one partition and works on a busy disk,
# where partprobe refuses the whole table. The others are fallbacks.
reread_table() {
	partx -u --nr "$3" "$1" 2>/dev/null ||
		partx -u "$1" 2>/dev/null ||
		partprobe "$1" 2>/dev/null || true
	_i=0
	while [ "$_i" -lt 20 ]; do
		[ -b "$2" ] && return 0
		sleep 1
		_i=$((_i + 1))
	done
	return 0
}

# ------------------------------------------------- 1. grow PPMEDIA (D36)
# exFAT has no grow tool, so the partition is deleted and made again at full
# size. A plain mkfs would delete the WiFi credentials that a headless user just
# copied onto the card, so the contents go to ext4 first and come back after.
grow_media() {
	if [ -f "$M_GROWN" ]; then
		return 0
	fi

	MEDIA_DEV="$(findfs LABEL=PPMEDIA 2>/dev/null || true)"
	if [ -z "$MEDIA_DEV" ]; then
		oplog firstboot.grow.skip "no PPMEDIA label: this is an on-box install"
		touch "$M_GROWN"
		return 0
	fi
	if ! split_part "$MEDIA_DEV"; then
		oplog firstboot.grow.skip "cannot read the disk of $MEDIA_DEV"
		touch "$M_GROWN"
		return 0
	fi
	DISK="$PART_DISK"
	NUM="$PART_NUM"

	# An image install has PPROOT on the same disk. When it does not, somebody
	# gave us a media partition by hand and the layout is not ours to change.
	ROOT_DEV="$(findfs LABEL=PPROOT 2>/dev/null || true)"
	case "$ROOT_DEV" in
	"$DISK"*) ;;
	*)
		oplog firstboot.grow.skip "PPROOT is not on $DISK: not an image install"
		touch "$M_GROWN"
		return 0
		;;
	esac

	# The backup GPT header sits at the end of the IMAGE, not at the end of the
	# medium. Move it first, or every sector number below is wrong.
	sgdisk -e "$DISK" >/dev/null 2>&1 || true

	LAST_USABLE="$(sgdisk -E "$DISK" 2>/dev/null || echo 0)"
	P3_END="$(sgdisk -i "$NUM" "$DISK" 2>/dev/null | awk '/Last sector/ { print $3; exit }')"
	P3_START="$(sgdisk -i "$NUM" "$DISK" 2>/dev/null | awk '/First sector/ { print $3; exit }')"
	case "$LAST_USABLE$P3_END$P3_START" in
	''|*[!0-9]*)
		oplog firstboot.grow.skip "cannot read the sector numbers of $MEDIA_DEV"
		touch "$M_GROWN"
		return 0
		;;
	esac

	if [ $((LAST_USABLE - P3_END)) -lt "$SLACK_SECTORS" ] && [ ! -f "$M_STAGED" ]; then
		oplog firstboot.grow.skip "p3 already fills the disk"
		touch "$M_GROWN"
		return 0
	fi

	# --- phase A: contents to ext4 -------------------------------------------
	if [ ! -f "$M_STAGED" ]; then
		mountpoint -q "$PP_MEDIA" || mount "$PP_MEDIA" 2>/dev/null || true
		if ! mountpoint -q "$PP_MEDIA"; then
			oplog firstboot.grow.fail "cannot mount $PP_MEDIA to stage it"
			return 1
		fi
		echo "$P3_START" >"$M_START"
		rm -rf "$STAGE"
		mkdir -p "$STAGE"
		# The image p3 is small by construction, so the copy always fits.
		cp -a "$PP_MEDIA/." "$STAGE/" 2>/dev/null || true
		sync
		touch "$M_STAGED"
		oplog firstboot.grow.staged "from $MEDIA_DEV to $STAGE"
	fi

	# --- phase B: make p3 again at full size ----------------------------------
	if [ ! -f "$M_RECREATED" ]; then
		umount "$PP_MEDIA" 2>/dev/null || true
		if mountpoint -q "$PP_MEDIA"; then
			oplog firstboot.grow.fail "cannot unmount $PP_MEDIA"
			return 1
		fi
		START="$(cat "$M_START" 2>/dev/null || echo "$P3_START")"
		OLD_SECTORS="$(kernel_sectors "$MEDIA_DEV")"
		# 0700 is the Microsoft basic data type, which is right for exFAT.
		if ! sgdisk -d "$NUM" -n "$NUM:$START:0" -t "$NUM:0700" -c "$NUM:PPMEDIA" \
			"$DISK" >/dev/null 2>&1; then
			oplog firstboot.grow.fail "sgdisk could not make p$NUM again"
			return 1
		fi
		reread_table "$DISK" "$MEDIA_DEV" "$NUM"
		# The GPT is right now, but the kernel may still hold the old size,
		# because the root is on this same disk. mkfs on the old size would make
		# a small file system and set the marker, and the chance would be gone.
		# Stop here instead: the new table is on the disk, the old contents are
		# still in staging, and the next boot reads the new size and finishes.
		NEW_SECTORS="$(kernel_sectors "$MEDIA_DEV")"
		if [ "$NEW_SECTORS" -le "$OLD_SECTORS" ]; then
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
		touch "$M_RECREATED"
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
		rm -rf "$STAGE"
	fi
	rm -f "$M_START" "$M_STAGED" "$M_RECREATED"
	touch "$M_GROWN"
	oplog firstboot.grow.done "PPMEDIA now fills $DISK"
	return 0
}

# --------------------------------------------------------- 2. ssh host keys
ssh_host_keys() {
	[ -f "$M_HOSTKEYS" ] && return 0
	if command -v ssh-keygen >/dev/null 2>&1; then
		ssh-keygen -A >/dev/null 2>&1 || true
		touch "$M_HOSTKEYS"
		oplog firstboot.sshkeys "host keys made"
	fi
	return 0
}

# ------------------------------------------------------- 3. root password
root_password() {
	[ -f "$M_ROOTPW" ] && return 0
	printf 'root:%s\n' "$DEFAULT_ROOT_PASSWORD" | chpasswd >/dev/null 2>&1 || {
		oplog firstboot.rootpw.fail "chpasswd failed"
		return 0
	}
	# The daemon compares this hash with the live one to know whether the user
	# has changed the password yet, and it nags until they have (D23).
	awk -F: '$1 == "root" { print $2 }' /etc/shadow >"$PP_STATE/.root-default-hash"
	chmod 0600 "$PP_STATE/.root-default-hash"
	touch "$M_ROOTPW"
	oplog firstboot.rootpw "default root password set"
	return 0
}

# ----------------------------------------------------------------- 4. run it
rc=0
grow_media || rc=1
ssh_host_keys
root_password

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
	touch "$DONE"
	oplog firstboot.done "provisioned"
else
	oplog firstboot.incomplete "a step did not finish; it runs again on the next boot"
fi
exit "$rc"
