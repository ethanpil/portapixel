package config

import "strings"

// templateHeader opens the template. It tells a person who opens the file on
// another computer how to turn a setting on.
const templateHeader = `# PortaPixel settings. This file has every setting, and each one is off.
# To turn on a setting, remove the "#" at the start of its line.
# Also remove the "#" from the [table] line above it, for example [server].
# A key under the wrong [table] line does not work. The device shows a warning.
# At the first boot, the device writes this file again with all the values.
# Your values stay. If the file has a fault, the device keeps it as it is.
# See docs/settings.md for every key.

`

// Template gives the portapixel.toml that the image puts on PPMEDIA. It is
// Render(Default()) with every setting turned off by a comment mark. A person
// removes the mark from the lines that they want, on any computer, before the
// first boot.
//
// The file holds no secret. The one password in it, web.password, is the
// documented default "portapixel".
//
// Parse reads the template as Default(), and Load finds no fault in it. The
// first boot then writes the full file with the real values (see
// refreshConfigID in cmd/portapixeld/provision.go).
func Template() []byte {
	var b strings.Builder
	b.WriteString(templateHeader)
	for _, l := range strings.Split(strings.TrimSuffix(string(Render(Default())), "\n"), "\n") {
		// A blank line and a comment stay as they are. A comment that continues the
		// line above it starts with spaces. It moves two columns to the right, as
		// the comment mark moved the text of the line above it.
		switch t := strings.TrimLeft(l, " "); {
		case t == "":
		case strings.HasPrefix(t, "#"):
			if t != l {
				l = "  " + l
			}
		default:
			l = "# " + l
		}
		b.WriteString(l + "\n")
	}
	return []byte(b.String())
}
