#!/bin/sh
# tests/ci/vanilla-onbox.sh -- runs ON the installed stock Alpine system.
# It is the live, on-box install path of D51: no --root, no image, no partitions
# that PortaPixel made.
set -eux

# vanilla-guest.sh put the os/ tree, the binary and this file under /root, so
# this phase needs no 9p share.
. /root/params

ip link set eth0 up 2>/dev/null || true
udhcpc -i eth0 -n -q 2>/dev/null || true
apk update

# The on-box install. No --root and no --media-partition, so the media directory
# is /var/lib/portapixel/media, which is what install.sh documents.
sh /root/pp/os/install.sh \
	--binary /root/portapixeld \
	--version "$VERSION"

# The player package must install on-box too. This virtual machine has no
# display, so mpv cannot show a picture here, and the gate after the reboot is
# the API, not the player.
command -v mpv
mpv --version | head -n 1
rc-update 2>/dev/null | grep -E 'portapixeld|portapixel-net' || true
sync
echo ONBOX-INSTALL-DONE
