package media

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"

	// The three formats that the standard library decodes. A file of any other
	// kind gets no thumbnail and the UI shows its own icon (D27).
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// ThumbWidth is the width of a thumbnail in pixels. It is wide enough for the
// card grid of the media page on a high-density screen.
const ThumbWidth = 480

// maxPixels is the largest picture that the server decodes.
//
// This is the guard against a decompression bomb. A PNG of a few kilobytes can
// declare 60000 by 60000 pixels. A decode of it would ask for 14 GB of memory and
// would stop the server. 50 million pixels covers every real photograph and every
// 8K frame.
const maxPixels = 50_000_000

// thumbQuality is the JPEG quality of a thumbnail.
const thumbQuality = 82

// ThumbTag names the thumbnail recipe. The ETag of a thumbnail holds it beside the
// hash of the source, so a browser with a cache life of a year still sees a new
// picture when the width, the quality or the scaler changes. Raise it with any such
// change.
const ThumbTag = "t1"

// makeThumb writes the thumbnail of one object. It gives the size of the
// original picture and reports if it made a thumbnail.
//
// Every failure is quiet and gives false. A file that is not a picture, a picture
// in a format that we cannot decode, a picture that declares too many pixels and a
// damaged file all take the same path. The caller treats a missing thumbnail as
// normal, so there is nothing here for a caller to handle.
func (s *Store) makeThumb(objectPath, sha string) (width, height int, ok bool) {
	f, err := os.Open(objectPath)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	// Read the header first. It gives the size without a decode, so a bomb is
	// refused before any memory goes to it.
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, false
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return cfg.Width, cfg.Height, false
	}
	if _, err := f.Seek(0, 0); err != nil {
		return cfg.Width, cfg.Height, false
	}

	src, _, err := image.Decode(f)
	if err != nil {
		return cfg.Width, cfg.Height, false
	}
	thumb := scale(src, ThumbWidth)

	out, err := os.CreateTemp(s.thumbs, "thumb*")
	if err != nil {
		return cfg.Width, cfg.Height, false
	}
	tmpName := out.Name()
	if err := jpeg.Encode(out, thumb, &jpeg.Options{Quality: thumbQuality}); err != nil {
		out.Close()
		os.Remove(tmpName)
		return cfg.Width, cfg.Height, false
	}
	if err := out.Close(); err != nil {
		os.Remove(tmpName)
		return cfg.Width, cfg.Height, false
	}
	dest, err := s.ThumbPath(sha)
	if err != nil {
		os.Remove(tmpName)
		return cfg.Width, cfg.Height, false
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return cfg.Width, cfg.Height, false
	}
	fsutil.SyncDir(s.thumbs)
	return cfg.Width, cfg.Height, true
}

// scale makes a smaller copy of src, at most maxWidth pixels wide.
//
// It takes the mean of the pixels of each source box. That is a box filter. A
// nearest-neighbour copy would drop most of the pixels of a large photograph and
// would give a thumbnail full of stair steps. A picture that is already small
// enough goes through without a change.
//
// The work is in the sRGB values and not in a linear light space. A thumbnail of a
// photograph is then a little darker than a fully correct one. Nobody sees the
// difference at this size. A correct version would need a table of 256 values and
// its inverse, for no gain on this page.
func scale(src image.Image, maxWidth int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= maxWidth {
		return src
	}
	dw := maxWidth
	dh := sh * dw / sw
	if dh < 1 {
		dh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		// The source rows of this output row.
		y0 := b.Min.Y + y*sh/dh
		y1 := b.Min.Y + (y+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dw; x++ {
			x0 := b.Min.X + x*sw/dw
			x1 := b.Min.X + (x+1)*sw/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					// At, then the 16-bit values. A 16-bit mean over a box of at
					// most a few hundred pixels cannot overflow a uint64.
					pr, pg, pb, pa := src.At(sx, sy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					bl += uint64(pb)
					a += uint64(pa)
				}
			}
			n := uint64((y1 - y0) * (x1 - x0))
			dst.Set(x, y, color.RGBA64{
				R: uint16(r / n), G: uint16(g / n), B: uint16(bl / n), A: uint16(a / n),
			})
		}
	}
	return dst
}
