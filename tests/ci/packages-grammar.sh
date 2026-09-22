#!/bin/sh
# tests/ci/packages-grammar.sh -- prove os/packages-read.awk.
#
# install.sh and scripts/ci-lint.sh read os/packages.list with that one awk file.
# A reader of a list must be proved with lines that the real list does not hold,
# so the sample lines are here.
#
# The case that made this file necessary: "@x86_64 @aarch64 pkg". The header of
# packages.list says that several architecture tags are an OR set. The first
# reader cleared its "arch is ok" flag on every tag that was not the current
# architecture, so such a line installed on NEITHER architecture and the lint
# called it good.
set -eu

cd "$(dirname "$0")/../.."
AWK=os/packages-read.awk
[ -f "$AWK" ] || { echo "FAIL: no $AWK"; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT INT TERM
LIST="$WORK/list"

cat >"$LIST" <<'EOF'
# a comment line
plain                      # every architecture, every mode
@x86_64 onlypc             # one architecture
@aarch64 onlypi            # the other one
@x86_64 @aarch64 bothtags  # an OR set: both architectures
@image kernelish           # an image build only
@x86_64 @image pckernel    # two tags of different kinds
two names                  # a line with two package names
@image @aarch64 pitwo pi3  # tags in the other order, two names
EOF

fail=0
check() {
	_what="$1"; _want="$2"; _got="$3"
	if [ "$_got" = "$_want" ]; then
		printf '    ok: %s\n' "$_what"
	else
		printf 'FAIL: %s\n  want: %s\n  got:  %s\n' "$_what" "$_want" "$_got"
		fail=1
	fi
}

read_set() { # arch onbox want
	awk -v arch="$1" -v onbox="$2" -v want="$3" -f "$AWK" "$LIST" | tr '\n' ' ' |
		sed 's/ *$//'
}

check "x86_64, image build, keep" \
	"plain onlypc bothtags kernelish pckernel two names" \
	"$(read_set x86_64 0 keep)"

check "aarch64, image build, keep" \
	"plain onlypi bothtags kernelish two names pitwo pi3" \
	"$(read_set aarch64 0 keep)"

check "x86_64, on-box, keep" \
	"plain onlypc bothtags two names" \
	"$(read_set x86_64 1 keep)"

check "x86_64, on-box, skip" \
	"kernelish pckernel" \
	"$(read_set x86_64 1 skip)"

check "aarch64, on-box, skip" \
	"kernelish pitwo pi3" \
	"$(read_set aarch64 1 skip)"

# The check mode, which the lint reads.
printf '@nosuchtag pkg  # a tag nobody knows\n@image  # tags and no name\n' >"$LIST.bad"
check "an unknown tag is BAD" \
	"BAD unknown tag @nosuchtag BAD the line has tags and no package name" \
	"$(awk -v arch=x86_64 -v onbox=0 -v want=check -f "$AWK" "$LIST.bad" | tr '\n' ' ' | sed 's/ *$//')"

# The real list must parse with no complaint, on both architectures.
bad="$(awk -v arch=x86_64 -v onbox=0 -v want=check -f "$AWK" os/packages.list |
	grep '^BAD ' || true)"
check "os/packages.list holds no bad line" "" "$bad"

[ "$fail" = 0 ] || { echo "packages-grammar.sh: FAILED"; exit 1; }
echo "packages-grammar.sh: the one reader of packages.list is correct"
