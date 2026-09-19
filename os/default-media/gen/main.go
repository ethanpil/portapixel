// Command gen makes the placeholder slides of os/default-media.
//
// The slides are the content that a new device plays before the owner puts real
// content on the card. They must look correct on a television, so they are
// 1920x1080 and they use the colours of the brand.
//
// The program uses the standard library only: image, image/draw and image/jpeg,
// plus a small bitmap font in font.go. A generated file goes into the repository
// with this program, so a person can make the slides again after a change.
//
// Run it from os/default-media:
//
//	go run ./gen
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
)

// The brand colours. CONTEXT.md and the wireframes name them.
var (
	ink   = color.RGBA{0x22, 0x1F, 0x19, 0xFF} // the dark warm background
	paper = color.RGBA{0xF9, 0xF6, 0xF2, 0xFF} // the light background
	moss  = color.RGBA{0x34, 0x73, 0x44, 0xFF} // the accent
	leaf  = color.RGBA{0x4E, 0x9A, 0x5F, 0xFF} // a lighter accent for a gradient
)

const (
	width   = 1920
	height  = 1080
	quality = 85
)

func main() {
	out := flag.String("out", ".", "directory for the JPEG files")
	flag.Parse()

	slides := []struct {
		name string
		draw func(*image.RGBA)
	}{
		{"01-welcome.jpg", slideWelcome},
		{"02-replace.jpg", slideReplace},
		{"03-grid.jpg", slideGrid},
		{"04-mark.jpg", slideMark},
	}
	for _, s := range slides {
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		s.draw(img)
		path := filepath.Join(*out, s.name)
		if err := write(path, img); err != nil {
			fmt.Fprintf(os.Stderr, "gen: %v\n", err)
			os.Exit(1)
		}
		info, _ := os.Stat(path)
		fmt.Printf("%s  %d bytes\n", path, info.Size())
	}
}

func write(path string, img *image.RGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: quality}); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ---------------------------------------------------------------- the slides

// slideWelcome gives the name of the product on the dark background, with the
// mark of the logo beside it.
func slideWelcome(img *image.RGBA) {
	fill(img, ink)
	glow(img, 1480, 240, 980, leaf, 0.30)
	glow(img, 260, 980, 760, moss, 0.16)

	const scale = 13
	mark := 132.0
	gap := 52.0
	text := "PortaPixel"
	total := mark + gap + float64(textWidth(text, scale))
	x := (width - total) / 2
	y := 492.0

	fillRound(img, x, y-14, x+mark, y-14+mark, 38, moss, 1)
	drawText(img, int(x+mark+gap), int(y), text, scale, paper, 1)

	sub := "Placeholder slide 1 of 4"
	drawText(img, (width-textWidth(sub, 4))/2, 724, sub, 4, paper, 0.45)
}

// slideReplace says what to do with these slides. It is the light slide.
func slideReplace(img *image.RGBA) {
	fill(img, paper)
	band(img, 0.58, 1.0, moss, 0.10)
	glow(img, 1720, 1040, 900, leaf, 0.18)

	// A row of rounded squares. It repeats the mark of the logo.
	const (
		cells = 7
		size  = 84.0
		step  = 128.0
	)
	x0 := (width - (cells-1)*step - size) / 2
	for i := 0; i < cells; i++ {
		a := 0.22 + 0.13*float64(i)
		if a > 1 {
			a = 1
		}
		x := x0 + float64(i)*step
		fillRound(img, x, 232, x+size, 232+size, 24, moss, a)
	}

	head := "Replace these slides"
	drawText(img, (width-textWidth(head, 11))/2, 470, head, 11, ink, 1)

	line := "Open the address shown on the device"
	drawText(img, (width-textWidth(line, 5))/2, 660, line, 5, moss, 1)

	foot := "PortaPixel"
	drawText(img, (width-textWidth(foot, 4))/2, 900, foot, 4, ink, 0.40)
}

// slideGrid is the quiet slide: a field of marks on a gradient.
func slideGrid(img *image.RGBA) {
	fill(img, ink)
	diagonal(img, moss, 0.30)
	glow(img, 1600, 900, 820, leaf, 0.14)

	const (
		cols = 8
		rows = 4
		size = 110.0
		step = 176.0
	)
	x0 := (width - (cols-1)*step - size) / 2
	y0 := (height - (rows-1)*step - size) / 2
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			// The alpha falls from the top left to the bottom right, so the eye
			// gets one direction and the field does not look flat.
			a := 0.36 - 0.024*float64(r+c)
			if a < 0.05 {
				a = 0.05
			}
			x := x0 + float64(c)*step
			y := y0 + float64(r)*step
			fillRound(img, x, y, x+size, y+size, 32, paper, a)
		}
	}

	fillRound(img, 132, 928, 132+56, 928+56, 16, moss, 1)
	drawText(img, 220, 940, "PortaPixel", 5, paper, 0.85)
}

