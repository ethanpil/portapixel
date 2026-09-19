#!/bin/sh
# tests/ci/sign-release.sh -- write SHA256SUMS and the minisign signatures.
#
# The format of SHA256SUMS is what internal/updater readSums parses: a checksum,
# whitespace, and the bare file name. A name with a path in it is ignored by the
# device, so the file is written from inside the directory.
#
# Signing happens only when there is a secret key. A release with no signature is
# still a release: a device that holds no public key cannot install an update at
# all, and the release note says so in capitals. A device that holds a key refuses
# an unsigned release, which is the point of D47.
#
# The key is read from the file that PP_SECRET_KEY_FILE names. This script never
# prints the key and never puts it in an argument.
#
# Environment:
#   PP_SECRET_KEY_FILE   the minisign secret key file, or empty for no signing
#   PP_PUBLIC_KEY        the matching public key, for the check after signing
#   PP_MINISIGN_PASSWORD the password of the key, when it has one
#
# Usage: sign-release.sh DIR
set -eu

DIR="${1:?the directory that holds the binaries}"
KEYFILE="${PP_SECRET_KEY_FILE:-}"
PUBKEY="${PP_PUBLIC_KEY:-}"

say() { printf '==> %s\n' "$*"; }
die() { printf 'sign-release.sh: %s\n' "$*" >&2; exit 1; }

cd "$DIR"

# The four release binaries, in one fixed order, so two builds of one release give
# the same SHA256SUMS byte for byte (D50).
BINARIES="portapixeld-amd64
portapixeld-arm64
portapixel-server-amd64
portapixel-server-arm64"

for b in $BINARIES; do
	[ -f "$b" ] || die "$b is missing"
done

say "write SHA256SUMS"
: >SHA256SUMS
for b in $BINARIES; do
	sha256sum "$b" >>SHA256SUMS
done
cat SHA256SUMS

if [ -z "$KEYFILE" ]; then
	say "NO SIGNATURES: no secret key is configured for this repository."
	say "Devices that hold the public key refuse this release. See docs/RELEASING.md."
	exit 0
fi

[ -f "$KEYFILE" ] || die "the key file $KEYFILE is not there"
[ -n "$PUBKEY" ] || die "there is a secret key and no public key; refusing to sign (D47)"
command -v minisign >/dev/null 2>&1 || die "minisign is not installed"

say "sign every binary"
for b in $BINARIES; do
	# -H makes a PREHASHED signature. internal/sigverify refuses a legacy
	# signature, because a check of one would have to hold the whole binary in
	# the memory of a device with 512 MB.
	if [ -n "${PP_MINISIGN_PASSWORD:-}" ]; then
		printf '%s\n' "$PP_MINISIGN_PASSWORD" |
			minisign -S -H -s "$KEYFILE" -m "$b" >/dev/null ||
			die "minisign could not sign $b"
	else
		minisign -S -H -s "$KEYFILE" -m "$b" </dev/null >/dev/null ||
			die "minisign could not sign $b"
	fi
	[ -f "$b.minisig" ] || die "no $b.minisig"
	printf '    signed: %s\n' "$b"
done

say "verify every signature with the public key"
for b in $BINARIES; do
	minisign -V -P "$PUBKEY" -m "$b" -x "$b.minisig" >/dev/null ||
		die "the signature of $b does not verify with MINISIGN_PUBLIC_KEY.
 The secret key and the public variable are not a pair. Do not publish this run."
	printf '    verified: %s\n' "$b"
done

say "every binary is signed and every signature verifies"
