#!/bin/sh
# Ops log helper for the shell side of PortaPixel. Source it, then call oplog.
#
# One line format everywhere (ARCHITECTURE section 3, D35):
#   <UTC RFC3339>\t<event>\t<details>
# The file goes straight to ext4, so it survives a power cut with no commit
# step. The write is best effort: a log line must never stop the work.
# It self trims: past 1200 lines it keeps the last 1000.

# Read the paths the installer wrote. Fall back to the documented defaults, so
# the script also works when somebody runs it by hand.
[ -f /etc/conf.d/portapixeld ] && . /etc/conf.d/portapixeld
PP_STATE="${PP_STATE:-/var/lib/portapixel}"
PP_MEDIA="${PP_MEDIA:-/media/ppmedia}"
PP_RELEASES="${PP_RELEASES:-/opt/portapixel}"
PP_RUN="${PP_RUN:-/run/portapixel}"
OPSLOG="$PP_STATE/ops.log"

oplog() {
	_ev="$1"; shift
	printf '%s\t%s\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$_ev" "$*" \
		>>"$OPSLOG" 2>/dev/null || return 0
	# Trim only now and then, so the cost is spread over about 200 lines.
	_n="$(wc -l <"$OPSLOG" 2>/dev/null || echo 0)"
	if [ "$_n" -gt 1200 ] 2>/dev/null; then
		tail -n 1000 "$OPSLOG" >"$OPSLOG.new" 2>/dev/null &&
			mv "$OPSLOG.new" "$OPSLOG" 2>/dev/null
	fi
	return 0
}
