#!/bin/sh
# tests/ci/release-notes.sh -- write the note of a release on the standard output.
#
# The note is the top section of CHANGELOG.md plus the signing state. The
# changelog is the one place that says what changed, so the note is never typed a
# second time.
#
# Usage: release-notes.sh VERSION SIGNED
set -eu

VERSION="${1:?the version}"
SIGNED="${2:?yes or no}"

REPO="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"
CHANGELOG="$REPO/CHANGELOG.md"

printf '## PortaPixel %s\n\n' "$VERSION"

if [ "$SIGNED" = yes ]; then
	cat <<'EOF'
The release binaries carry a minisign signature. A device checks the signature
and the SHA-256 checksum before it swaps a release (D47).

EOF
else
	cat <<'EOF'
**SELF-UPDATE IS OFF FOR THIS RELEASE, BECAUSE NO SIGNING KEY IS SET.**

The binaries of this release have NO minisign signature, and the binaries hold
NO public key. A device from this release cannot install an update: every check
answers that the build holds no key. Set the repository secret
MINISIGN_SECRET_KEY and the repository variable MINISIGN_PUBLIC_KEY, then build
the release again. docs/RELEASING.md gives the commands.

EOF
fi

# The top section of the changelog: the lines after the first "## " heading, up to
# the next one. The BOM of the file is dropped, because it would land in the
# first line of the note.
printf '### Changes\n\n'
sed '1s/^\xef\xbb\xbf//' "$CHANGELOG" | awk '
	/^## / { seen++; if (seen > 1) exit; next }
	seen == 1 { print }
'

cat <<EOF

### The files of this release

| File | What it is |
|---|---|
| \`portapixel-$VERSION-x86_64.img.gz\` | the image for a PC or a thin client (BIOS and UEFI) |
| \`portapixel-$VERSION-aarch64.img.gz\` | the image for a Raspberry Pi |
| \`portapixeld-amd64\`, \`portapixeld-arm64\` | the device daemon. The updater downloads these |
| \`portapixel-server-amd64\`, \`portapixel-server-arm64\` | the fleet server |
| \`SHA256SUMS\` | the checksum of every binary |
| \`portapixel-os-$VERSION.tar.gz\` | the \`os\` directory. Unpack it to install on a stock Alpine box (D51) |
| \`install.sh\` | the installer of that directory, to read before you run it |
| \`portapixel-*-packages.manifest\` | the exact package set of each image (D50) |
| \`portapixel-server-deploy.tar.gz\` | the OpenRC, systemd and compose files |
| \`LICENSES-THIRD-PARTY.md\` | the licences of the software in the image |

The server container image is \`ghcr.io/ethanpil/portapixel-server:$VERSION\`.

Write an image to a USB stick, an SD card, eMMC or an internal disk. The same
image boots from all of them (D53).
EOF
