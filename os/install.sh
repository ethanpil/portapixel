#!/bin/sh
# os/install.sh -- the one provisioning path for PortaPixel (D51).
#
# Two modes, one code path:
#   --root DIR   put PortaPixel into a rootfs tree. os/build-image.sh uses this.
#   (no --root)  put PortaPixel onto the running Alpine system.
#
# The script is idempotent. Run it twice and the result is the same.
#
# It never parses TOML. The daemon owns all TOML work (ARCHITECTURE section 4).
set -eu

# ------------------------------------------------------------------ parameters
ROOT=""                  # empty means "the running system"
ARCH=""
BINARY=""
VERSION="0.0.0-dev"
ALPINE_RELEASE=""
MEDIA_PARTITION=""
IMAGE=0                  # 1 = the PPBOOT/PPROOT/PPMEDIA partition layout

# The mirror. Set PP_MIRROR to build behind a local mirror.
MIRROR="${PP_MIRROR:-https://dl-cdn.alpinelinux.org/alpine}"

# Fixed ids for the kiosk user. A number, not a name, because the capped tmpfs
# for the browser cache needs a numeric uid in /etc/fstab: busybox mount gives
# the option string to the kernel as it is, and the kernel cannot read a name.
KIOSK_UID=1300
KIOSK_GID=1300
# The seat group. seatd normally makes it in a post-install script, which cannot
# run in a foreign architecture root, so install.sh makes it instead.
SEAT_GID=1301

# Mount point of the size capped tmpfs that holds the browser profile and the
# browser cache (D39). The browser must never write to the flash.
# 256M, not the 96M the plan named for WPE WebKit: a Chromium user-data-dir is
# much bigger than a WebKit data directory. It holds the Local State file, the
# GPU shader cache, the code cache and the site data. 96M fills up and Chromium
# then fails in ways that look like a rendering bug.
KIOSK_CACHE=/var/cache/kiosk
KIOSK_CACHE_SIZE=256M

# The mkinitfs feature list. THIS IS THE ONLY PLACE IT IS WRITTEN (D53).
# base   the initramfs itself: busybox, the init script, modprobe
# ata    PATA and SATA disks. mkinitfs calls this "ata". There is no "sata".
# scsi   sd_mod and the SCSI disks, which is also how USB disks appear
# usb    usb-storage, so the same image boots from a USB stick
# mmc    SD cards and eMMC
# nvme   NVMe disks
# virtio the QEMU buses, so the CI smoke test can boot the image
# ext4   the PPROOT file system
#
# "kms" is NOT in the list, and that is a measured decision, not an oversight.
# The plan asked for it. On Alpine 3.23 it puts every GPU firmware blob into the
# initramfs and the file grows from 18 MB to 167 MB, almost all of it amdgpu
# firmware that is already on PPROOT. That is 149 MB of PPBOOT and 149 MB of
# decompression on every boot of a 512 MB Pi Zero 2 W, for nothing: the
# initramfs only has to find the root by label. udev and hwdrivers load the
# display drivers from the real root a second later, and the boot messages go
# to the serial port anyway (plan section 5).
# Do not re-add "kms" without a measurement that shows it is needed.
MKINITFS_FEATURES="base ata scsi usb mmc nvme virtio ext4"

SRC="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"

die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
say() { printf '==> %s\n' "$*"; }

usage() {
	cat <<'EOF'
Usage: install.sh [options]

  --root DIR             install into DIR instead of the running system
  --arch x86_64|aarch64  target architecture (default: the host architecture)
  --binary PATH          portapixeld binary to install as the release
  --version VER          release version, for example 0.1.0
  --alpine-release X.Y.Z exact Alpine release to pin the repositories to
  --media-partition DEV  format DEV as exFAT PPMEDIA and mount it (on-box only)
  --image                write the PPBOOT/PPROOT/PPMEDIA fstab (build-image.sh)
  -h, --help             this text
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--root) ROOT="${2:?--root needs a directory}"; shift 2 ;;
	--arch) ARCH="${2:?--arch needs a value}"; shift 2 ;;
	--binary) BINARY="${2:?--binary needs a path}"; shift 2 ;;
	--version) VERSION="${2:?--version needs a value}"; shift 2 ;;
	--alpine-release) ALPINE_RELEASE="${2:?--alpine-release needs a value}"; shift 2 ;;
	--media-partition) MEDIA_PARTITION="${2:?--media-partition needs a device}"; shift 2 ;;
	--image) IMAGE=1; shift ;;
	-h|--help) usage; exit 0 ;;
	*) usage >&2; die "unknown option: $1" ;;
	esac
