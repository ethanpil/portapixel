package fallback

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// fontSet holds the three parsed fonts. The Go fonts are BSD licensed, and
// they are in the binary, so the screen needs no font file on the device.
type fontSet struct {
	regular, bold, mono *opentype.Font
}

var (
	fontsOnce sync.Once
	fonts     *fontSet
	fontsErr  error
)

// loadFonts parses the fonts one time. A parse costs more than a render.
func loadFonts() (*fontSet, error) {
	fontsOnce.Do(func() {
		set := &fontSet{}
		for _, f := range []struct {
			dst  **opentype.Font
			data []byte
			name string
		}{
			{&set.regular, goregular.TTF, "Go Regular"},
			{&set.bold, gobold.TTF, "Go Bold"},
			{&set.mono, gomono.TTF, "Go Mono"},
		} {
			parsed, err := opentype.Parse(f.data)
			if err != nil {
				fontsErr = fmt.Errorf("fallback: parse the font %s: %w", f.name, err)
				return
			}
			*f.dst = parsed
		}
		fonts = set
	})
	return fonts, fontsErr
}

type faceKey struct {
	font *opentype.Font
	px   float64
}

// faceCache makes one face for each font and size in one render. A face holds
// a cache of glyphs, so a face that draws many lines is cheaper.
type faceCache struct {
	faces map[faceKey]font.Face
	err   error
}

// get gives the face of a font at a size in pixels. A fault is kept in err and
// the drawing goes on with a plain face, so the caller needs no error path.
func (c *faceCache) get(f *opentype.Font, px float64) font.Face {
	key := faceKey{f, px}
	if face, ok := c.faces[key]; ok {
		return face
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		if c.err == nil {
			c.err = fmt.Errorf("fallback: make a face of %.1f px: %w", px, err)
		}
		return basicfont.Face7x13
	}
	c.faces[key] = face
	return face
}

func (c *faceCache) close() {
	for _, face := range c.faces {
		_ = face.Close()
	}
}

// face gives a face at a size in units.
func (s *scene) face(f *opentype.Font, units float64) font.Face {
	return s.faces.get(f, units*s.u)
}

// width gives the width of a text in pixels.
func width(face font.Face, text string) int {
	return font.MeasureString(face, text).Ceil()
}

// lineHeight gives the height of a line: the size times factor. The factor of the
// text is lineFactor.
func (s *scene) lineHeight(units, factor float64) int { return s.px(units * factor) }

// baseline gives the y of the baseline of a line that has its top at y and the
// height lineH. The text stands in the middle of the line.
func baseline(face font.Face, y, lineH int) int {
	m := face.Metrics()
	ascent, descent := m.Ascent.Ceil(), m.Descent.Ceil()
	return y + (lineH-(ascent+descent))/2 + ascent
}

// text draws one line with its left end at x. spacing is the extra space after
// each character, in pixels. It gives the width of the line.
func (s *scene) text(face font.Face, text string, x, base int, c color.RGBA, spacing float64) int {
	d := font.Drawer{
		Dst:  s.img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, base),
	}
	if spacing == 0 {
		d.DrawString(text)
		return (d.Dot.X - fixed.I(x)).Ceil()
	}
	extra := fixed.Int26_6(spacing * 64)
	for _, r := range text {
		d.DrawString(string(r))
		d.Dot.X += extra
	}
	// The last space is not part of the visible line.
	return (d.Dot.X - extra - fixed.I(x)).Ceil()
}

// spacedWidth gives the width of a text that text draws with spacing.
func spacedWidth(face font.Face, text string, spacing float64) int {
	extra := fixed.Int26_6(spacing * 64)
	var total fixed.Int26_6
	for _, r := range text {
		total += font.MeasureString(face, string(r)) + extra
	}
	return (total - extra).Ceil()
}

// fit cuts a text so that it fits in maxW pixels. It puts "…" at the cut.
func fit(face font.Face, text string, maxW int) string {
	if width(face, text) <= maxW {
		return text
	}
	runes := []rune(text)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		cut := strings.TrimRight(string(runes), " ") + "…"
		if width(face, cut) <= maxW {
			return cut
		}
	}
	return ""
}

// wrap breaks the words of a text into lines that fit in maxW pixels. A text
// that needs more than maxLines lines ends with "…" on the last line. sep goes
// between two words on one line.
func wrap(face font.Face, words []string, sep string, maxW, maxLines int) []string {
	var lines []string
	line := ""
	for i, word := range words {
		next := word
		if line != "" {
			next = line + sep + word
		}
		if line == "" || width(face, next) <= maxW {
			line = next
			continue
		}
		lines = append(lines, line)
		line = word
		if len(lines) == maxLines {
			// The rest does not fit. Put the cut mark on the last line.
			last := lines[maxLines-1] + sep + strings.Join(words[i:], sep)
			lines[maxLines-1] = fit(face, last, maxW)
			return lines
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	for i := range lines {
		lines[i] = fit(face, lines[i], maxW)
	}
	return lines
}
