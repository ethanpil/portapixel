package fallback

import (
	"image"
	"strings"

	"github.com/skip2/go-qrcode"
	"golang.org/x/image/font"
)

// The words on the screen.
const (
	wordAddress = "OPEN THIS ADDRESS IN A BROWSER"
	wordIPs     = "IP ADDRESSES"
	wordPairing = "PAIRING CODE"
	wordNoWeb   = "no network yet"
	wordNoIP    = "no address yet"
	wordMessage = "No content yet. Open the address above in a browser to add some."
)

// lineFactor is the line height of the text, as a part of its size.
const lineFactor = 1.35

// brandBlock draws the green mark and the word "PortaPixel".
func (s *scene) brandBlock() block {
	mark := s.px(2.1)
	face := s.face(s.fonts.bold, 2)
	gap := s.px(0.85)
	word := "PortaPixel"
	return block{
		w: mark + gap + width(face, word),
		h: mark,
		draw: func(x, y int) {
			// The mark is a square with round corners. In the 24 unit drawing of the
			// old page it was 22 units wide with a radius of 6.
			inset := mark / 24
			side := mark - 2*inset
			fillRound(s.img, image.Rect(x+inset, y+inset, x+inset+side, y+inset+side), mark*6/24, colBrand)
			s.text(face, word, x+mark+gap, baseline(face, y, mark), colInk, -0.02*2*s.u)
		},
	}
}

// warningBlock draws the warning at the bottom. Two lines are enough: more is a
// wall of text for a person who looks from far away. A screen with no warning
// keeps the room of one line, so the layout does not move.
func (s *scene) warningBlock(maxW int) block {
	face := s.face(s.fonts.regular, 1.25)
	lineH := s.lineHeight(1.25, 1.4)
	var lines []string
	if text := clean(s.info.Warning); text != "" {
		lines = wrap(face, strings.Fields(text), " ", maxW, 2)
	}
	return block{
		w: maxW,
		h: lineH * max(1, len(lines)),
		draw: func(x, y int) {
			for i, line := range lines {
				s.text(face, line, x, baseline(face, y+i*lineH, lineH), colQuiet, 0)
			}
		},
	}
}

