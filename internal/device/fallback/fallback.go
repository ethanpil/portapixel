// Package fallback draws the fallback screen (D18) as a PNG.
//
// The player shows this screen when there is no content to play. It must tell a
// person who stands in front of the television how to reach the device. mpv
// shows the PNG with image-display-duration=inf, so the daemon renders again
// when the data changes or when the minute changes.
//
// The picture has these parts: the brand, the name and the ID of the device, the
// admin address, the IP addresses, the pairing code, a QR code of the address,
// the clock and a warning line. Every size follows the size of the screen, so
// 720p, 1080p and 4K look the same.
package fallback

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
	"time"

	"golang.org/x/image/font"
)

// Info is the data of the screen.
type Info struct {
	Name        string    // the device name
	DeviceID    string    // the short device ID
	URL         string    // the admin address, for example http://lobby.local/
	IPs         []string  // the IP addresses of the device
	PairingCode string    // "" when there is none
	Warning     string    // one short line, "" when there is none
	Now         time.Time // the clock. The screen shows it in the location of Now.
	Shift       int       // the burn-in step. The caller adds 1 every few minutes.
}

// The smallest picture that the layout can hold.
const (
	minWidth  = 320
	minHeight = 240
)

// The colours. They are the dark values of web/shared/pp.css. The accents are a
// little brighter than the admin UI, because the background here is pure black.
var (
	colInk        = color.RGBA{0xf4, 0xf1, 0xed, 0xff} // oklch(0.96 0.006 85)
	colMuted      = color.RGBA{0xae, 0xaa, 0xa4, 0xff} // oklch(0.74 0.010 80)
	colQuiet      = color.RGBA{0x7d, 0x7a, 0x74, 0xff} // oklch(0.58 0.010 80)
	colLink       = color.RGBA{0x8f, 0xd8, 0x9e, 0xff} // oklch(0.82 0.11 150)
	colBrand      = color.RGBA{0x47, 0x93, 0x5a, 0xff} // oklch(0.60 0.115 150)
	colTint       = color.RGBA{0x1c, 0x34, 0x22, 0xff} // oklch(0.30 0.045 150)
	colTintBorder = color.RGBA{0x37, 0x59, 0x3e, 0xff} // oklch(0.43 0.06 150)
	colTintInk    = color.RGBA{0xba, 0xf6, 0xc5, 0xff} // oklch(0.92 0.09 150)
	colBlack      = color.RGBA{0, 0, 0, 0xff}
	colWhite      = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

// shifts is a ring of small offsets in pixels at 1080p. A still picture for
// weeks can burn a screen, so the whole layout moves between these places.
var shifts = [...][2]int{{0, 0}, {10, 6}, {-8, 10}, {6, -8}, {-10, -6}, {12, 2}, {2, 12}, {-12, -2}}

// refUnit is the unit u at 1920x1080 (see unitFor). The offsets of shifts are
// for this unit, and the code scales them.
const refUnit = 0.006*1920 + 0.0036*1080

// Render draws the screen at w x h pixels and gives the PNG bytes.
func Render(info Info, w, h int) ([]byte, error) {
	if w < minWidth || h < minHeight {
		return nil, fmt.Errorf("fallback: a screen of %dx%d is too small; the minimum is %dx%d",
			w, h, minWidth, minHeight)
	}
	fonts, err := loadFonts()
	if err != nil {
		return nil, err
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Rect, image.NewUniform(colBlack), image.Point{}, draw.Src)

	s := newScene(img, fonts, info)
	s.draw()
	s.faces.close()
	if s.faces.err != nil {
		return nil, s.faces.err
	}

	// BestSpeed: the picture is mostly black, so it packs small at once. A
	// render must be cheap, because the caller renders again each minute.
	var buf bytes.Buffer
	buf.Grow(64 << 10)
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("fallback: encode the PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// unitFor gives the unit u of the layout in pixels. It is 0.6 % of the width
// plus 0.36 % of the height. It gives about 10 px at 1280x720, 15 px at
// 1920x1080 and 31 px at 3840x2160.
func unitFor(w, h int) float64 { return 0.006*float64(w) + 0.0036*float64(h) }

// scene holds what the drawing code needs.
type scene struct {
	img   *image.RGBA
	fonts *fontSet
	faces *faceCache
	info  Info
	u     float64 // the unit
}

func newScene(img *image.RGBA, fonts *fontSet, info Info) *scene {
	return &scene{
		img:   img,
		fonts: fonts,
		faces: &faceCache{faces: map[faceKey]font.Face{}},
		info:  info,
		u:     unitFor(img.Rect.Dx(), img.Rect.Dy()),
	}
}

// px turns a size in units into pixels.
func (s *scene) px(units float64) int { return int(units*s.u + 0.5) }

// block is a piece of the layout. The code measures it first and draws it later,
// so that it can centre it.
type block struct {
	w, h int
	draw func(x, y int)
}

// clean removes the characters that cannot be in one line of text.
func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(text))
}

// draw lays out the whole screen.
func (s *scene) draw() {
	w, h := s.img.Rect.Dx(), s.img.Rect.Dy()
	u := s.u
	portrait := w < h

	// The safe area: 5 % on each side keeps every part of the screen away from
	// the overscan area of an old television. The shift moves the area.
	shift := shifts[((s.info.Shift%len(shifts))+len(shifts))%len(shifts)]
	k := u / refUnit
	x0 := w*5/100 + int(math.Round(float64(shift[0])*k))
	y0 := h*5/100 + int(math.Round(float64(shift[1])*k))
	width := w - 2*(w*5/100)
	height := h - 2*(h*5/100)

	// The brand is at the top and the warning is at the bottom. The two
	// columns stand in the free space between them.
	brand := s.brandBlock()
	warn := s.warningBlock(width)
	gap := s.px(2.2)
	gridTop := y0 + brand.h + gap
	gridBottom := y0 + height - warn.h - gap

	brand.draw(x0, y0)
	warn.draw(x0, y0+height-warn.h)

	right := s.rightBlocks(portrait)
	rightW := 0
	for _, b := range right {
		rightW = max(rightW, b.w)
	}

	if portrait {
		// No room for two columns. The facts are at the top. The QR code and the
		// clock are at the bottom, side by side.
		left := s.leftBlock(width)
		left.draw(x0, gridTop)
		if len(right) == 2 {
			y := gridBottom - max(right[0].h, right[1].h)
			right[0].draw(x0, y)
			right[1].draw(x0+width-right[1].w, y+max(right[0].h, right[1].h)-right[1].h)
		} else if len(right) == 1 {
			right[0].draw(x0+width-right[0].w, gridBottom-right[0].h)
		}
		return
	}

	leftW := width
	if rightW > 0 {
		leftW = width - rightW - s.px(4)
	}
	left := s.leftBlock(leftW)
	left.draw(x0, centred(gridTop, gridBottom, left.h))

	// The right column is right aligned and centred as a whole.
	total := 0
	for i, b := range right {
		if i > 0 {
			total += s.px(1.4)
		}
		total += b.h
	}
	y := centred(gridTop, gridBottom, total)
	for _, b := range right {
		b.draw(x0+width-b.w, y)
		y += b.h + s.px(1.4)
	}
}

// centred gives the top of a block of height h that stands in the middle of
// the space from top to bottom. A block that is too high starts at the top.
func centred(top, bottom, h int) int {
	return top + max(0, (bottom-top-h)/2)
}
