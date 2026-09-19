#!/bin/sh
# shot.sh -- write a screenshot of the pp-client display to a PNG file.
# Usage: shot.sh FILE.png
set -eu
. "$(dirname "$0")/lab.env"

OUT="${1:?usage: shot.sh FILE.png}"
case "$OUT" in
/*) ;;
*) OUT="$PWD/$OUT" ;;
esac
running "$CLI_DIR/qemu.pid" || die "$CLI_NAME does not run"

PPM="$CLI_DIR/shot.ppm"
rm -f "$PPM"
# The monitor writes from the working directory of QEMU, so the path is absolute.
# PPM, not PNG: not every QEMU build has the PNG writer.
mon "$CLI_DIR/monitor.sock" "screendump $PPM" >/dev/null
i=0
while [ "$i" -lt 10 ]; do
	[ -s "$PPM" ] && break
	sleep 1
	i=$((i + 1))
done
[ -s "$PPM" ] || die "the monitor wrote no screenshot"
case "$OUT" in
*.png)
	command -v pnmtopng >/dev/null 2>&1 || die "pnmtopng is missing; apk add netpbm"
	pnmtopng "$PPM" >"$OUT" 2>/dev/null || die "cannot convert the screenshot"
	;;
*) cp "$PPM" "$OUT" ;;
esac
say "$OUT"
ls -l "$OUT"
