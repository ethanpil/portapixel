#!/bin/sh
# builder-guest.sh -- runs INSIDE the throwaway Alpine builder VM.
# builder-vm.sh starts the VM and calls this file. Do not run it on a
# workstation: it formats the second disk without asking.
#
# Why a VM at all: os/build-image.sh needs loop devices, and an unprivileged
# LXC container has none.
set -eu

ARCH="${1:?arch}"
VERSION="${2:?version}"
ALPINE_RELEASE="${3:?alpine release}"
BINARY="${4:-}"

REPO=/mnt/repo          # 9p share: the git checkout, read only in practice
WORK=/mnt/work          # 9p share: where the result goes back to the host
SCRATCH=/mnt/scratch    # the second virtio disk: real block device for the loop
BRANCH="v$(echo "$ALPINE_RELEASE" | cut -d. -f1,2)"

say() { printf '\n[guest] %s\n' "$*"; }

say "bring up the network"
# The Alpine live image starts with no address. QEMU user mode networking gives
# 10.0.2.15 by DHCP, with the gateway at 10.0.2.2 and the DNS at 10.0.2.3.
ip link set eth0 up 2>/dev/null || true
udhcpc -i eth0 -n -q >/dev/null 2>&1 || true
if ! grep -q nameserver /etc/resolv.conf 2>/dev/null; then
	echo "nameserver 10.0.2.3" >/etc/resolv.conf
fi
ip addr show eth0 | grep "inet " || true

say "pin the repositories to $BRANCH"
cat >/etc/apk/repositories <<EOF
https://dl-cdn.alpinelinux.org/alpine/$BRANCH/main
https://dl-cdn.alpinelinux.org/alpine/$BRANCH/community
EOF
apk update

say "install the build tools"
# alpine-keys brings /usr/share/apk/keys/<arch>/, which install.sh needs to
# verify the index of a foreign architecture.
apk add --no-progress sgdisk gptfdisk e2fsprogs exfatprogs dosfstools \
	mkinitfs parted partx sfdisk losetup cpio alpine-keys
if [ "$ARCH" = x86_64 ]; then
	apk add --no-progress syslinux grub grub-efi
fi

say "make sure the file system drivers are there"
# The Alpine live system loads a module only when something asks for it, and
# mkfs does not ask: it writes to the block device. Without these, the mount of
# the new file system fails with "Invalid argument".
for m in loop ext4 vfat exfat; do modprobe "$m" 2>/dev/null || true; done
[ -e /dev/loop-control ] || { echo "[guest] FAIL: no loop devices"; exit 1; }

say "format and mount the scratch disk"
# The live system runs from the ISO, which is /dev/sr0, so the FIRST virtio disk
# is our scratch disk. Do not write /dev/vdb here: the name depends on how many
# drives QEMU got.
SDEV=""
for d in /dev/vda /dev/vdb /dev/vdc; do
	[ -b "$d" ] && { SDEV="$d"; break; }
done
[ -n "$SDEV" ] || { echo "[guest] FAIL: no virtio scratch disk"; exit 1; }
say "the scratch disk is $SDEV"
mkdir -p "$SCRATCH"
mkfs.ext4 -q -F -L PPSCRATCH "$SDEV"
# -t ext4, never a guess: busybox mount says "Invalid argument" when the kernel
# does not know the type yet.
mount -t ext4 "$SDEV" "$SCRATCH"

say "build the image"
set -- --arch "$ARCH" --version "$VERSION" --alpine-release "$ALPINE_RELEASE" \
	--out "$SCRATCH/portapixel-$VERSION-$ARCH.img"
if [ -n "$BINARY" ] && [ -f "$WORK/$BINARY" ]; then
	set -- "$@" --binary "$WORK/$BINARY"
fi
sh "$REPO/os/build-image.sh" "$@"

say "copy the result to the host share"
cp "$SCRATCH"/*.img.gz "$WORK/"
cp "$SCRATCH"/*packages.manifest "$WORK/" 2>/dev/null || true
sync
ls -l "$WORK"
say "build finished"