// leftBlock is the left column: the name, the facts, the pairing code and the
// message. maxW is the width that it may use.
func (s *scene) leftBlock(maxW int) block {
	var parts []block

	// The name and the ID.
	nameFace := s.face(s.fonts.bold, 4)
	idFace := s.face(s.fonts.mono, 1.5)
	name := clean(s.info.Name)
	if name == "" {
		name = "PortaPixel"
	}
	name = fit(nameFace, name, maxW)
	id := fit(idFace, clean(s.info.DeviceID), maxW)
	nameH := s.lineHeight(4, lineFactor)
	idH := s.lineHeight(1.5, lineFactor)
	idGap := s.px(0.3)
	parts = append(parts, block{
		w: maxW,
		h: nameH + idGap + idH,
		draw: func(x, y int) {
			s.text(nameFace, name, x, baseline(nameFace, y, nameH), colInk, -0.02*4*s.u)
			s.text(idFace, id, x, baseline(idFace, y+nameH+idGap, idH), colMuted, 0)
		},
	})

	// The two facts.
	url := clean(s.info.URL)
	if url == "" {
		url = wordNoWeb
	}
	urlFace := s.face(s.fonts.mono, 2.5)
	url = fit(urlFace, url, maxW)
	urlH := s.lineHeight(2.5, lineFactor)
	var ips []string
	for _, ip := range s.info.IPs {
		if ip = clean(ip); ip != "" {
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		ips = []string{wordNoIP}
	}
	ipFace := s.face(s.fonts.mono, 1.7)
	ipLines := wrap(ipFace, ips, "    ", maxW, 3)
	ipH := s.lineHeight(1.7, lineFactor)

	label := s.labelFace()
	labelH := s.lineHeight(1.15, lineFactor)
	labelGap := s.px(0.35)
	factGap := s.px(1.4)
	parts = append(parts, block{
		w: maxW,
		h: labelH + labelGap + urlH + factGap + labelH + labelGap + ipH*len(ipLines),
		draw: func(x, y int) {
			s.label(label, wordAddress, x, y, labelH)
			y += labelH + labelGap
			s.text(urlFace, url, x, baseline(urlFace, y, urlH), colLink, 0)
			y += urlH + factGap
			s.label(label, wordIPs, x, y, labelH)
			y += labelH + labelGap
			for _, line := range ipLines {
				s.text(ipFace, line, x, baseline(ipFace, y, ipH), colInk, 0)
				y += ipH
			}
		},
	})

	// The pairing code, in a box.
	if code := clean(s.info.PairingCode); code != "" {
		codeFace := s.face(s.fonts.mono, 4)
		spacing := 0.14 * 4 * s.u
		codeH := s.lineHeight(4, lineFactor)
		padX, padY := s.px(1.4), s.px(0.7)
		boxW := spacedWidth(codeFace, code, spacing) + 2*padX
		boxH := codeH + 2*padY
		parts = append(parts, block{
			w: boxW,
			h: labelH + labelGap + boxH,
			draw: func(x, y int) {
				s.label(label, wordPairing, x, y, labelH)
				y += labelH + labelGap
				border := max(1, s.px(1)/15)
				box := image.Rect(x, y, x+boxW, y+boxH)
				fillRound(s.img, box, s.px(0.7), colTintBorder)
				fillRound(s.img, box.Inset(border), max(0, s.px(0.7)-border), colTint)
				s.text(codeFace, code, x+padX, baseline(codeFace, y+padY, codeH), colTintInk, spacing)
			},
		})
	}

	// The message. A line of it is 34 "0" widths wide at most.
	msgFace := s.face(s.fonts.regular, 1.85)
	msgW := min(maxW, 34*width(msgFace, "0"))
	msgLines := wrap(msgFace, strings.Fields(wordMessage), " ", msgW, 4)
	msgH := s.lineHeight(1.85, lineFactor)
	parts = append(parts, block{
		w: msgW,
		h: msgH * len(msgLines),
		draw: func(x, y int) {
			for _, line := range msgLines {
				s.text(msgFace, line, x, baseline(msgFace, y, msgH), colMuted, 0)
				y += msgH
			}
		},
	})

	gap := s.px(1.9)
	total := gap * (len(parts) - 1)
	for _, p := range parts {
		total += p.h
	}
	return block{
		w: maxW,
		h: total,
		draw: func(x, y int) {
			for _, p := range parts {
				p.draw(x, y)
				y += p.h + gap
			}
		},
	}
}

// labelFace is the small face of the capital labels above a fact.
func (s *scene) labelFace() font.Face { return s.face(s.fonts.regular, 1.15) }

// label draws a small capital label with wide letter spacing.
func (s *scene) label(face font.Face, text string, x, y, lineH int) {
	s.text(face, text, x, baseline(face, y, lineH), colQuiet, 0.09*1.15*s.u)
}

// rightBlocks gives the QR code and the clock. A screen with no address has no
// QR code. A screen with no clock time (a zero Now) has no clock.
func (s *scene) rightBlocks(portrait bool) []block {
	var out []block
	if b, ok := s.qrBlock(portrait); ok {
		out = append(out, b)
	}
	if b, ok := s.clockBlock(); ok {
		out = append(out, b)
	}
	return out
}

// qrBlock draws the QR code of the address on a white box. A code on black does
// not scan, so the box is white and has the quiet zone of the code inside.
func (s *scene) qrBlock(portrait bool) (block, bool) {
	url := clean(s.info.URL)
	if url == "" {
		return block{}, false
	}
	code, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		return block{}, false
	}
	bitmap := code.Bitmap() // true is a dark module. The quiet zone is in it.
	n := len(bitmap)
	if n == 0 {
		return block{}, false
	}

	// A module is a whole number of pixels, so the edges are sharp. The box is
	// 19 units wide in landscape and 16 in portrait, with a padding of 0.7.
	boxUnits := 19.0
	if portrait {
		boxUnits = 16
	}
	pad := s.px(0.7)
	module := max(1, (s.px(boxUnits)-2*pad)/n)
	side := module*n + 2*pad
	return block{
		w: side,
		h: side,
		draw: func(x, y int) {
			fillRound(s.img, image.Rect(x, y, x+side, y+side), s.px(0.7), colWhite)
			for row, line := range bitmap {
				for col := 0; col < len(line); col++ {
					if !line[col] {
						continue
					}
					// One rectangle for each run of dark modules in a row.
					run := 1
					for col+run < len(line) && line[col+run] {
						run++
					}
					px := x + pad + col*module
					py := y + pad + row*module
					fillRect(s.img, image.Rect(px, py, px+run*module, py+module), colBlack)
					col += run - 1
				}
			}
		},
	}, true
}

// clockBlock draws the time and the date, right aligned.
func (s *scene) clockBlock() (block, bool) {
	if s.info.Now.IsZero() {
		return block{}, false
	}
	timeFace := s.face(s.fonts.regular, 4.6)
	dateFace := s.face(s.fonts.regular, 1.5)
	clock := s.info.Now.Format("15:04")
	date := s.info.Now.Format("Monday, 2 January 2006")
	timeH := s.lineHeight(4.6, 1.05)
	dateH := s.lineHeight(1.5, lineFactor)
	timeW, dateW := width(timeFace, clock), width(dateFace, date)
	w := max(timeW, dateW)
	return block{
		w: w,
		h: timeH + dateH,
		draw: func(x, y int) {
			s.text(timeFace, clock, x+w-timeW, baseline(timeFace, y, timeH), colInk, 0)
			s.text(dateFace, date, x+w-dateW, baseline(dateFace, y+timeH, dateH), colMuted, 0)
		},
	}, true
}