// slideMark is the accent slide: the mark alone, large, on moss green.
func slideMark(img *image.RGBA) {
	fill(img, moss)
	diagonal(img, ink, 0.30)
	vignette(img, 0.34)

	// One large rounded square with a smaller one inside it, which is the mark.
	fillRound(img, 760, 250, 1160, 650, 118, paper, 0.94)
	fillRound(img, 860, 350, 1060, 550, 58, moss, 1)

	name := "PortaPixel"
	drawText(img, (width-textWidth(name, 9))/2, 760, name, 9, paper, 1)

	line := "Replace these slides"
	drawText(img, (width-textWidth(line, 4))/2, 908, line, 4, paper, 0.72)
}

// ------------------------------------------------------------ the draw tools

// fill paints the whole picture.
func fill(img *image.RGBA, c color.RGBA) {
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			set(img, x, y, c)
		}
	}
}

// glow adds a soft round light. The falloff is smooth at both ends, so the edge
// of the light is not visible as a ring.
func glow(img *image.RGBA, cx, cy, radius float64, c color.RGBA, strength float64) {
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy) / radius
			if d >= 1 {
				continue
			}
			blend(img, x, y, c, strength*smooth(1-d))
		}
	}
}

// band paints a soft horizontal band from the height from0 to the height to1,
// both as a part of the picture height.
func band(img *image.RGBA, from0, to1 float64, c color.RGBA, strength float64) {
	y0 := from0 * height
	y1 := to1 * height
	for y := int(y0); y < int(y1) && y < height; y++ {
		t := (float64(y) - y0) / (y1 - y0)
		for x := 0; x < width; x++ {
			blend(img, x, y, c, strength*smooth(t))
		}
	}
}

// diagonal lays a gradient from the top left corner to the bottom right corner.
func diagonal(img *image.RGBA, c color.RGBA, strength float64) {
	maximum := float64(width + height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			t := 1 - float64(x+y)/maximum
			blend(img, x, y, c, strength*smooth(t))
		}
	}
}

// vignette makes the corners darker, which holds the eye in the middle.
func vignette(img *image.RGBA, strength float64) {
	cx, cy := float64(width)/2, float64(height)/2
	maximum := math.Hypot(cx, cy)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			d := math.Hypot(float64(x)-cx, float64(y)-cy) / maximum
			blend(img, x, y, ink, strength*smooth(d))
		}
	}
}

// fillRound paints a rounded rectangle with smooth edges. The coverage of a
// pixel comes from its distance to the shape, which gives a clean edge without
// a second, larger picture.
func fillRound(img *image.RGBA, x0, y0, x1, y1, radius float64, c color.RGBA, alpha float64) {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hw, hh := (x1-x0)/2, (y1-y0)/2
	radius = math.Min(radius, math.Min(hw, hh))

	for py := int(y0) - 1; py <= int(y1)+1; py++ {
		for px := int(x0) - 1; px <= int(x1)+1; px++ {
			dx := math.Abs(float64(px)+0.5-cx) - (hw - radius)
			dy := math.Abs(float64(py)+0.5-cy) - (hh - radius)
			var d float64
			if dx > 0 && dy > 0 {
				d = math.Hypot(dx, dy) - radius
			} else {
				d = math.Max(dx, dy) - radius
			}
			cover := 0.5 - d
			if cover <= 0 {
				continue
			}
			if cover > 1 {
				cover = 1
			}
			blend(img, px, py, c, alpha*cover)
		}
	}
}

// smooth gives an S curve between 0 and 1. A straight line leaves a visible
// edge where a gradient starts.
func smooth(t float64) float64 {
	switch {
	case t <= 0:
		return 0
	case t >= 1:
		return 1
	}
	return t * t * (3 - 2*t)
}

// blend puts c over the pixel with the given alpha.
func blend(img *image.RGBA, x, y int, c color.RGBA, alpha float64) {
	if x < 0 || y < 0 || x >= width || y >= height || alpha <= 0 {
		return
	}
	if alpha > 1 {
		alpha = 1
	}
	i := img.PixOffset(x, y)
	img.Pix[i+0] = mix(img.Pix[i+0], c.R, alpha)
	img.Pix[i+1] = mix(img.Pix[i+1], c.G, alpha)
	img.Pix[i+2] = mix(img.Pix[i+2], c.B, alpha)
	img.Pix[i+3] = 0xFF
}

func set(img *image.RGBA, x, y int, c color.RGBA) {
	i := img.PixOffset(x, y)
	img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 0xFF
}

func mix(have, want uint8, alpha float64) uint8 {
	return uint8(float64(have)*(1-alpha) + float64(want)*alpha + 0.5)
}
