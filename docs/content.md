# Playlists and content

This page covers playlists, the images and videos that a playlist can hold,
the transitions, Ken Burns, schedules, and the two ways to add media: the web
UI and the stick itself.

## What a playlist is

A playlist is a directory on the media partition. Each playlist directory
holds a file named `playlist.toml` and the media files that it uses. A
directory with no `playlist.toml` is not a playlist, so you can use it as
scratch space.

Every new device starts with one playlist named `default`, made from the
seven demo videos that ship with PortaPixel. Each video plays for its full
length. Replace them with your own content.

The device plays the items of the active playlist in order, and then starts
again at the first item. The player (mpv) reads each file straight from the
media partition. The loop goes on if the daemon is busy.

## Images and videos

A playlist item is an image or a video. There are no other kinds of item.

| Kind | Extensions |
|---|---|
| Image | `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp`, `.avif`, `.bmp` |
| Video | `.mp4`, `.m4v`, `.mov`, `.webm`, `.mkv`, `.ogv` |

A file with another extension is not a playable item. An SVG file is an
example. The device skips such a file and writes a line to the activity log.
See "A broken item" below.

A photo with an EXIF orientation shows the right way up. The player turns the
picture for you.

### Advice on video files

- Use H.264 for video on a Raspberry Pi. A Pi Zero 2 W, Pi 3 and Pi 4 decode
  H.264 in hardware.
- HEVC (H.265) decodes in software on a Pi 4 and a Pi 5. A Pi 4 and a Pi 5
  have an HEVC decoder, but the FFmpeg of Alpine cannot use it. A Pi Zero 2 W
  and a Pi 3 have no HEVC hardware. A large HEVC video can drop frames.
- A Pi 5 has no H.264 decoder, so it decodes H.264 in software.
- On a PC with an Intel or AMD graphics chip, the hardware decodes the video
  through VA-API.
- A device always falls back to software decode when the hardware decoder
  fails.
- Match the size of the video to the screen. A video larger than the screen
  costs time and gives no better picture.
- Read `now_playing.dropped_frames` in `/api/status` to see if a video drops
  frames.

All of this is untested on real hardware. See `docs/release-checklist.md`.

### Advice on images

- Use a copy of the image at the size of the screen. A very large image takes
  a moment to decode each time round the loop. The editor warns about an image
  of more than 12 MiB or 40 megapixels.

### Item keys

These keys go in an `[[item]]` table of `playlist.toml`.

| Key | What it does |
|---|---|
| `file` | The file name, relative to the playlist directory. Required. |
| `duration` | How many seconds an image stays on the screen. An image with no `duration` uses `playback.image_duration` of the device. Images only. |
| `mute` | `true` silences a video. The default is off: a video plays with sound. |
| `max_duration` | Stops a video after this many seconds. Leave it out and the video plays its full length. |
| `transition` | The transition into this item. See "Transitions". |
| `transition_ms` | The length of that transition, in milliseconds. |

A playlist that holds one image keeps the image on the screen for ever. A
playlist that holds one video plays the video again and again.

A `[playlist]` table at the top of the file holds the keys of the whole
playlist.

| Key | What it does |
|---|---|
| `name` | The title. Empty means the name of the directory. |
| `shuffle` | `true` plays the items in a new order each time. This replaces `playback.shuffle`. |
| `transition` | The transition of this playlist. This replaces `playback.transition`. |
| `ken_burns` | `true` gives each image a slow zoom and pan. See "Ken Burns". |

A short example:

```toml
[playlist]
name = "Lobby"
transition = "slide-in-left"
ken_burns = true

[[item]]
file = "welcome.jpg"
duration = 8

[[item]]
file = "intro.mp4"
mute = true
transition = "fade"
transition_ms = 800
```

## Transitions

A transition is the effect between two items. Set it in three places:

1. On an item, to name the transition into that item.
2. On a playlist, for all items that name no transition.
3. In `portapixel.toml`, as `playback.transition`, for the whole device.

A value falls back by itself: the item, then the playlist, then the device. The
word and the length fall back separately. An item that sets `transition_ms`
alone changes the length only.

The transition of an item is the transition INTO that item, from the item
before it. For the first item, that is the last item, because the list goes
round.
When the playlist is shuffled, an item keeps its own transition, but the item
before it can be any other item.

