package httpd

import (
	"fmt"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// QRCodeSVG makes the QR code of text as an SVG image.
//
// Why by hand: go-qrcode can write a PNG, but a PNG needs a size in pixels, and
// the fallback screen shows the code at whatever size the layout gives it. An SVG
// is sharp at every size, it is a few hundred bytes, and the whole renderer is the
// loop below. The module map from the library does the hard part.
//
// Medium error correction is the usual choice for a code on a screen: it survives
// a camera at an angle and keeps the code small.
func QRCodeSVG(text string) ([]byte, error) {
	if text == "" {
		return nil, fmt.Errorf("there is no address to put in the QR code")
	}
	code, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return nil, fmt.Errorf("make the QR code: %w", err)
	}
	bitmap := code.Bitmap() // true is a dark module; the quiet zone is in it
	size := len(bitmap)
	if size == 0 {
		return nil, fmt.Errorf("the QR code is empty")
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" `+
		`shape-rendering="crispEdges" role="img" aria-label="QR code of the admin address">`, size, size)
	b.WriteString(`<rect width="100%" height="100%" fill="#ffffff"/><path fill="#000000" d="`)

	// One rectangle for each run of dark modules in a row. A rectangle for each
	// module would work and would be four times the size.
	for y, row := range bitmap {
		for x := 0; x < len(row); x++ {
			if !row[x] {
				continue
			}
			run := 1
			for x+run < len(row) && row[x+run] {
				run++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", x, y, run, run)
			x += run - 1
		}
	}
	b.WriteString(`"/></svg>`)
	return []byte(b.String()), nil
}
