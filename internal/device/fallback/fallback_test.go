package fallback

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/skip2/go-qrcode"
)

// fullInfo has every field set.
func fullInfo() Info {
	return Info{
		Name:        "Lobby screen",
		DeviceID:    "pp-3f9a21c4",
		URL:         "http://lobby.local/",
		IPs:         []string{"192.168.1.42", "fe80::a00:27ff:fe4e:66a1"},
		PairingCode: "483 917",
		Warning:     "Waiting for the clock.",
		Now:         time.Date(2026, 10, 8, 14, 7, 0, 0, time.FixedZone("EDT", -4*3600)),
	}
}

func decode(t *testing.T, data []byte) *image.RGBA {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the PNG does not decode: %v", err)
	}
	out := image.NewRGBA(img.Bounds())
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			out.Set(x, y, img.At(x, y))
		}
	}
	return out
}

// bright gives the bounding box of the pixels that are not black.
func bright(img *image.RGBA) image.Rectangle {
	box := image.Rectangle{Min: img.Rect.Max, Max: img.Rect.Min}
	for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
		for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
			if p := img.RGBAAt(x, y); p.R|p.G|p.B != 0 {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return box
}

func TestRenderGivesAPNGOfTheRightSize(t *testing.T) {
	for _, size := range []struct{ w, h int }{{1280, 720}, {1920, 1080}, {3840, 2160}, {1080, 1920}} {
		data, err := Render(fullInfo(), size.w, size.h)
		if err != nil {
			t.Fatalf("%dx%d: %v", size.w, size.h, err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%dx%d: %v", size.w, size.h, err)
		}
		if cfg.Width != size.w || cfg.Height != size.h {
			t.Errorf("the PNG is %dx%d, want %dx%d", cfg.Width, cfg.Height, size.w, size.h)
		}
	}
}

func TestRenderRefusesATinyScreen(t *testing.T) {
	if _, err := Render(fullInfo(), 100, 100); err == nil {
		t.Error("a 100x100 screen gave no error")
	}
	if _, err := Render(fullInfo(), 0, 0); err == nil {
		t.Error("a 0x0 screen gave no error")
	}
}

// Empty fields must not crash and must not leave a hole in the picture.
func TestRenderWithEmptyFields(t *testing.T) {
	tests := []struct {
		name string
		info Info
	}{
		{"nothing at all", Info{}},
		{"no network", Info{Name: "x", Now: fullInfo().Now}},
		{"no clock", Info{Name: "x", URL: "http://x.local/", IPs: []string{"10.0.0.1"}}},
		{"blank strings", Info{Name: "  ", DeviceID: "\n", URL: "  ", IPs: []string{"", " "}, PairingCode: " ", Warning: " "}},
		{"a negative shift", Info{Shift: -3}},
		{"a huge shift", Info{Shift: 1 << 40}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := Render(tt.info, 1280, 720)
			if err != nil {
				t.Fatal(err)
			}
			img := decode(t, data)
			if bright(img).Empty() {
				t.Error("the picture is all black")
			}
		})
	}
}

// Long and odd values must stay inside the screen.
func TestRenderWithLongFields(t *testing.T) {
	info := fullInfo()
	info.Name = strings.Repeat("A very long device name ", 10)
	info.URL = "http://" + strings.Repeat("a", 60) + ".local/"
	info.IPs = []string{"192.168.1.1", "192.168.1.2", "192.168.1.3", "192.168.1.4", "192.168.1.5", "192.168.1.6",
		"192.168.1.7", "192.168.1.8", "192.168.1.9", "192.168.1.10", "fe80::a00:27ff:fe4e:66a1", "fe80::a00:27ff:fe4e:66a2"}
	info.Warning = strings.Repeat("A warning that is far too long for one line. ", 20)
	info.Shift = 4
	data, err := Render(info, 1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, data)
	// The safe area is 5 % on each side, and the shift is at most 12 px at 1080p.
	box := bright(img)
	safe := image.Rect(1280*5/100-12, 720*5/100-12, 1280-1280*5/100+12, 720-720*5/100+12)
	if !box.In(safe) {
		t.Errorf("the picture reaches %v, outside the safe area %v", box, safe)
	}
}

// A shift of one step must move the layout, and the ring must close.
func TestShiftMovesTheLayout(t *testing.T) {
	render := func(shift int) image.Rectangle {
		info := fullInfo()
		info.Shift = shift
		data, err := Render(info, 1920, 1080)
		if err != nil {
			t.Fatal(err)
		}
		return bright(decode(t, data))
	}
	base := render(0)
	moved := render(1)
	if moved == base {
		t.Fatal("shift 1 draws the same box as shift 0")
	}
	// Step 1 is (+10, +6) at 1080p, so the whole box moves by that.
	if got, want := moved.Min.Sub(base.Min), image.Pt(10, 6); got != want {
		t.Errorf("shift 1 moved the layout by %v, want %v", got, want)
	}
	if again := render(len(shifts)); again != base {
		t.Errorf("shift %d gives %v, want %v (the ring has %d steps)", len(shifts), again, base, len(shifts))
	}
}

// The time of the screen follows the location of Now, not the local zone.
func TestClockUsesTheLocationOfNow(t *testing.T) {
	a := fullInfo()
	b := fullInfo()
	b.Now = a.Now.In(time.UTC) // the same moment, 18:07
	dataA, err := Render(a, 1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	dataB, err := Render(b, 1280, 720)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(dataA, dataB) {
		t.Error("14:07 and 18:07 gave the same picture")
	}
}

// The QR code in the PNG must be the QR code of the address. The test finds the
// white box, reads each module at its centre and compares the grid with the grid
// that the library makes. A real reader would also need the finder patterns, and
// they are part of the grid.
func TestQRCodeInThePNGIsTheCodeOfTheURL(t *testing.T) {
	for _, size := range []struct{ w, h int }{{1280, 720}, {1920, 1080}, {3840, 2160}} {
		for _, url := range []string{"http://lobby.local/", "http://192.168.100.200/", "http://a-longer-device-name-for-the-lobby.local/"} {
			info := fullInfo()
			info.URL = url
			data, err := Render(info, size.w, size.h)
			if err != nil {
				t.Fatal(err)
			}
			img := decode(t, data)

			code, err := qrcode.New(url, qrcode.Medium)
			if err != nil {
				t.Fatal(err)
			}
			want := code.Bitmap()
			n := len(want)

			// The white box is the only pure white area. The ink of the text is
			// a little darker than white.
			white := image.Rectangle{Min: img.Rect.Max, Max: img.Rect.Min}
			for y := 0; y < size.h; y++ {
				for x := 0; x < size.w; x++ {
					if img.RGBAAt(x, y) == (color.RGBA{0xff, 0xff, 0xff, 0xff}) {
						white = white.Union(image.Rect(x, y, x+1, y+1))
					}
				}
			}
			if white.Empty() {
				t.Fatalf("%dx%d %s: no white box", size.w, size.h, url)
			}
			// The first and the last dark module of the grid are the corners of the
			// finder patterns. The quiet zone is 4 modules wide on each side.
			// The search stays away from the rounded corners of the box. The pixels
			// there are black too, but they are not modules.
			inner := white.Inset(white.Dx() / 16)
			dark := image.Rectangle{Min: inner.Max, Max: inner.Min}
			for y := inner.Min.Y; y < inner.Max.Y; y++ {
				for x := inner.Min.X; x < inner.Max.X; x++ {
					if img.RGBAAt(x, y) == (color.RGBA{0, 0, 0, 0xff}) {
						dark = dark.Union(image.Rect(x, y, x+1, y+1))
					}
				}
			}
			module := dark.Dx() / (n - 8)
			if module < 3 {
				t.Errorf("%dx%d %s: a module is %d px; a camera needs 3 or more", size.w, size.h, url, module)
			}
			if module*(n-8) != dark.Dx() || dark.Dx() != dark.Dy() {
				t.Fatalf("%dx%d %s: the dark area is %v, not a square of %d modules", size.w, size.h, url, dark, n-8)
			}
			for row := 0; row < n; row++ {
				for col := 0; col < n; col++ {
					x := dark.Min.X + (col-4)*module + module/2
					y := dark.Min.Y + (row-4)*module + module/2
					got := img.RGBAAt(x, y) == (color.RGBA{0, 0, 0, 0xff})
					if got != want[row][col] {
						t.Fatalf("%dx%d %s: module (%d,%d) is %v, want %v", size.w, size.h, url, row, col, got, want[row][col])
					}
				}
			}
		}
	}
}

// The PNG must be small, because the caller renders each minute and mpv decodes
// it each time.
func TestRenderIsSmall(t *testing.T) {
	data, err := Render(fullInfo(), 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 250<<10 {
		t.Errorf("a 1080p PNG has %d bytes; more than 250 KB", len(data))
	}
}

func BenchmarkRender1080p(b *testing.B) {
	info := fullInfo()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Render(info, 1920, 1080); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRender720p(b *testing.B) {
	info := fullInfo()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Render(info, 1280, 720); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRender4K(b *testing.B) {
	info := fullInfo()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Render(info, 3840, 2160); err != nil {
			b.Fatal(err)
		}
	}
}
