package config

import (
	"strconv"
	"strings"
)

// commentCol is the column where a comment starts. It keeps the file easy to
// read for the person who edits it by hand.
const commentCol = 27

// Render writes the canonical form of portapixel.toml with the values of cfg.
// Every key that the system honours is in the output. A key that is not set and
// is optional comes out as a comment, so the reader can see that the key exists.
//
// Render is stable: parse the output and render it again, and the bytes are the
// same. The first boot writes Render(Default()).
func Render(cfg Config) []byte {
	var b strings.Builder

	b.WriteString("# PortaPixel device configuration.\n")
	b.WriteString("# You can edit this file by hand. Every computer can read this partition.\n")
	b.WriteString("# You can also use the web UI at http://portapixel.local/ .\n")
	b.WriteString("# A save from the web UI writes this file again. These comments come back.\n")
	b.WriteString("# Your own comments do not come back.\n")

	b.WriteString("\n[device]\n")
	line(&b, "name", quote(cfg.Device.Name), "The display name. mDNS uses it in lower case.")
	line(&b, "id", quote(cfg.Device.ID), "Comes from the hardware at each boot. An edit does nothing.")
	line(&b, "timezone", quote(cfg.Device.Timezone), "IANA name, for example \"America/New_York\".")
	comment(&b, "The schedules use it. The web UI asks you to set it.")
	line(&b, "tier", quote(cfg.Device.Tier), "auto | low | high. low degrades transitions and adds zram.")

	b.WriteString("\n[network]\n")
	line(&b, "mode", quote(cfg.Network.Mode), "dhcp | static")
	b.WriteString("# Set the three keys below when mode is static.\n")
	optional(&b, "address", quote(cfg.Network.Address), cfg.Network.Address != "", `"192.168.1.50/24"`)
	optional(&b, "gateway", quote(cfg.Network.Gateway), cfg.Network.Gateway != "", `"192.168.1.1"`)
	optional(&b, "dns", list(cfg.Network.DNS), len(cfg.Network.DNS) > 0, `["192.168.1.1", "1.1.1.1"]`)
	line(&b, "wifi_ssid", quote(cfg.Network.WifiSSID), "Empty for a wired network.")
	line(&b, "wifi_psk", quote(cfg.Network.WifiPSK), "")

	b.WriteString("\n[display]\n")
	line(&b, "rotation", strconv.Itoa(cfg.Display.Rotation), "0 | 90 | 180 | 270")
	b.WriteString("# video_mode forces the output mode when the display or an HDMI splitter\n")
	b.WriteString("# gives a bad EDID. Leave it out to trust the display.\n")
	optional(&b, "video_mode", quote(cfg.Display.VideoMode), cfg.Display.VideoMode != "", `"1920x1080@60"`)
	line(&b, "power_method", quote(cfg.Display.PowerMethod), "auto | cec | dpms | none")
	b.WriteString("# Set both times for a screen schedule. Leave both out to keep the screen on.\n")
	optional(&b, "on_time", quote(cfg.Display.OnTime), cfg.Display.OnTime != "", `"07:30"`)
	optional(&b, "off_time", quote(cfg.Display.OffTime), cfg.Display.OffTime != "", `"22:00"`)
	optional(&b, "power_days", list(cfg.Display.PowerDays), len(cfg.Display.PowerDays) > 0,
		`["mon", "tue", "wed", "thu", "fri", "sat", "sun"]`)

	b.WriteString("\n[audio]\n")
	line(&b, "output", quote(cfg.Audio.Output), "auto (HDMI first) | hdmi | analog | usb")
	line(&b, "volume", strconv.Itoa(cfg.Audio.Volume), "0 to 100")

	b.WriteString("\n[playback]\n")
	line(&b, "default_playlist", quote(cfg.Playback.DefaultPlaylist), "")
	line(&b, "transition", quote(cfg.Playback.Transition), strings.Join(Transitions, " | "))
	line(&b, "transition_ms", strconv.Itoa(cfg.Playback.TransitionMS), "")
	line(&b, "image_duration", strconv.Itoa(cfg.Playback.ImageDuration), "Seconds for an image that has no duration.")
	line(&b, "shuffle", boolText(cfg.Playback.Shuffle), "")
	line(&b, "nightly_restart", quote(cfg.Playback.NightlyRestart), "The daily browser restart. \"\" stops it.")

	b.WriteString("\n# Playlist schedule rules. The first rule that matches wins. If no rule\n")
	b.WriteString("# matches, default_playlist plays. A rule with no days matches every day.\n")
	if len(cfg.Schedule) == 0 {
		b.WriteString("# [[schedule]]\n")
		b.WriteString("# playlist = \"weekday-loop\"\n")
		b.WriteString("# days = [\"mon\", \"tue\", \"wed\", \"thu\", \"fri\"]\n")
		b.WriteString("# start = \"08:00\"\n")
		b.WriteString("# end = \"18:00\"\n")
	}
	for _, r := range cfg.Schedule {
		b.WriteString("\n[[schedule]]\n")
		line(&b, "playlist", quote(r.Playlist), "")
		if len(r.Days) > 0 {
			line(&b, "days", list(r.Days), "")
		}
		line(&b, "start", quote(r.Start), "")
		line(&b, "end", quote(r.End), "")
	}

	b.WriteString("\n[server]\n")
	line(&b, "url", quote(cfg.Server.URL), "The fleet server. Empty keeps the device standalone.")
	line(&b, "token", quote(cfg.Server.Token), "An enrollment token or a device token.")
	comment(&b, "Leave it empty to pair by code in the web UI.")
	line(&b, "poll_seconds", strconv.Itoa(cfg.Server.PollSeconds), "10 or more")

	b.WriteString("\n[web]\n")
	line(&b, "port", strconv.Itoa(cfg.Web.Port), "")
	line(&b, "password", quote(cfg.Web.Password), "The local admin password. Change it.")

	b.WriteString("\n[ssh]\n")
	line(&b, "enabled", boolText(cfg.SSH.Enabled), "The first boot sets the root password. Change it.")

	b.WriteString("\n[updates]\n")
	line(&b, "auto", boolText(cfg.Updates.Auto), "false: you approve each update. This is the safe value.")

	b.WriteString("\n[logging]\n")
	line(&b, "persist", boolText(cfg.Logging.Persist), "true writes the system log to the disk. It adds wear.")

	return []byte(b.String())
}

