#!/bin/sh
# tests/ci/vanilla-install.sh -- prove the second install path (D51).
#
# It installs a stock Alpine of the pinned release in QEMU, then runs
# os/install.sh on that live system as root, then reboots and asks the box for
# /api/status. The image build runs the same install.sh, so a pass here also says
# that the configuration work of the image is sound.
#
# Two virtual machines run one after the other:
#   1. the Alpine live ISO, which installs Alpine to the disk (setup-disk -m sys),
#   2. that installed Alpine, which runs os/install.sh and then reboots.
#
# It needs KVM. Under TCG the two installs take hours.
set -eu

ALPINE_RELEASE=""
VERSION="0.0.0-dev"
BINARY=""
PORT=18081
MEM=3072
DISK_GB=8

die() { printf 'vanilla-install: %s\n' "$*" >&2; exit 1; }
say() { printf '==> %s\n' "$*"; }

usage() {
	cat <<'EOF'
Usage: vanilla-install.sh --alpine-release X.Y.Z [options]

  --alpine-release X.Y.Z  exact Alpine release for the guest
  --version VER           release version to install
  --binary PATH           portapixeld binary to install
  --port N                host port that forwards to guest port 80
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--alpine-release) ALPINE_RELEASE="${2:?}"; shift 2 ;;
	--version) VERSION="${2:?}"; shift 2 ;;
	--binary) BINARY="${2:?}"; shift 2 ;;
	--port) PORT="${2:?}"; shift 2 ;;
	--mem) MEM="${2:?}"; shift 2 ;;
	-h|--help) usage; exit 0 ;;
	*) usage >&2; die "unknown option: $1" ;;
	esac
done

case "$ALPINE_RELEASE" in
[0-9]*.[0-9]*.[0-9]*) ;;
*) die "--alpine-release must be an exact release like 3.23.2" ;;
esac
[ -n "$BINARY" ] || die "--binary is needed"
[ -f "$BINARY" ] || die "$BINARY does not exist"

for t in qemu-system-x86_64 qemu-img expect wget curl; do
	command -v "$t" >/dev/null 2>&1 || die "$t is missing"
done
[ -e /dev/kvm ] || die "no /dev/kvm: two Alpine installs under TCG are too slow"

REPO="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"
BRANCH="v$(echo "$ALPINE_RELEASE" | cut -d. -f1,2)"
ISO="alpine-virt-$ALPINE_RELEASE-x86_64.iso"
CACHE="${PP_ISO_CACHE:-$HOME/.cache/portapixel}"
mkdir -p "$CACHE"

if [ ! -f "$CACHE/$ISO" ]; then
	say "fetch $ISO"
	wget -q -O "$CACHE/$ISO.part" \
		"https://dl-cdn.alpinelinux.org/alpine/$BRANCH/releases/x86_64/$ISO" ||
		die "cannot fetch $ISO"
	mv "$CACHE/$ISO.part" "$CACHE/$ISO"
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT INT TERM

# The 9p share. The guest reads the whole checkout from it, so install.sh runs
# from the repository and not from a copy that could differ.
mkdir -p "$WORK/share"
cp "$REPO/tests/ci/vanilla-guest.sh" "$WORK/share/"
cp "$REPO/tests/ci/vanilla-onbox.sh" "$WORK/share/"
cp "$BINARY" "$WORK/share/portapixeld"
printf 'ALPINE_RELEASE=%s\nVERSION=%s\n' "$ALPINE_RELEASE" "$VERSION" >"$WORK/share/params"

say "make a ${DISK_GB}G disk"
qemu-img create -f qcow2 "$WORK/disk.qcow2" "${DISK_GB}G" >/dev/null

say "1. install stock Alpine $ALPINE_RELEASE from the live ISO"
expect "$REPO/tests/ci/vanilla-install.exp" install \
	"$CACHE/$ISO" "$WORK/disk.qcow2" "$WORK/share" "$REPO" "$MEM" "$PORT" ||
	die "the Alpine install failed"

say "2. run os/install.sh on that system, reboot, and ask for /api/status"
expect "$REPO/tests/ci/vanilla-install.exp" onbox \
	"$CACHE/$ISO" "$WORK/disk.qcow2" "$WORK/share" "$REPO" "$MEM" "$PORT" ||
	die "the on-box install or the check after the reboot failed"

say "PASS: os/install.sh puts PortaPixel on a stock Alpine box (D51)"