`playback.transition_ms` is the length of a transition. A `cut` has no length.

There are 22 transitions. In a name that ends in `-left`, `-right`, `-up` or
`-down`, the word is the direction of the movement.

| Transition | What you see |
|---|---|
| `cut` | The next item shows at once. |
| `fade` | The picture goes dark, and the next item comes out of black. |
| `fade-white` | The same, but through white. |
| `crossfade` | The old picture fades into the next item. |
| `wipe-left`, `wipe-right`, `wipe-up`, `wipe-down` | The edge of the old picture moves in the direction of the word and uncovers the next item. |
| `push-left`, `push-right`, `push-up`, `push-down` | The old picture moves out and the next item moves in behind it. |
| `slide-in-left`, `slide-in-right`, `slide-in-up`, `slide-in-down` | The next item moves in over the old picture. The old picture stays where it is. |
| `slide-out-left`, `slide-out-right`, `slide-out-up`, `slide-out-down` | The old picture moves away. The next item stays where it is. |
| `zoom-out` | The old picture gets smaller, toward the centre of the screen. |
| `split` | The old picture opens from the centre, like a barn door. |

### How a transition works

The player (mpv) takes a copy of the last picture of the old item. It shows
the copy on top, loads the next item under it, and then moves or fades the
copy away. This works with any video output. The copy is a still picture.

The old item has ended when a transition starts. So the copy shows its last
frame, and an old video stands still during the transition. A `crossfade` from
a video can be different. See "The moving crossfade".

A transition uses the first part of the next item. The length of the loop
does not change.

Every fault ends in a plain cut. A fault is a failed copy, a failed script or
a next item that does not show in 5 seconds. The ops log names the fault, and
the playlist goes on.

A slow device draws fewer steps in a transition. The length of the transition
stays the same, and the steps are coarser. In the lab, a 1080p screen on a slow
device looked coarse. Use a wipe or a push there, or a longer `transition_ms`.
Test it on your hardware.

### The moving crossfade

By default, a `crossfade` from a video uses the copy, and the old video stands
still. On a fast device both items can move. The setting `playback.motion`
decides. It has three words: `auto`, `on` and `off`.

- `auto` turns the moving crossfade on for an x86_64 PC and for a Raspberry Pi
  5 or Compute Module 5. It turns it off for every other Raspberry Pi.
- `on` always uses it.
- `off` never uses it.

With `auto`, a moving crossfade can fail, or it can drop more than a quarter
of its frames. The device then switches it off until the next restart of the
daemon, and the ops log writes `player.motion.off`. With `on`, the device keeps
it on and writes `player.motion.slow`, at most one time each hour.

A moving crossfade needs these conditions. Else the device uses the copy.

- The old item is a video, and it is at least 1 second longer than the
  transition.
- Both items have the same width and height. The next item can be an image.
- Neither item has a rotation tag, and both have square pixels.

## Ken Burns

`ken_burns = true` in the `[playlist]` table gives each image a slow zoom and
pan while it shows. The zoom changes between 1 and 1.12 times, and the picture
drifts to a random side by 4 percent at most. The edge of the picture never
shows. A video does not move this way.

Ken Burns needs the `gpu` video output. A screen can use the `drm` output, for
example a virtual machine or a board with no graphics driver. It shows the
images still. The ops log writes `player.kenburns.off` one time when the
playlist loads. See `display.video_output` in `docs/settings.md`.

An image alone in a playlist has no end, so it gets no Ken Burns.

Ken Burns is a choice of the playlist. The device has no setting for it.

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

An item can name a file that is not there, or a file kind that PortaPixel
does not play. The device skips that one item and writes a line to the
activity log (Ops log). It never stops the rest of the playlist for one bad
item.

When no item of the playlist can play, the screen shows the fallback screen
with the address of the device. The device tries the playlist again after 5
minutes.

## The comment-loss warning

`playlist.toml` and `portapixel.toml` are plain text files that you can edit
by hand. When you save a playlist from the web UI, PortaPixel writes the
whole file again from its own model. The stock comments come back every
time. Any comment that you typed into the file yourself does not come back.
The web UI shows this warning once before the first save of each visit to
the page.