done

[ "$(id -u)" = 0 ] || die "run this script as root"

# ------------------------------------------------------------------ validation
if [ -n "$ROOT" ]; then
	case "$ROOT" in /*) ;; *) ROOT="$PWD/$ROOT" ;; esac
	[ "$ROOT" != "/" ] || die "--root / is not allowed; leave --root out instead"
	mkdir -p "$ROOT"
	APK="apk --root $ROOT"
else
	[ "$IMAGE" = 0 ] || die "--image needs --root"
	APK="apk"
fi

if [ -z "$ARCH" ]; then
	ARCH="$(apk --print-arch 2>/dev/null || uname -m)"
fi
case "$ARCH" in
x86_64|aarch64) ;;
*) die "unsupported architecture: $ARCH" ;;
esac

if [ -z "$ALPINE_RELEASE" ]; then
	# On-box: take the release of the running system. In --root mode the caller
	# must name it, because a release must never float (D50).
	[ -z "$ROOT" ] || die "--root mode needs --alpine-release X.Y.Z"
	ALPINE_RELEASE="$(cat /etc/alpine-release)"
fi
case "$ALPINE_RELEASE" in
[0-9]*.[0-9]*.[0-9]*) ;;
*) die "--alpine-release must be an exact release like 3.23.2, not a branch" ;;
esac
# v3.23 from 3.23.2. The repositories pin to the branch of the exact release.
ALPINE_BRANCH="v$(echo "$ALPINE_RELEASE" | cut -d. -f1,2)"

if [ -n "$MEDIA_PARTITION" ]; then
	[ -z "$ROOT" ] || die "--media-partition works on the running system only"
	[ -b "$MEDIA_PARTITION" ] || die "$MEDIA_PARTITION is not a block device"
fi

[ -f "$SRC/packages.list" ] || die "cannot find $SRC/packages.list"

# Device paths (ARCHITECTURE section 3).
if [ "$IMAGE" = 1 ]; then
	MEDIA_ROOT=/media/ppmedia
elif [ -n "$MEDIA_PARTITION" ]; then
	MEDIA_ROOT=/media/ppmedia
else
	MEDIA_ROOT=/var/lib/portapixel/media
fi
STATE_DIR=/var/lib/portapixel
RUN_DIR=/run/portapixel
RELEASE_ROOT=/opt/portapixel

say "arch=$ARCH alpine=$ALPINE_RELEASE ($ALPINE_BRANCH) version=$VERSION"
say "media=$MEDIA_ROOT root=${ROOT:-/}"

# ---------------------------------------------------------- 1. apk repositories
# Pin to the branch of the exact release. Never latest-stable: that symlink
# moves on a new Alpine release and the installed base then drifts (D29, D50).
say "pin the repositories to $ALPINE_BRANCH"
mkdir -p "$ROOT/etc/apk/keys"
cat >"$ROOT/etc/apk/repositories" <<EOF
$MIRROR/$ALPINE_BRANCH/main
$MIRROR/$ALPINE_BRANCH/community
EOF

if [ -n "$ROOT" ]; then
	echo "$ARCH" >"$ROOT/etc/apk/arch"
	# The signing keys are per architecture. apk-tools 3 rejects the aarch64
	# index as UNTRUSTED when it only has the x86_64 keys, so copy the keys of
	# the TARGET architecture. They live under /usr/share/apk/keys/<arch>/ and
	# the alpine-keys package puts them there.
	if [ -d "/usr/share/apk/keys/$ARCH" ]; then
		cp -f "/usr/share/apk/keys/$ARCH/"*.pub "$ROOT/etc/apk/keys/"
	else
		cp -f /etc/apk/keys/*.pub "$ROOT/etc/apk/keys/"
	fi
	# apk needs the database before it can read an index.
	[ -f "$ROOT/lib/apk/db/installed" ] || $APK --arch "$ARCH" add --initdb >/dev/null
fi

# ------------------------------------------------------------- 2. the packages
# packages.list is the only source of the package set. Read the lines for this
# architecture: untagged lines plus the lines tagged with our architecture.
say "read the package set for $ARCH"
PKGS="$(awk -v arch="$ARCH" '
	{ sub(/[ \t]*#.*$/, "") }        # drop the rationale comment
	{ gsub(/^[ \t]+|[ \t]+$/, "") }  # trim
	$0 == "" { next }
	$1 ~ /^@/ { if ($1 == "@" arch) print $2; next }
	{ print $1 }
' "$SRC/packages.list")"
[ -n "$PKGS" ] || die "packages.list gave no packages for $ARCH"

say "install $(printf '%s\n' "$PKGS" | wc -l | tr -d ' ') packages"
# shellcheck disable=SC2086  # $APK and $PKGS are deliberate word lists
$APK --arch "$ARCH" add --no-interactive $PKGS

# ------------------------------------------------------------ 3. the kiosk user
# The browser renders web pages from the internet. It must not run as root. The
# kiosk user is the security boundary (D43). Write the account files directly,
# so the same code works on a foreign architecture root where we cannot run
# adduser, and where no post-install script can run either.
say "create the kiosk user"
if ! grep -q '^kiosk:' "$ROOT/etc/group" 2>/dev/null; then
	printf 'kiosk:x:%s:\n' "$KIOSK_GID" >>"$ROOT/etc/group"
fi
# The seatd package makes the "seat" group in a post-install script. That script
# never runs in a foreign architecture root, so make the group ourselves when it
# is missing. seatd looks the group up by name, so the number does not matter.
if ! grep -q '^seat:' "$ROOT/etc/group" 2>/dev/null; then
	printf 'seat:x:%s:\n' "$SEAT_GID" >>"$ROOT/etc/group"
fi
if ! grep -q '^kiosk:' "$ROOT/etc/passwd" 2>/dev/null; then
	printf 'kiosk:x:%s:%s:PortaPixel browser:/home/kiosk:/sbin/nologin\n' \
		"$KIOSK_UID" "$KIOSK_GID" >>"$ROOT/etc/passwd"
fi
if ! grep -q '^kiosk:' "$ROOT/etc/shadow" 2>/dev/null; then
	# "!" means the password is locked. Nobody logs in as kiosk.
	printf 'kiosk:!::0:::::\n' >>"$ROOT/etc/shadow"
fi
# video for /dev/dri, input for /dev/input, audio for /dev/snd, seat so cage can
# ask seatd for DRM master without being root (plan 3.2).
for g in video input audio seat; do
	awk -v grp="$g" -F: 'BEGIN { OFS=":" }
		$1 == grp {
			n = split($4, m, ",")
			for (i = 1; i <= n; i++) if (m[i] == "kiosk") { print; next }
			$4 = ($4 == "" ? "kiosk" : $4 ",kiosk")
		}
		{ print }
	' "$ROOT/etc/group" >"$ROOT/etc/group.new"
	mv "$ROOT/etc/group.new" "$ROOT/etc/group"
done
# Read the real ids back: an on-box install may already have a kiosk user.
KIOSK_UID="$(awk -F: '$1 == "kiosk" { print $3 }' "$ROOT/etc/passwd")"
KIOSK_GID="$(awk -F: '$1 == "kiosk" { print $4 }' "$ROOT/etc/passwd")"
mkdir -p "$ROOT/home/kiosk"
chown "$KIOSK_UID:$KIOSK_GID" "$ROOT/home/kiosk"
chmod 0700 "$ROOT/home/kiosk"

# ------------------------------------------------------- 4. the /opt/portapixel
say "make the release layout"
mkdir -p "$ROOT$RELEASE_ROOT/releases/$VERSION" "$ROOT$RELEASE_ROOT/health"
if [ -n "$BINARY" ]; then
	[ -f "$BINARY" ] || die "--binary $BINARY does not exist"
	install -m 0755 "$BINARY" "$ROOT$RELEASE_ROOT/releases/$VERSION/portapixeld"
fi
# A relative link, so it is correct inside --root and after the boot.
ln -sfn "releases/$VERSION" "$ROOT$RELEASE_ROOT/current"
mkdir -p "$ROOT$STATE_DIR" "$ROOT$MEDIA_ROOT"
chmod 0755 "$ROOT$STATE_DIR"

# --------------------------------------------------------------- 5. the overlay
say "copy the overlay"
[ -d "$SRC/overlay" ] || die "cannot find $SRC/overlay"
cp -a "$SRC/overlay/." "$ROOT/"
# A shell script needs the execute bit. git keeps it, a zip export does not.
# "|| :" so a glob that matches nothing cannot make the loop return non-zero,
# which set -e would turn into an exit.
for f in "$ROOT"/etc/init.d/portapixel* "$ROOT"/usr/libexec/portapixel/*.sh; do
	[ -f "$f" ] && chmod 0755 "$f" || :
done

# Default media for the first boot (D37).
if [ -d "$SRC/default-media" ]; then
	mkdir -p "$ROOT$RELEASE_ROOT/default-media"
	cp -a "$SRC/default-media/." "$ROOT$RELEASE_ROOT/default-media/"
fi

# Licence text in the image (D33).
mkdir -p "$ROOT/usr/share/portapixel"
if [ -f "$SRC/../LICENSES-THIRD-PARTY.md" ]; then
	cp -f "$SRC/../LICENSES-THIRD-PARTY.md" "$ROOT/usr/share/portapixel/"
fi

# ----------------------------------------------------------------- 6. the fstab
# One marked block, so an on-box install keeps the entries of the owner and a
# second run replaces our block instead of adding a copy.
#
# NEVER use x-mount.mkdir here. busybox mount gives the option to the kernel,
# which rejects it, and the mount fails at about three seconds into the boot
# (mountnas lesson). All mount points are made below, by hand.
say "write the fstab block"
FSTAB="$ROOT/etc/fstab"
[ -f "$FSTAB" ] || : >"$FSTAB"
BEGIN='# >>> portapixel >>>'
END='# <<< portapixel <<<'
{
	awk -v b="$BEGIN" -v e="$END" '
		$0 == b { skip = 1 } !skip { print } $0 == e { skip = 0 }
	' "$FSTAB"
	echo "$BEGIN"
	if [ "$IMAGE" = 1 ]; then
		echo "# Partitions are found by label. A label survives a clone of the image"
		echo "# and is the same on both architectures (D53)."
		echo "LABEL=PPROOT   /                ext4   rw,noatime,commit=60      0 1"
		# Pass 0, so fsck does NOT run on PPBOOT. "syslinux --install" writes
		# the boot sector but not its backup copy, and fsck.vfat then reports
		# the difference on every single boot and refuses to fix it. The ESP
		# holds only the loader and the kernel, which a re-flash replaces.
		echo "LABEL=PPBOOT   /boot            vfat   rw,noatime,nofail         0 0"
		echo "# exFAT has no journal. fsck.exfat runs every boot (plan section 5)."
		echo "LABEL=PPMEDIA  $MEDIA_ROOT   exfat  rw,noatime,nofail         0 2"
	elif [ -n "$MEDIA_PARTITION" ]; then
		echo "LABEL=PPMEDIA  $MEDIA_ROOT   exfat  rw,noatime,nofail         0 2"
	fi
	echo "# Logs, temporary files and run state stay in RAM (D2, D35)."
	echo "tmpfs          /tmp             tmpfs  rw,nosuid,nodev,mode=1777,size=64M   0 0"
	echo "tmpfs          /run             tmpfs  rw,nosuid,nodev,mode=0755,size=32M   0 0"
	echo "tmpfs          /var/log         tmpfs  rw,nosuid,nodev,mode=0755,size=32M   0 0"
	echo "# The Chromium profile (--user-data-dir) and cache (--disk-cache-dir)."
	echo "# The cap is what keeps the browser off the flash (D39). 256M, not the"
	echo "# 96M the plan named for WPE WebKit: a Chromium profile is much bigger,"
	echo "# because it also holds the GPU shader cache and the code cache."
	echo "# uid and gid are numbers: busybox mount cannot translate a user name"
	echo "# for the kernel."
	echo "tmpfs          $KIOSK_CACHE  tmpfs  rw,nosuid,nodev,mode=0700,uid=$KIOSK_UID,gid=$KIOSK_GID,size=$KIOSK_CACHE_SIZE  0 0"
	echo "$END"
} >"$FSTAB.new"
mv "$FSTAB.new" "$FSTAB"

say "make the mount points"
mkdir -p "$ROOT/tmp" "$ROOT/run" "$ROOT/var/log" "$ROOT$KIOSK_CACHE" "$ROOT$MEDIA_ROOT"
[ "$IMAGE" = 0 ] || mkdir -p "$ROOT/boot"
chmod 1777 "$ROOT/tmp"

# On-box, format and mount the spare partition now (plan section 5).
if [ -n "$MEDIA_PARTITION" ]; then
	# findfs, not "blkid -s LABEL -o value": the busybox blkid takes no options
	# at all, and the util-linux one is a separate package we do not install.
	if [ "$(findfs LABEL=PPMEDIA 2>/dev/null || true)" != "$MEDIA_PARTITION" ]; then
		say "format $MEDIA_PARTITION as exFAT PPMEDIA"
		mkfs.exfat -L PPMEDIA "$MEDIA_PARTITION"
	fi
	mountpoint -q "$MEDIA_ROOT" || mount "$MEDIA_ROOT"
	mountpoint -q "$MEDIA_ROOT" || die "cannot mount $MEDIA_ROOT"
fi

# -------------------------------------------------------------- 7. the initramfs
# One feature list, set at the top of this file. The same image must boot from a
# USB stick, an SD card, eMMC, SATA or NVMe (D53), and from a QEMU virtio disk.
say "write the mkinitfs feature list"
mkdir -p "$ROOT/etc/mkinitfs"
printf 'features="%s"\n' "$MKINITFS_FEATURES" >"$ROOT/etc/mkinitfs/mkinitfs.conf"

KVER="$(ls "$ROOT/lib/modules" 2>/dev/null | head -n1 || true)"
[ -n "$KVER" ] || die "no kernel found in $ROOT/lib/modules"
# 6.18.52-0-lts -> lts, 6.12.85-0-rpi -> rpi. apk names the file after it.
FLAVOR="${KVER##*-}"
say "build initramfs-$FLAVOR for kernel $KVER"
if [ -n "$ROOT" ]; then
	# -b takes every file from the target tree, so this also works when the
	# target architecture is not the architecture of this host.
	# -P puts the feature files of the TARGET first, so the module list comes
	# from the mkinitfs of the image and not from the one on the build host.
	mkinitfs -b "$ROOT" -c "$ROOT/etc/mkinitfs/mkinitfs.conf" \
		-P "$ROOT/etc/mkinitfs/features.d" \
		-o "$ROOT/boot/initramfs-$FLAVOR" "$KVER"
else
	mkinitfs -c /etc/mkinitfs/mkinitfs.conf -o "/boot/initramfs-$FLAVOR" "$KVER"
fi

# ---------------------------------------------------------------- 8. the console
# The screen belongs to the browser. No getty on tty1, ever (plan section 5).
say "write the inittab"
{
	echo "# PortaPixel inittab. The display is for the cage session only: there"
	echo "# is no getty on tty1."
	echo "# Serial login stays on, because it is the last way in when the network"
	echo "# is down."
	echo "::sysinit:/sbin/openrc sysinit"
	echo "::sysinit:/sbin/openrc boot"
	echo "::wait:/sbin/openrc default"
	if [ "$ARCH" = aarch64 ]; then
		# The UART of a Pi has a different name on each model: ttyS0 on Pi 3
		# and Pi 4, ttyAMA0 on Pi 2, ttyAMA10 on Pi 5. The cmdline ends with
		# console=serial0,115200, which the Pi kernel resolves through the
		# device tree, so /dev/console is the real UART on every model.
		echo "::respawn:/sbin/getty -L 115200 console vt100"
	else
		echo "ttyS0::respawn:/sbin/getty -L 115200 ttyS0 vt100"
	fi
	echo "::ctrlaltdel:/sbin/reboot"
	echo "::shutdown:/sbin/openrc shutdown"
	echo "::shutdown:/sbin/killall5 -9"
} >"$ROOT/etc/inittab"

# ------------------------------------------------------- 9. a default network
# The daemon renders this file from the TOML at every boot (portapixel-net). It
# still needs a good default, because ifupdown-ng fails to parse an empty or
# missing file, "networking" then fails, and OpenRC refuses to start chronyd
# after that. One missing file took the whole network down. Nothing about the
# network may cascade like that (plan 3.3).
if [ ! -s "$ROOT/etc/network/interfaces" ]; then
	say "write a default /etc/network/interfaces"
	mkdir -p "$ROOT/etc/network"
	cat >"$ROOT/etc/network/interfaces" <<'EOF'
# Default only. portapixeld render-net replaces this file from portapixel.toml
# on every boot. It stays here so a box with no config is still reachable.
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet dhcp
EOF
fi

# ---------------------------------------------------------------- 10. the sshd
# Root login with a password is on by design (D23). This is a trusted LAN
# appliance and SSH is the recovery path.
say "configure sshd"
mkdir -p "$ROOT/etc/ssh/sshd_config.d"
cat >"$ROOT/etc/ssh/sshd_config.d/10-portapixel.conf" <<'EOF'
# PortaPixel. The main sshd_config includes this directory.
PermitRootLogin yes
PasswordAuthentication yes
EOF

# ------------------------------------------------------------- 11. the services
# Write the runlevel links by hand. rc-update needs the target OpenRC, which we
# cannot run in a foreign architecture root. A link is idempotent.
rc_add() {
	_svc="$1"; _lvl="$2"
	[ -f "$ROOT/etc/init.d/$_svc" ] || die "no init script for $_svc"
	mkdir -p "$ROOT/etc/runlevels/$_lvl"
	ln -sfn "/etc/init.d/$_svc" "$ROOT/etc/runlevels/$_lvl/$_svc"
}

say "enable the services"
# sysinit: device nodes and drivers.
# LANDMINE: alpine-base also brings the mdev init scripts. Enable udev only.
# Two device managers on one /dev is a fight nobody wins.
for s in devfs dmesg udev udev-trigger hwdrivers; do rc_add "$s" sysinit; done

# boot: kernel settings, host name, logs and the network.
for s in modules sysctl hostname bootmisc syslog networking; do rc_add "$s" boot; done
# portapixel-net renders the network files from the TOML on PPMEDIA, so it needs
# localmount and must run before networking (plan section 7).
rc_add portapixel-net boot
# Exactly one clock service, named on purpose. Both hwclock and swclock
# "provide clock", so leaving it implicit lets OpenRC pick for us.
if [ "$ARCH" = aarch64 ]; then
	# A Pi has no RTC. swclock restores the saved time, so the box never wakes
	# up in 1970 and TLS to the fleet server works before chrony syncs (D40).
	rc_add swclock boot
else
	# A PC has an RTC. chrony still owns real sync.
	rc_add hwclock boot
fi

# default: the rest. seatd must be up before the daemon starts cage.
for s in udev-postmount dbus alsa chronyd sshd seatd; do rc_add "$s" default; done
rc_add portapixel-firstboot default
rc_add portapixeld default

# shutdown: leave the file systems clean.
for s in killprocs mount-ro savecache; do rc_add "$s" shutdown; done

# zram-init is installed but NOT enabled. Only a low tier device gets swap, and
# the daemon decides that at run time (plan section 4).

# --------------------------------------------------------- 12. daemon settings
say "write /etc/conf.d/portapixeld"
mkdir -p "$ROOT/etc/conf.d"
cat >"$ROOT/etc/conf.d/portapixeld" <<EOF
# PortaPixel daemon settings. install.sh writes this file.
# The paths follow ARCHITECTURE section 3.

# Where playlists, media and portapixel.toml live.
PP_MEDIA="$MEDIA_ROOT"
# State on ext4: ops.log, the shadow config, state.json.
PP_STATE="$STATE_DIR"
# Run state, in RAM.
PP_RUN="$RUN_DIR"
# Release directories and the current symlink.
PP_RELEASES="$RELEASE_ROOT"

# The browser account. cage and Chromium run here, never as root (D43).
# Groups: video, input, audio, seat.
PP_KIOSK_USER="kiosk"
PP_KIOSK_UID="$KIOSK_UID"
# The size capped tmpfs for the Chromium profile and cache (D39). Give Chromium
# --user-data-dir and --disk-cache-dir under this path. A tmpfs mount always
# starts empty, so the daemon makes the subdirectories it needs.
PP_KIOSK_CACHE="$KIOSK_CACHE"
# XDG_RUNTIME_DIR for the kiosk session. cage and Wayland need it. /run is a
# tmpfs, so portapixeld makes this directory again on every boot.
PP_KIOSK_RUNTIME="/run/user/$KIOSK_UID"

# Seconds the new release has to write its health marker before the update
# rolls back (plan section 15). health-gate.sh reads this too.
PP_HEALTH_TIMEOUT=120
EOF

# ---------------------------------------------------- 13. identity of the image
say "write /etc/portapixel-release"
cat >"$ROOT/etc/portapixel-release" <<EOF
PORTAPIXEL_VERSION=$VERSION
ALPINE_RELEASE=$ALPINE_RELEASE
ARCH=$ARCH
EOF
echo portapixel >"$ROOT/etc/hostname"

# Every release publishes the exact package set it shipped (D50).
say "write the package manifest"
# shellcheck disable=SC2086
$APK info -v | sort >"$ROOT/usr/share/portapixel/packages.manifest"

say "done. version $VERSION, arch $ARCH, alpine $ALPINE_RELEASE"
