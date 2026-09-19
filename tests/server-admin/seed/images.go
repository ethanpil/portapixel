package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math/rand"
)

// testFile is one generated upload.
type testFile struct {
	name  string
	bytes []byte
}

// library makes the files that the media page shows. The pictures are drawn
// here, so the repository holds no binary fixture.
func library() []testFile {
	return []testFile{
		jpegFile("welcome-autumn.jpg", 1920, 1080, color.RGBA{198, 122, 44, 255}, color.RGBA{86, 48, 18, 255}),
		jpegFile("lobby-hours.jpg", 1920, 1080, color.RGBA{44, 96, 132, 255}, color.RGBA{14, 34, 52, 255}),
		pngFile("safety-notice-1.png", 1280, 720, color.RGBA{186, 62, 60, 255}, color.RGBA{72, 20, 20, 255}),
		pngFile("safety-notice-2.png", 1280, 720, color.RGBA{201, 137, 43, 255}, color.RGBA{74, 48, 12, 255}),
		pngFile("menu-board.png", 1080, 1920, color.RGBA{52, 115, 68, 255}, color.RGBA{16, 42, 24, 255}),
		jpegFile("retail-promo.jpg", 1920, 1080, color.RGBA{120, 74, 150, 255}, color.RGBA{40, 22, 54, 255}),
		jpegFile("room-signs.jpg", 1280, 800, color.RGBA{96, 104, 110, 255}, color.RGBA{34, 38, 42, 255}),
		// A video has no thumbnail, so the media page must draw its own icon.
		videoFile("promo-fall.mp4", 1_400_000),
		videoFile("safety-brief-4k-hevc.mp4", 2_600_000),
	}
}

// canvas draws a diagonal gradient with a few blocks. It gives each file a
// thumbnail that a person can tell from the others.
func canvas(w, h int, a, b color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := float64(x+y) / float64(w+h)
			img.Set(x, y, color.RGBA{
				R: mix(a.R, b.R, t), G: mix(a.G, b.G, t), B: mix(a.B, b.B, t), A: 255,
			})
		}
	}
	// Three pale blocks, so the picture is not one flat wash.
	pale := color.RGBA{242, 240, 236, 255}
	for i := 0; i < 3; i++ {
		x := w / 8 * (i*2 + 1)
		y := h / 3
		draw.Draw(img, image.Rect(x, y, x+w/9, y+h/4), &image.Uniform{pale}, image.Point{}, draw.Src)
	}
	return img
}

func mix(a, b uint8, t float64) uint8 {
	return uint8(float64(a)*(1-t) + float64(b)*t)
}

func jpegFile(name string, w, h int, a, b color.RGBA) testFile {
	var buf bytes.Buffer
	jpeg.Encode(&buf, canvas(w, h, a, b), &jpeg.Options{Quality: 82})
	return testFile{name: name, bytes: buf.Bytes()}
}

func pngFile(name string, w, h int, a, b color.RGBA) testFile {
	var buf bytes.Buffer
	png.Encode(&buf, canvas(w, h, a, b))
	return testFile{name: name, bytes: buf.Bytes()}
}

// videoFile makes bytes that the store keeps and cannot decode. The size is what
// the media page shows; the content only has to be the same on every run, so the
// hash of a second run matches the first.
func videoFile(name string, size int) testFile {
	data := make([]byte, size)
	source := rand.New(rand.NewSource(int64(len(name))))
	source.Read(data)
	copy(data, []byte("\x00\x00\x00\x18ftypmp42"))
	return testFile{name: name, bytes: data}
}
