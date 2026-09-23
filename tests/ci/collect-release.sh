#!/bin/sh
# tests/ci/collect-release.sh -- put every deliverable of a release in one place.
#
# The release step uploads DIR/publish/* and nothing else, so this script is the
# one list of what a release holds (plan section 17 item 8). A file that is missing
# stops the release here, with the name of the file, instead of at the upload.
#
# Usage: collect-release.sh DIR VERSION SIGNED
#   DIR      holds the binaries, the images and the manifests
#   VERSION  the release version, for example 0.1.0
#   SIGNED   "yes" when CI made minisign signatures
set -eu

DIR="${1:?the directory of the build output}"
VERSION="${2:?the version}"
SIGNED="${3:?yes or no}"

REPO="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"

say() { printf '==> %s\n' "$*"; }
die() { printf 'collect-release.sh: %s\n' "$*" >&2; exit 1; }

PUB="$DIR/publish"
rm -rf "$PUB"
mkdir -p "$PUB"

take() {
	[ -f "$1" ] || die "$1 is missing"
	cp "$1" "$PUB/"
	printf '    %s\n' "$(basename "$1")"
}

say "the images and their package manifests (D50)"
for arch in x86_64 aarch64; do
	take "$DIR/portapixel-$VERSION-$arch.img.gz"
	take "$DIR/portapixel-$VERSION-$arch-packages.manifest"
done

say "the binaries and the checksum file"
BINARIES="portapixeld-amd64
portapixeld-arm64
portapixel-server-amd64
portapixel-server-arm64"
for b in $BINARIES; do
	take "$DIR/$b"
done
take "$DIR/SHA256SUMS"

if [ "$SIGNED" = yes ]; then
	say "the signatures"
	for b in $BINARIES; do
		take "$DIR/$b.minisig"
	done
else
	say "NO SIGNATURES in this release"
fi

say "the installer and the licences"
take "$REPO/os/install.sh"
take "$REPO/LICENSES-THIRD-PARTY.md"

# install.sh ALONE cannot install anything. It reads os/packages.list with
# os/packages-read.awk, it copies os/overlay into the target, and it takes the
# demo videos (about 59 MB) from os/default-media. The release published the one file and
# docs/install.md told a person to run it, so the documented on-box path stopped
# at "cannot find .../packages.list". Publish the whole directory beside it.
say "the os directory that install.sh needs"
OSTAR="$PUB/portapixel-os-$VERSION.tar.gz"
tar -czf "$OSTAR" -C "$REPO" os LICENSES-THIRD-PARTY.md
printf '    %s\n' "$(basename "$OSTAR")"

# The service files of the server, as one small archive. They are four text files
# that only a person who installs the server needs.
say "the deployment files of the server"
TAR="$PUB/portapixel-server-deploy.tar.gz"
tar -czf "$TAR" -C "$REPO" \
	deploy/portapixel-server.initd \
	deploy/portapixel-server.confd \
	deploy/portapixel-server.service \
	deploy/docker-compose.yml \
	deploy/README.md
printf '    %s\n' "$(basename "$TAR")"

say "the release holds these files"
ls -l "$PUB"
