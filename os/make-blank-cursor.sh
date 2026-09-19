#!/bin/sh
# os/make-blank-cursor.sh -- make the transparent cursor theme of the image.
#
# WHY: cage 0.2.1 draws a software mouse pointer in the middle of the screen and
# has no flag that stops it. A signage screen must show the content and nothing
# else. wlroots and Chromium both take the pointer image from an Xcursor theme,
# so a theme in which every cursor is one transparent pixel hides the pointer in
# the compositor and in the page. internal/device/browser/command.go sets
# XCURSOR_THEME, XCURSOR_PATH and XCURSOR_SIZE for the cage session.
#
# TWO theme directories, because two programs ask in two ways. Measured in QEMU
# on 2026-09-19 with cage 0.2.1 and wlroots 0.19:
#
#   portapixel-blank  Chromium reads XCURSOR_THEME and asks for this name.
#   default           cage holds no XCURSOR_THEME string at all. It gives
#                     wlroots a null theme name, and wlroots then asks for the
#                     name "default". Its index.theme inherits the blank theme,
#                     so the compositor pointer is also transparent.
#
# wlroots reads XCURSOR_PATH, and /usr/share/icons is in its list even without
# the variable.
#
# Run this script one time on a development machine. It writes into
# os/overlay/usr/share/icons/, and those files go into the image with the rest
# of the overlay. Run it again only to change the theme.
#
# THE XCURSOR BINARY FORMAT
#
# All numbers are unsigned 32-bit and little-endian. A file has a header, a table
# of contents, and one chunk for each image.
#
#   file header, 16 bytes
#     magic     "Xcur", which reads back as the number 0x72756358
#     header    the size of this file header, always 16
#     version   0x00010000, which is version 1.0
#     ntoc      how many entries the table of contents has
#
#   one table of contents entry, 12 bytes
#     type      0xfffd0002 for an image, 0xfffe0001 for a comment
#     subtype   for an image, the nominal size, for example 24
#     position  the byte offset of the chunk in this file
#
#   one image chunk, 36 bytes of header and then the pixels
#     header    the size of this chunk header, always 36
#     type      0xfffd0002 again
#     subtype   the nominal size again
#     version   1
#     width     pixels across, 1 here
#     height    pixels down, 1 here
#     xhot      the hot spot across, 0 here
#     yhot      the hot spot down, 0 here
#     delay     milliseconds for an animation, 0 here
#     pixels    width times height ARGB values, alpha first, premultiplied
#
# Our file is 16 + 12 + 36 + 4 = 68 bytes and its one pixel is 0x00000000, which
# is fully transparent.
set -eu

ICONS="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)/overlay/usr/share/icons"
DEST="$ICONS/portapixel-blank"

# The nominal size of the one image. XCURSOR_SIZE must ask for this size.
SIZE=24

# The cursor names that wlroots and Chromium ask for. Each is a copy of the same
# 68 bytes, not a symbolic link: a Windows development machine writes a link as a
# text file, and the file in the image must be the cursor itself.
NAMES="default left_ptr pointer text xterm"

# u32 writes one number as four little-endian bytes.
# printf '%b' with \0ddd is the one octal escape that ash, dash and bash all
# read the same way.
u32() {
	_n="$1"
	_i=0
	while [ "$_i" -lt 4 ]; do
		printf '%b' "\\0$(printf '%o' $((_n % 256)))"
		_n=$((_n / 256))
		_i=$((_i + 1))
	done
}

blank_cursor() {
	# file header
	u32 $((0x72756358)) # magic "Xcur"
	u32 16              # the size of this header
	u32 $((0x00010000)) # version 1.0
	u32 1               # one entry in the table of contents
	# table of contents
	u32 $((0xfffd0002)) # the entry is an image
	u32 "$SIZE"         # the nominal size
	u32 28              # the chunk starts after 16 + 12 bytes
	# image chunk
	u32 36              # the size of this chunk header
	u32 $((0xfffd0002)) # an image again
	u32 "$SIZE"         # the nominal size again
	u32 1               # the image chunk version
	u32 1               # width
	u32 1               # height
	u32 0               # the hot spot across
	u32 0               # the hot spot down
	u32 0               # no animation delay
	u32 0               # one ARGB pixel, fully transparent
}

mkdir -p "$DEST/cursors"

# index.theme has no Inherits line on purpose. With an Inherits line, a name that
# this theme does not hold comes from another theme, and that cursor is visible.
cat >"$DEST/index.theme" <<'EOF'
[Icon Theme]
Name=PortaPixel blank
Comment=Every cursor in this theme is one transparent pixel.
EOF

blank_cursor >"$DEST/cursors/.blank.tmp"
for name in $NAMES; do
	cp "$DEST/cursors/.blank.tmp" "$DEST/cursors/$name"
	chmod 0644 "$DEST/cursors/$name"
done
rm -f "$DEST/cursors/.blank.tmp"
chmod 0644 "$DEST/index.theme"

# The theme that cage asks for. It holds no cursor of its own and inherits all
# of them from the blank theme.
mkdir -p "$ICONS/default"
cat >"$ICONS/default/index.theme" <<'EOF'
[Icon Theme]
Name=Default
Comment=The pointer of this device is transparent.
Inherits=portapixel-blank
EOF
chmod 0644 "$ICONS/default/index.theme"

printf 'wrote %s\n' "$ICONS"
ls -l "$DEST/cursors" "$ICONS/default"
