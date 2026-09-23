# Playlists and content

This page covers playlists, the kinds of item a playlist can hold, schedules,
and the two ways to add media: the web UI and the stick itself.

## What a playlist is

A playlist is a directory on the media partition. Each playlist directory
holds a file named `playlist.toml` and the media files that it uses. A
directory with no `playlist.toml` is not a playlist, so you can use it as
scratch space.

Every new device starts with one playlist named `default`, made from the
seven demo videos that ship with PortaPixel. Each video plays for its full
length. Replace them with your own content.

## Item types

A playlist item is an image, a video, or a web page.

| Kind | Extensions |
|---|---|
| Image | `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.svg`, `.avif`, `.bmp` |
| Video | `.mp4`, `.m4v`, `.mov`, `.webm`, `.mkv`, `.ogv` |
| Web page | any address that starts with `http://` or `https://` |

A file with another extension is not a playable item. Content with no
extension that the daemon recognizes is skipped and logged; see "A broken
item" below.

### Durations, mute and the decode cap

- **`duration`** sets how many seconds an image stays on the screen. Leave it
  out and the image uses the device's default image duration.
- **`mute`** silences a video. The default is off: a video plays with sound
  unless you turn `mute` on.
- **`max_duration`** stops a video after this many seconds. Leave it out and
  the video plays its full length.
- **`refresh_seconds`** reloads a web page item in place, on this interval.
  Use it for a page that shows numbers that change, so the screen never shows
  a stale page for hours.
- A web page item in a playlist of more than one item needs a `duration`,
  because the daemon must know when to move to the next item.

### Transitions

A playlist can set its own transition, which replaces the device's default
transition for that playlist only. The choices are `crossfade`, `push-left`,
`push-right`, `push-up`, `push-down`, and `cut`.

### Single-URL kiosk mode

A playlist of exactly one web page item is kiosk mode (D42). The device parks
the browser on that one page and refreshes it on `refresh_seconds`, with no
player screen in between. Use this for a single dashboard or a single sign
that never needs to switch to anything else.

## Schedules

A schedule rule says which playlist plays, on which days, and between which
times. The device reads the rules from the top. The first rule that matches
the current day and time wins. When no rule matches, the default playlist
plays.

A rule needs both a start time and an end time, or neither of them. A rule
with no times at all covers the whole day: this is how you write "this
playlist plays every Saturday and Sunday, all day."

## Sideload: add files from a computer

You can add or change files on the media partition from any computer, with
no network at all.

1. Turn the device off, or leave it running; the media partition is safe to
   read and write from another computer either way.
2. Pull the USB stick out and put it into a laptop or a desktop computer.
3. Copy files into a playlist directory, or make a new directory with its
   own `playlist.toml`.
4. Edit `playlist.toml` by hand if you want, following the shape of the
   files that PortaPixel already wrote.
5. **Eject the stick properly before you pull it out.** An operating system
   buffers writes, and pulling a stick that is still writing can damage the
   exFAT media partition.
6. Put the stick back into the device and let it boot, or tell the running
   device to look for new files (see below).

## Add files from the web UI

Open Playlists, pick a playlist, and use its "Add files from the stick"
control to upload new images or videos. An upload streams straight into the
playlist's directory. The size of an upload is limited only by the free
space on the stick.

## Look for new files: the web button and the rescan hook

After you sideload files from a laptop, the device does not see them until
it scans the media partition again. Three things trigger a scan: a reboot,
the "Look for new files" button on the Dashboard and the Playlists page, and
the documented hook below.

```sh
curl -c cookies.txt -X POST http://portapixel.local/api/login \
  -d '{"password":"portapixel"}'

curl -b cookies.txt -X POST http://portapixel.local/api/rescan \
  -H "X-PortaPixel: 1"
```

The first call signs in and keeps the session in `cookies.txt`. The second
call runs the scan. Every state-changing call needs the `X-PortaPixel: 1`
header; a script that leaves it out gets refused.

## File name rules

A file name inside a playlist directory must stay inside that directory. The
device refuses a file path that:

- uses a backslash instead of a forward slash between directory names,
- starts with a forward slash, or names a drive letter,
- is not already in its simplest form, for example `./a.jpg` or `a//b.jpg`,
- steps outside the playlist directory with `..`.

## A broken item

When an item names a file that is missing, or a file kind that PortaPixel
does not play, the device skips that one item and writes a line to the
activity log (Ops log). It never stops the rest of the playlist for one bad
item.

## The comment-loss warning

`playlist.toml` and `portapixel.toml` are plain text files that you can edit
by hand. When you save a playlist from the web UI, PortaPixel writes the
whole file again from its own model. The stock comments come back every
time. Any comment that you typed into the file yourself does not come back.
The web UI shows this warning once before the first save of each visit to
the page.
