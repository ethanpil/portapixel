#!/bin/sh
# tests/ci/vanilla-onbox.sh -- runs ON the installed stock Alpine system.
# It is the live, on-box install path of D51: no --root, no image, no partitions
# that PortaPixel made.
set -eux

. /mnt/work/params

# 9pnet_virtio holds the share. The repository is under /mnt/repo.
ip link set eth0 up 2>/dev/null || true
udhcpc -i eth0 -n -q 2>/dev/null || true
apk update

# The on-box install. No --root and no --media-partition, so the media directory
# is /var/lib/portapixel/media, which is what install.sh documents.
sh /mnt/repo/os/install.sh \
	--binary /mnt/work/portapixeld \
	--version "$VERSION"

# The daemon needs a browser to show a picture, and this virtual machine has no
# display. The gate after the reboot is the API, not the browser.
rc-update 2>/dev/null | grep -E 'portapixeld|portapixel-net' || true
sync
echo ONBOX-INSTALL-DONE
