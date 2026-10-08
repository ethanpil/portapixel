package fallback

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

// fillRound fills a rectangle with rounded corners. The straight parts are plain
// fills. Only the four corners go pixel by pixel, and they blend with the pixels
// below by the share of the pixel that the corner circle covers.
func fillRound(img *image.RGBA, r image.Rectangle, radius int, c color.RGBA) {
	radius = min(radius, r.Dx()/2, r.Dy()/2)
	src := image.NewUniform(c)
	if radius <= 0 {
		draw.Draw(img, r, src, image.Point{}, draw.Over)
		return
	}
	// The cross: one band for the full width and two bands above and below it.
	draw.Draw(img, image.Rect(r.Min.X, r.Min.Y+radius, r.Max.X, r.Max.Y-radius), src, image.Point{}, draw.Over)
	draw.Draw(img, image.Rect(r.Min.X+radius, r.Min.Y, r.Max.X-radius, r.Min.Y+radius), src, image.Point{}, draw.Over)
	draw.Draw(img, image.Rect(r.Min.X+radius, r.Max.Y-radius, r.Max.X-radius, r.Max.Y), src, image.Point{}, draw.Over)

	rad := float64(radius)
	corners := []struct{ x, y, cx, cy int }{
		{r.Min.X, r.Min.Y, r.Min.X + radius, r.Min.Y + radius},
		{r.Max.X - radius, r.Min.Y, r.Max.X - radius, r.Min.Y + radius},
		{r.Min.X, r.Max.Y - radius, r.Min.X + radius, r.Max.Y - radius},
		{r.Max.X - radius, r.Max.Y - radius, r.Max.X - radius, r.Max.Y - radius},
	}
	for _, k := range corners {
		for y := k.y; y < k.y+radius; y++ {
			for x := k.x; x < k.x+radius; x++ {
				dist := math.Hypot(float64(x)+0.5-float64(k.cx), float64(y)+0.5-float64(k.cy))
				cover := math.Min(1, math.Max(0, rad-dist+0.5))
				if cover <= 0 || !image.Pt(x, y).In(img.Rect) {
					continue
				}
				blend(img, x, y, c, cover)
			}
		}
	}
}

// blend mixes colour c into one pixel. cover is the weight of c, from 0 to 1.
func blend(img *image.RGBA, x, y int, c color.RGBA, cover float64) {
	i := img.PixOffset(x, y)
	mix := func(old, nu uint8) uint8 {
		return uint8(float64(old)*(1-cover) + float64(nu)*cover + 0.5)
	}
	img.Pix[i+0] = mix(img.Pix[i+0], c.R)
	img.Pix[i+1] = mix(img.Pix[i+1], c.G)
	img.Pix[i+2] = mix(img.Pix[i+2], c.B)
	img.Pix[i+3] = 0xff
}

// fillRect fills a rectangle.
func fillRect(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
}
