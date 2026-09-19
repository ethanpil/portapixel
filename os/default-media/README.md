# Default media

These four slides are placeholders. A new device plays them, so the screen shows
a picture at the first boot instead of the words "No content yet". The owner
replaces them with real content.

| File | What it shows |
|---|---|
| `01-welcome.jpg` | The name of the product on the dark background |
| `02-replace.jpg` | The instruction to replace these slides |
| `03-grid.jpg` | A quiet field of marks |
| `04-mark.jpg` | The mark of the logo on the accent colour |

Each file is 1920x1080 JPEG at quality 85 and is smaller than 150 KB.

## How they reach the screen

1. `os/install.sh` copies the files in this directory to
   `/opt/portapixel/default-media/` in the image. It does not copy `gen/`.
2. At the first boot, `portapixeld provision` looks at the media partition. If
   the partition holds no playlist directory, it copies every image and video
   file from `/opt/portapixel/default-media/` to `<media>/default/`.
3. `provision` then writes `<media>/default/playlist.toml` with one item for
   each file, in name order. The items have no duration, so each one stays for
   `playback.image_duration` seconds from `portapixel.toml`.
4. The playlist is named `default`. The player plays it and `/api/status` shows
   it in `now_playing`.

`provision` never touches a media partition that already holds a playlist. The
content of the owner is safe.

To change the slides on a device, open the address that the device shows and use
the playlist editor. To change the slides in the image, replace the files here.

## How to make the files again

The generator has no dependencies. It uses the standard library only, and it
carries its own bitmap font.

```sh
cd os/default-media
go run ./gen
```

The program writes the four JPEG files into this directory. Both the program and
the JPEG files are in the repository, because a person who builds an image must
not need the Go tool for the slides.

## Licence

The generator in `gen/` made these files. They hold no photograph and no
third-party asset. Real CC0 photographs may replace them later.
