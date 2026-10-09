# Default media

These seven videos are the demo content. A new device plays them, so the screen
shows video at the first boot instead of the words "No content yet".
The owner replaces them with real content.

| File | What it shows | Length | Source | Licence |
|---|---|---|---|---|
| `01-meadow.mp4` | A green meadow with trees and yellow flowers | 23 s | Mixkit, clip 4075 | Mixkit Stock Video Free License |
| `02-creek.mp4` | A flight over a shallow, rocky creek | 22 s | Mixkit, clip 51585 | Mixkit Stock Video Free License |
| `03-forest.mp4` | The camera moves slowly into a pine forest | 24 s | Mixkit, clip 50847 | Mixkit Stock Video Free License |
| `04-beach.mp4` | Waves come onto a sand beach | 9 s | Mixkit, clip 5016 | Mixkit Stock Video Free License |
| `05-mountain-road.mp4` | A drive down a curved road in the mountains | 50 s | Mixkit, clip 41576 | Mixkit Stock Video Free License |
| `06-waterfall.mp4` | A waterfall falls into a brown river between rocks | 19 s | Source unknown, to be confirmed by the maintainer | Source unknown, to be confirmed by the maintainer |
| `07-roses.mp4` | Red roses with drops of water | 13 s | Source unknown, to be confirmed by the maintainer | Source unknown, to be confirmed by the maintainer |

Each file is H.264, 1280x720, yuv420p, about 3 Mbit/s, with no audio track. The
seven files are 59 MB together. They are the files that the maintainer
supplied, byte for byte. Do not encode them again.

The names start with a number, because the playlist plays the files in name
order. The long `05-mountain-road.mp4` is not first, so a person who looks at a
new screen sees a change soon.

## How they reach the screen

1. `os/install.sh` copies the files at the top level of this directory to
   `/opt/portapixel/default-media/` in the image. This `README.md` goes in
   too. It does no harm: `provision` copies only the files that the kind rule
   of `internal/playlist` knows as an image or a video.
2. At the first boot, `portapixeld provision` looks at the media partition. If
   the partition holds no playlist directory, it copies every image and video
   file from `/opt/portapixel/default-media/` to `<media>/default/`.
   `firstboot.sh` grows PPMEDIA before it calls `provision`. The PPMEDIA of the
   image is about 250 MB before the grow, so the files fit also when the grow
   does not occur.
3. `provision` then writes `<media>/default/playlist.toml` with one item for
   each file, in name order. The items have no duration and `mute = false`, so
   each video plays for its full length.
4. The playlist is named `default`. The player plays it and `/api/status` shows
   it in `now_playing`.

`provision` never touches a media partition that already holds a playlist. The
content of the owner is safe.

To change the videos on a device, open the address that the device shows and
use the playlist editor. To change the videos in the image, replace the files
here.

## Licence

Five of the videos are clips from Mixkit (<https://mixkit.co/>). We thank Mixkit
and the people who made the clips. The clips use the **Mixkit Stock Video Free
License**:

- The licence: <https://mixkit.co/license/#videoFree>
- The Mixkit terms: <https://mixkit.co/terms/>

The licence permits free commercial use. It lets a person copy, modify,
distribute, perform and broadcast the clips. It is non-exclusive, worldwide and
sub-licensable. It does not require attribution, but Mixkit asks for it. Read the
licence and the terms for the exact words.

These files are NOT under the MIT licence of PortaPixel, and they are NOT CC0.
The Mixkit terms still apply to everyone who takes the clips from this
repository or from an image. A person who wants the clips for another use should
get them from Mixkit.

The source and the licence of `06-waterfall.mp4` and `07-roses.mp4` are unknown.
The maintainer must confirm them.

Plan D37 asks for CC0 content, and these files do not meet that rule. The owner
decided on 2026-10-09 to keep all seven videos as they are.

`LICENSES-THIRD-PARTY.md` has the same list.
