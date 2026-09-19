#!/bin/sh
# ci-lint.sh -- lint every shipped shell script and the Go module.
# Run it from any directory.
#
# The file list is FOUND, not kept by hand, so a new script can never ship
# unlinted (mountnas discipline). Two mechanisms:
#   1. a shebang on line 1: "#!/bin/sh" or "#!/sbin/openrc-run"
#   2. explicit globs for files that are always sourced and therefore carry no
#      shebang at all: os/overlay/etc/conf.d/*
#
# Line 1 only, never a search through the whole file: a document that quotes a
# shebang inside a code example would otherwise make shellcheck try to parse
# HTML (mountnas lesson).
#
# -s sh: every script must parse as POSIX sh, because the real shell on the
# device is busybox ash, which is stricter than bash.
# Excluded checks, each a verified safe busybox ash idiom or a false positive:
#   SC1091 sourced file is not there at lint time (/etc/conf.d, /usr/libexec)
#   SC2034 OpenRC reads variables like "description" itself; they look unused
#   SC3043 "local" works in busybox ash
#   SC3045 "read -s" works in busybox ash
#   SC3033 a function name with a hyphen works in busybox ash
set -eu
cd "$(dirname "$0")/.."

fail=0

# ------------------------------------------------------------------ shellcheck
if ! command -v shellcheck >/dev/null 2>&1; then
	echo "FAIL: shellcheck is not installed"
	exit 1
fi

# The roots to search. "deploy" belongs here: deploy/portapixel-server.initd is a
# shipped OpenRC script, and it was the one shell file in the repository that no
# gate ever read. It carried four real faults that this list now finds.
ROOTS="os scripts tests deploy"

# `|| :` so a file that does not match cannot fail the $() under set -e.
# shellcheck disable=SC2086  # $ROOTS is a deliberate word list of directories
files=$(find $ROOTS -type f | while IFS= read -r f; do
	head -n1 "$f" | grep -qE '^#!/(bin/sh|sbin/openrc-run)' && printf '%s\n' "$f" || :
done)
[ -n "$files" ] || { echo "FAIL: shebang discovery found no scripts"; exit 1; }

# A shipped script with a shebang we do not expect is a gap in the gate, not a
# file to pass over. bash and ash are both wrong here: the device has busybox ash
# and every script must parse as POSIX sh.
# shellcheck disable=SC2086
badshebang=$(find $ROOTS -type f | while IFS= read -r f; do
	head -n1 "$f" | grep -qE '^#!.*(bash|/bin/ash|env +sh)' && printf '%s\n' "$f" || :
done)
if [ -n "$badshebang" ]; then
	echo "FAIL: these scripts must use #!/bin/sh, not bash or ash:"
	echo "$badshebang"
	fail=1
fi

# shellcheck disable=SC2086  # $files is a newline list of repository paths
shellcheck -s sh -S warning -e SC1091,SC2034,SC3043,SC3045,SC3033 \
	$files os/overlay/etc/conf.d/* deploy/portapixel-server.confd ||
	fail=1
# shellcheck disable=SC2086
echo "shellcheck: $(printf '%s\n' $files | wc -l | tr -d ' ') shebang scripts + conf.d"

# --------------------------------------------------------------- shell parsing
# The linter understands the syntax, but only the real shell proves that a file
# parses. Use the shell that is on the device when we have it.
# (Do not start this comment with the linter's own name: it reads the next word
# as a directive.)
sh_bin="$(command -v busybox >/dev/null 2>&1 && echo "busybox sh" || echo sh)"
# shellcheck disable=SC2086
for f in $files; do
	$sh_bin -n "$f" || { echo "FAIL: $f does not parse"; fail=1; }
done
echo "parse check: all scripts parse as sh"

# ------------------------------------------------------------- CRLF line ends
# These files run on Alpine. A carriage return breaks a shebang and an OpenRC
# script in ways that are hard to read in an error message.
CR="$(printf '\r')"   # $'\r' is a bash idiom, not POSIX sh
# Image and font data can hold the byte 0x0D. busybox grep has no -I option that
# works, so the file name decides which files are binary.
BINARY='\.(jpg|jpeg|png|gif|ico|woff2|ppm|pcap|mp4|webm|gz|img|qcow2|zip|pdf|ttf|otf)$'
# shellcheck disable=SC2086  # $ROOTS is a deliberate word list of directories
crlf="$(grep -lr "$CR" $ROOTS 2>/dev/null | grep -vE "$BINARY" || true)"
if [ -n "$crlf" ]; then
	echo "FAIL: CRLF line ends found:"
	echo "$crlf"
	fail=1
else
	echo "line ends: LF everywhere under $ROOTS"
fi

# ----------------------------------------------------- the package list format
# packages.list is the only source of the package set. Guard its format so a
# line with no rationale comment, or with an unknown tag, cannot slip in.
# A line can carry more than one tag, for example "@x86_64 @image linux-lts". Read
# every leading tag, and a package name must come after them.
awk '
	/^[ \t]*(#|$)/ { next }
	{
		if ($0 !~ /#/) { printf "FAIL: packages.list:%d: no rationale comment: %s\n", FNR, $0; bad = 1 }
		n = 1
		while (n <= NF && substr($n, 1, 1) == "@") {
			if ($n != "@x86_64" && $n != "@aarch64" && $n != "@image") {
				printf "FAIL: packages.list:%d: unknown tag %s\n", FNR, $n; bad = 1
			}
			n++
		}
		if (n > NF || substr($n, 1, 1) == "#") {
			printf "FAIL: packages.list:%d: tags with no package name: %s\n", FNR, $0; bad = 1
		}
	}
	END { exit bad + 0 }
' os/packages.list || fail=1
echo "packages.list: every line has a tag we know and a rationale"

# -------------------------------------------------------------------- Go module
if command -v go >/dev/null 2>&1; then
	out="$(gofmt -l . || true)"
	if [ -n "$out" ]; then
		echo "FAIL: gofmt wants to change these files:"
		echo "$out"
		fail=1
	else
		echo "gofmt: clean"
	fi
	if go vet ./... 2>&1; then
		echo "go vet: clean"
	else
		echo "FAIL: go vet found problems"
		fail=1
	fi
else
	echo "SKIP: go is not installed, so gofmt and go vet did not run."
	echo "      The CI lint job must have Go, so this line must never appear there."
fi

[ "$fail" = 0 ] || exit 1
echo "ci-lint: all checks pass"
