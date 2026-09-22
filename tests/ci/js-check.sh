#!/bin/sh
# tests/ci/js-check.sh -- the web asset gate.
#
# It proves two things:
#   1. every JavaScript file parses as a module,
#   2. no web file asks for a file from another host.
#
# Why a copy with the .mjs extension: "node --check" reads a .js file as a
# script, and a script permits a duplicate identifier at the top level. A module
# does not. One duplicate identifier in player.js gave a black screen while
# /api/status still looked good (CONTEXT.md section 4). The device loads every
# file as a module, so the gate must read them the same way.
#
# A device is often on a closed network. A page that asks a content network for a
# stylesheet or a font shows nothing there, and the fault is hard to see in a
# test on a desktop with internet. The second check looks for the shapes that
# LOAD something: src=, href=, url() and @import with a host in them, and the
# names of the well known content networks.
set -eu

cd "$(dirname "$0")/../.."

command -v node >/dev/null 2>&1 || { echo "FAIL: node is not installed"; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT INT TERM
: >"$WORK/bad"

# ------------------------------------------------------------------ 1. the parse
# A pipe into "while" makes a subshell, so a variable set inside it is lost.
# The faults go into a file instead.
count=0
find web -name '*.js' | sort >"$WORK/list"
while IFS= read -r f; do
	# The copy name keeps the path, so an error message names the real file.
	out="$WORK/$(printf '%s' "$f" | tr '/' '_').mjs"
	cp "$f" "$out"
	if ! node --check "$out" 2>"$WORK/err"; then
		echo "FAIL: $f does not parse as a module:"
		sed "s|$out|$f|g" "$WORK/err"
		echo "$f" >>"$WORK/bad"
	fi
done <"$WORK/list"
count="$(wc -l <"$WORK/list" | tr -d ' ')"
[ "$count" -gt 0 ] || { echo "FAIL: found no JavaScript under web/"; exit 1; }
echo "node --check: $count modules read as modules"

# --------------------------------------------------------------- 2. remote loads
# A placeholder in an input field and a comment are text, not a load. Only these
# shapes fetch bytes from another host.
grep -rnE \
	-e '(src|href)[[:space:]]*=[[:space:]]*.?(https?:)?//' \
	-e 'url\([[:space:]]*.?(https?:)?//' \
	-e '@import[[:space:]]+.?(https?:)?//' \
	-e '(googleapis|gstatic|jsdelivr|unpkg|cdnjs|bootstrapcdn|fontawesome|cloudflare)\.' \
	--include='*.html' --include='*.css' --include='*.js' --include='*.svg' \
	web >"$WORK/hits" 2>/dev/null || true
# The SVG namespace is a name and not an address: nothing is fetched from it.
grep -v 'www\.w3\.org/2000/svg' <"$WORK/hits" >"$WORK/hits2" || true
if [ -s "$WORK/hits2" ]; then
	echo "FAIL: a web file asks another host for a file. A device on a closed"
	echo "      network cannot get it (D33, plan section 10):"
	cat "$WORK/hits2"
	echo remote >>"$WORK/bad"
else
	echo "remote loads: none; every asset is local"
fi

# ------------------------------------------------------------- 3. inline scripts
# Nothing under web/ may hold a script inside the page, and nothing may hold an
# event handler attribute. Each answer of the two admin UIs and of the player
# carries Content-Security-Policy with script-src 'self', which blocks both. A
# page with an inline script then shows nothing and says why only in the console
# of the browser. web/shared/gallery.html was such a page: it was in both
# binaries, reachable at /shared/gallery.html, and dead. It is now
# tests/gallery/gallery.html, where a developer runs it through the devserver.
#
# An index.html may only LOAD a module: <script type="module" src="...">.
grep -rnE '<script(>|[^>]*>[[:space:]]*[^<[:space:]])' --include='*.html' web \
	>"$WORK/inline" 2>/dev/null || true
grep -rnE '<[^>]+[[:space:]]on[a-z]+[[:space:]]*=' --include='*.html' --include='*.svg' web \
	>>"$WORK/inline" 2>/dev/null || true
if [ -s "$WORK/inline" ]; then
	echo "FAIL: a file under web/ holds a script in the page or an event handler"
	echo "      attribute. Content-Security-Policy script-src 'self' blocks both:"
	cat "$WORK/inline"
	echo inline >>"$WORK/bad"
else
	echo "inline scripts: none; every script under web/ is a file of its own"
fi

[ ! -s "$WORK/bad" ] || exit 1
echo "js-check: all checks pass"
