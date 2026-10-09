package playlist

import (
	"strconv"
	"strings"
)

// commentCol is the column where a comment starts. It keeps the file easy to
// read for the person who edits it by hand.
const commentCol = 27

// Render writes the canonical form of a playlist. The stock comments always
// come back. Comments that the user wrote are lost, and the UI says so before
// it saves (D15).
//
// Render is stable: parse the output and render it again, and the bytes are the
// same.
func Render(p Playlist) []byte {
	var b strings.Builder
	b.WriteString("# PortaPixel playlist. The media files are beside this file.\n")
	b.WriteString("# The web UI writes this file again when you save. These comments come back,\n")
	b.WriteString("# your comments do not.\n")
	b.WriteString("\n[playlist]\n")
	writeLine(&b, "name", quote(p.Meta.Name), "The title. Empty means the name of the directory.")

	if p.Meta.Shuffle != nil {
		writeLine(&b, "shuffle", boolText(*p.Meta.Shuffle), "Replaces [playback].shuffle.")
	} else {
		b.WriteString("# shuffle = true           # Replaces [playback].shuffle.\n")
	}
	if p.Meta.Transition != "" {
		writeLine(&b, "transition", quote(p.Meta.Transition), "Replaces [playback].transition.")
	} else {
		b.WriteString("# transition = \"cut\"       # Replaces [playback].transition.\n")
	}
	if p.Meta.KenBurns {
		writeLine(&b, "ken_burns", "true", "A slow zoom and pan on each image.")
	} else {
		b.WriteString("# ken_burns = true         # A slow zoom and pan on each image.\n")
	}

	for _, it := range p.Items {
		b.WriteString("\n[[item]]\n")
		renderItem(&b, it)
	}
	return []byte(b.String())
}

func renderItem(b *strings.Builder, it Item) {
	writeLine(b, "file", quote(it.File), "")
	if it.Duration > 0 {
		writeLine(b, "duration", strconv.Itoa(it.Duration), "Seconds. Images only.")
	}
	if Kind(it) == KindVideo {
		writeLine(b, "mute", boolText(it.Mute), "Set true to silence this video.")
		if it.MaxDuration > 0 {
			writeLine(b, "max_duration", strconv.Itoa(it.MaxDuration), "Stop the video after N seconds.")
		} else {
			b.WriteString("# max_duration = 60        # Stop the video after N seconds.\n")
		}
	} else {
		if it.Mute {
			writeLine(b, "mute", "true", "")
		}
		if it.MaxDuration > 0 {
			writeLine(b, "max_duration", strconv.Itoa(it.MaxDuration), "Stop the item after N seconds.")
		}
	}
	// The two transition keys come only when they have a value. A comment for
	// them on each item would make a long file for a feature that most items
	// do not use.
	if it.Transition != "" {
		writeLine(b, "transition", quote(it.Transition), "The transition into this item.")
	}
	if it.TransitionMS > 0 {
		writeLine(b, "transition_ms", strconv.Itoa(it.TransitionMS), "The length of that transition.")
	}
}

// writeLine writes "key = value" and puts the comment at commentCol.
func writeLine(b *strings.Builder, key, value, comment string) {
	line := key + " = " + value
	if comment == "" {
		b.WriteString(line + "\n")
		return
	}
	pad := commentCol - len(line)
	if pad < 1 {
		pad = 1
	}
	b.WriteString(line + strings.Repeat(" ", pad) + "# " + comment + "\n")
}

// quote makes a TOML basic string. It gives back the characters that TOML needs
// as escapes, so a file name with a quotation mark or a backslash survives a
// write and a read.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteString(`\u`)
				const hex = "0123456789abcdef"
				b.WriteByte('0')
				b.WriteByte('0')
				b.WriteByte(hex[(r>>4)&0xf])
				b.WriteByte(hex[r&0xf])
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
