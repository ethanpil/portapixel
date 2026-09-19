#!/bin/sh
# tests/ci/vanilla-guest.sh -- runs INSIDE the Alpine live ISO.
# It installs stock Alpine to /dev/vda with no PortaPixel in it at all.
# vanilla-install.exp calls it over the 9p share.
set -eux

. /mnt/work/params

ip link set eth0 up
udhcpc -i eth0 -n -q

cat >/etc/apk/repositories <<REPO
https://dl-cdn.alpinelinux.org/alpine/v$(echo "$ALPINE_RELEASE" | cut -d. -f1,2)/main
https://dl-cdn.alpinelinux.org/alpine/v$(echo "$ALPINE_RELEASE" | cut -d. -f1,2)/community
REPO
apk update

echo alpine >/etc/hostname
hostname alpine
cat >/etc/network/interfaces <<NET
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet dhcp
NET
setup-timezone -z UTC || true

# The unattended install. ERASE_DISKS answers the one question that setup-disk
# asks. "-m sys" is the classic install to disk that PortaPixel needs (D1).
ERASE_DISKS=/dev/vda setup-disk -m sys /dev/vda

# Find the new root and give it a serial console login and a root password. The
# test drives the serial line, and the installed system must let it in.
mkdir -p /m
target=""
for p in /dev/vda1 /dev/vda2 /dev/vda3; do
	[ -b "$p" ] || continue
	if mount "$p" /m 2>/dev/null; then
		if [ -f /m/etc/alpine-release ] && [ -d /m/etc/runlevels ]; then
			target="$p"
			break
		fi
		umount /m
	fi
done
[ -n "$target" ] || { echo NO-TARGET-ROOT; exit 1; }
echo "target root is $target"

# Root with no password cannot log in on a serial line. Set the documented
# PortaPixel password, which is what the boot test uses.
chroot /m /bin/sh -c 'echo "root:portapixel" | chpasswd'

# The installer VM has the network. The installed system needs it too, for the
# apk work that install.sh does on the next boot.
cp /etc/network/interfaces /m/etc/network/interfaces
cp /etc/apk/repositories /m/etc/apk/repositories

# Put everything that the next phase needs ON THE DISK. The installed system
# then needs no 9p share at all. The first attempt mounted the share in the live
# ISO and got "Resource busy" on the installed system, and one file system less
# is one fault less. install.sh reads os/ and LICENSES-THIRD-PARTY.md only.
mkdir -p /m/root/pp
cp -a /mnt/repo/os /m/root/pp/
cp -a /mnt/repo/LICENSES-THIRD-PARTY.md /m/root/pp/
cp /mnt/work/portapixeld /m/root/portapixeld
chmod 0755 /m/root/portapixeld
cp /mnt/work/vanilla-onbox.sh /m/root/
cp /mnt/work/params /m/root/

umount /m
sync
echo VANILLA-INSTALL-DONE
