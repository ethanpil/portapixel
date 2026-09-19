#!/bin/sh
# tests/ci/image-build.sh -- build one image inside an Alpine container.
#
# The CI job starts an Alpine container of the pinned release with --privileged
# and /dev from the host, because os/build-image.sh needs loop devices. This
# script installs the build tools that Alpine does not have in its base, then
# calls os/build-image.sh, which does all the work (D51).
#
# For the aarch64 image the host must have qemu-user binfmt. Without it the apk
# triggers of the target root fail with "Exec format error" (CONTEXT.md section
# 4). The CI job installs the handlers before it starts this container.
#
# Usage: image-build.sh ARCH ALPINE_RELEASE VERSION BINARY OUTDIR
set -eu

ARCH="${1:?arch}"
ALPINE_RELEASE="${2:?alpine release}"
VERSION="${3:?version}"
BINARY="${4:?portapixeld binary}"
OUTDIR="${5:?output directory}"

REPO="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"

say() { printf '==> %s\n' "$*"; }
die() { printf 'image-build.sh: %s\n' "$*" >&2; exit 1; }

case "$ARCH" in x86_64|aarch64) ;; *) die "ARCH must be x86_64 or aarch64" ;; esac
[ -f "$BINARY" ] || die "$BINARY does not exist"

# The tools that os/build-image.sh asks for. losetup is a package of its own:
# the busybox one cannot limit the size, which the fallback path needs.
# alpine-keys carries /usr/share/apk/keys/<arch>, and apk-tools 3 refuses the
# index of another architecture without those keys.
PKGS="gptfdisk util-linux losetup e2fsprogs dosfstools exfatprogs mkinitfs cpio alpine-keys"
if [ "$ARCH" = x86_64 ]; then
	PKGS="$PKGS syslinux grub grub-efi"
fi
say "install the build tools: $PKGS"
# shellcheck disable=SC2086  # $PKGS is a deliberate word list
apk add --no-cache $PKGS >/dev/null

[ -e /dev/loop-control ] || die \
	"the container has no /dev/loop-control. The job must run the container with
 --privileged and with /dev from the host."

if [ "$ARCH" = aarch64 ]; then
	# Prove binfmt before the build, not 20 minutes into it. A static aarch64
	# binary from the target root is the only true test, so use the one that apk
	# will run: there is none yet, so ask the kernel instead.
	if [ -r /proc/sys/fs/binfmt_misc/qemu-aarch64 ]; then
		say "binfmt: qemu-aarch64 is registered"
	else
		say "WARNING: /proc/sys/fs/binfmt_misc/qemu-aarch64 is not readable here."
		say "         The registration is in the kernel of the host and can still work."
	fi
fi

mkdir -p "$OUTDIR"
OUT="$OUTDIR/portapixel-$VERSION-$ARCH.img"

say "build $OUT"
sh "$REPO/os/build-image.sh" \
	--arch "$ARCH" \
	--alpine-release "$ALPINE_RELEASE" \
	--version "$VERSION" \
	--binary "$BINARY" \
	--out "$OUT"

say "the output of the build"
ls -l "$OUTDIR"
[ -f "$OUT.gz" ] || die "no $OUT.gz"
[ -s "$OUTDIR/portapixel-$VERSION-$ARCH-packages.manifest" ] ||
	die "no package manifest beside the image (D50)"
say "packages in the image: $(wc -l <"$OUTDIR/portapixel-$VERSION-$ARCH-packages.manifest" | tr -d ' ')"
