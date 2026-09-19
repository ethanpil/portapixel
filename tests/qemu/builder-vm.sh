#!/bin/sh
# tests/qemu/builder-vm.sh -- build an image in a throwaway Alpine VM.
#
# os/build-image.sh needs loop devices. An unprivileged LXC container has none,
# so this script starts a short lived Alpine VM under KVM, shares the checkout
# into it over 9p, runs os/build-image.sh there, and takes the .img.gz back out.
#
# Nothing here survives the run: no bridge, no long lived VM, no state.
set -eu

ARCH="$(uname -m)"
VERSION="0.0.0-dev"
ALPINE_RELEASE=""
BINARY=""
OUTDIR="$PWD/out"
MEM=3072
CPUS=4
SCRATCH_GB=12

die() { printf 'builder-vm: %s\n' "$*" >&2; exit 1; }
say() { printf '==> %s\n' "$*"; }

usage() {
	cat <<'EOF'
Usage: builder-vm.sh --alpine-release X.Y.Z [options]

  --arch ARCH              target architecture for the image (default: the host)
  --alpine-release X.Y.Z   exact Alpine release for BOTH the builder VM and the
                           image
  --version VER            release version for the image
  --binary PATH            portapixeld binary to put in the image
  --out DIR                where the .img.gz lands (default: ./out)
  --mem MB / --cpus N      builder VM size
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--arch) ARCH="${2:?}"; shift 2 ;;
	--alpine-release) ALPINE_RELEASE="${2:?}"; shift 2 ;;
	--version) VERSION="${2:?}"; shift 2 ;;
	--binary) BINARY="${2:?}"; shift 2 ;;
	--out) OUTDIR="${2:?}"; shift 2 ;;
	--mem) MEM="${2:?}"; shift 2 ;;
	--cpus) CPUS="${2:?}"; shift 2 ;;
	-h|--help) usage; exit 0 ;;
	*) usage >&2; die "unknown option: $1" ;;
	esac
done

case "$ALPINE_RELEASE" in
[0-9]*.[0-9]*.[0-9]*) ;;
*) die "--alpine-release must be an exact release like 3.23.2" ;;
esac

for t in qemu-system-x86_64 qemu-img expect wget; do
	command -v "$t" >/dev/null 2>&1 || die "$t is missing"
done
[ -e /dev/kvm ] || die "no /dev/kvm: the builder VM under TCG is too slow to be useful"

# A VM bigger than the free memory of the host gets killed part way through the
# build, and the only sign is that QEMU stops. Say so before we start.
FREE_MB="$(awk '/MemAvailable/ { print int($2 / 1024) }' /proc/meminfo 2>/dev/null || echo 0)"
if [ "$FREE_MB" -gt 0 ] && [ "$FREE_MB" -lt "$MEM" ]; then
	die "the host has ${FREE_MB} MB free but --mem is ${MEM} MB.
 Lower --mem or free memory first. The live builder keeps its root in a tmpfs of
 half the VM memory, so about 2000 MB is the floor."
fi

REPO="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"
BRANCH="v$(echo "$ALPINE_RELEASE" | cut -d. -f1,2)"
# The virt flavour is built for a serial console, which is what we drive.
ISO="alpine-virt-$ALPINE_RELEASE-x86_64.iso"
CACHE="${PP_ISO_CACHE:-$HOME/.cache/portapixel}"
mkdir -p "$CACHE" "$OUTDIR"

if [ ! -f "$CACHE/$ISO" ]; then
	say "fetch $ISO"
	wget -q -O "$CACHE/$ISO.part" \
		"https://dl-cdn.alpinelinux.org/alpine/$BRANCH/releases/x86_64/$ISO" ||
		die "cannot fetch $ISO"
	mv "$CACHE/$ISO.part" "$CACHE/$ISO"
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT INT TERM
BINNAME=""
if [ -n "$BINARY" ]; then
	[ -f "$BINARY" ] || die "--binary $BINARY does not exist"
	BINNAME="$(basename "$BINARY")"
	cp "$BINARY" "$WORK/$BINNAME"
fi

say "make a ${SCRATCH_GB}G scratch disk"
qemu-img create -f qcow2 "$WORK/scratch.qcow2" "${SCRATCH_GB}G" >/dev/null

say "start the builder VM (alpine $ALPINE_RELEASE, ${MEM}M, $CPUS cpus)"
expect "$(dirname "$0")/builder-vm.exp" \
	"$CACHE/$ISO" "$WORK/scratch.qcow2" "$REPO" "$WORK" \
	"$MEM" "$CPUS" "$ARCH" "$VERSION" "$ALPINE_RELEASE" "$BINNAME" ||
	die "the builder VM failed; read the log above"

say "collect the result"
found=0
for f in "$WORK"/*.img.gz "$WORK"/*packages.manifest; do
	[ -f "$f" ] || continue
	cp "$f" "$OUTDIR/"
	found=1
done
[ "$found" = 1 ] || die "the VM produced no image"
ls -l "$OUTDIR"
say "done"