// line writes "key = value" and puts the comment at commentCol.
func line(b *strings.Builder, key, value, text string) {
	s := key + " = " + value
	if text == "" {
		b.WriteString(s + "\n")
		return
	}
	b.WriteString(s + pad(len(s)) + "# " + text + "\n")
}

// optional writes a key that the user can leave out. When the key is set it
// comes out as a live key. When it is not set it comes out as a comment with an
// example value, so that the reader learns that the key exists.
func optional(b *strings.Builder, key, value string, set bool, example string) {
	if set {
		line(b, key, value, "")
		return
	}
	b.WriteString("# " + key + " = " + example + "\n")
}

// comment writes a comment line that continues the line above it.
func comment(b *strings.Builder, text string) {
	b.WriteString(strings.Repeat(" ", commentCol) + "# " + text + "\n")
}

// pad gives the spaces that put a comment at commentCol. A line that is already
// longer gets one space.
func pad(length int) string {
	if n := commentCol - length; n > 0 {
		return strings.Repeat(" ", n)
	}
	return " "
}

// list makes a TOML array of strings.
func list(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = quote(v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// quote makes a TOML basic string. It escapes the characters that would
// otherwise end the string or change its meaning, so a password with a
// quotation mark or a backslash survives a write and a read.
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
				const hex = "0123456789abcdef"
				b.WriteString(`\u00`)
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
