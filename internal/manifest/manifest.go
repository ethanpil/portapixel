package manifest

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// EnrollRequest asks the server to pair this device. It is the only device call
// that carries no bearer token.
type EnrollRequest struct {
	DeviceID   string `json:"device_id"`   // px-xxxxxxxx
	HardwareID string `json:"hardware_id"` // full SHA-256 hex of the hardware source
	Name       string `json:"name"`
	Token      string `json:"token"` // enrollment token, device token, or "" for code pairing
	Version    string `json:"version"`
}

// EnrollResponse answers an EnrollRequest. A pending device calls enroll again
// with the same ClaimSecret in Token until the status is "paired".
type EnrollResponse struct {
	Status      string `json:"status"`       // "paired" | "pending"
	DeviceToken string `json:"device_token"` // set when paired
	PairingCode string `json:"pairing_code"` // set when pending by code; 6 chars, no 0/O/1/I
	ClaimSecret string `json:"claim_secret"` // device keeps it; sends it again to poll a pending enroll
}

// Manifest is everything that the server wants this device to do. The device
// gets it from GET /api/v1/manifest on each poll.
type Manifest struct {
	ServerName      string      `json:"server_name"`
	PollSeconds     int         `json:"poll_seconds"`
	Playlists       []Playlist  `json:"playlists"`
	DefaultPlaylist string      `json:"default_playlist"`
	Schedule        []Rule      `json:"schedule"`
	Media           []MediaRef  `json:"media"`
	Commands        []Command   `json:"commands"`
	Screen          *ScreenRule `json:"screen,omitempty"`
	Release         *ReleaseRef `json:"release,omitempty"`
}

// Playlist is one fleet playlist. The device writes it to
// _fleet/<name>/playlist.toml.
type Playlist struct {
	Name       string `json:"name"` // directory-safe slug
	Title      string `json:"title"`
	Transition string `json:"transition,omitempty"`
	Shuffle    *bool  `json:"shuffle,omitempty"`
	Items      []Item `json:"items"`
}

// Item is one entry of a fleet playlist. It is a media item or a URL item.
type Item struct {
	SHA256         string `json:"sha256,omitempty"` // media item
	URL            string `json:"url,omitempty"`    // url item
	Duration       int    `json:"duration,omitempty"`
	Mute           bool   `json:"mute,omitempty"`
	MaxDuration    int    `json:"max_duration,omitempty"`
	RefreshSeconds int    `json:"refresh_seconds,omitempty"`
}

// Rule is one schedule rule. The first rule that matches wins.
type Rule struct {
	Playlist string   `json:"playlist"`
	Days     []string `json:"days"`  // "mon".."sun"; empty = all days
	Start    string   `json:"start"` // "HH:MM"
	End      string   `json:"end"`
}

// MediaRef names one object that the device must hold.
type MediaRef struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Name   string `json:"name"`
	URL    string `json:"url"` // path relative to the server base URL
}

// Command is one queued remote command.
//
// Only rename has an argument: Args["name"] is the new display name. It must pass
// CleanName on both ends.
type Command struct {
	ID   int64             `json:"id"`
	Type string            `json:"type"` // reboot | restart-browser | screen-on | screen-off | rescan | update | rename
	Args map[string]string `json:"args,omitempty"`
}

// MaxNameLength is the longest display name of a screen, in characters. The mDNS
// name of the device comes from it, and a DNS label holds 63 octets at most. A
// label of 64 octets gives an announcement that answers no query.
const MaxNameLength = 63

// NameRule is the rule of CleanName in words. A field error and an ops log line
// use it, so the words and the rule change together.
var NameRule = fmt.Sprintf("1 to %d characters, with no control character and no invisible character",
	MaxNameLength)

// CleanName gives the display name of a screen in its one correct form. It
// removes the spaces at the two ends. ok is false for a name that is empty, that
// is longer than MaxNameLength characters, that is not UTF-8 or that holds a
// character that hiddenInName refuses.
//
// The server applies it to a rename command, to an enroll request and to the
// name in a heartbeat. The device applies it to a rename command and to
// device.name in portapixel.toml. One rule in one function keeps the two ends in
// agreement.
func CleanName(raw string) (name string, ok bool) {
	name = strings.TrimSpace(raw)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxNameLength {
		return "", false
	}
	for _, r := range name {
		if hiddenInName(r) {
			return "", false
		}
	}
	return name, true
}

// hiddenInName reports if a name must not hold r. Each such character is a
// control character, or a person cannot see it:
//
//   - Cc, the control characters.
//   - Zl and Zp, the line and paragraph separators.
//   - The bidi controls. They can show the device ID beside the name in the
//     wrong order.
//   - U+200B, U+2060, U+FEFF and U+00AD. They show nothing, so a typed name
//     does not agree with a name that holds one.
//
// U+200C and U+200D (ZWNJ and ZWJ) stay permitted. Persian, the Indic scripts
// and emoji need them.
func hiddenInName(r rune) bool {
	switch {
	case unicode.IsControl(r), unicode.In(r, unicode.Zl, unicode.Zp):
		return true
	case r == '\u200E', r == '\u200F', r >= '\u202A' && r <= '\u202E', r >= '\u2066' && r <= '\u2069':
		return true
	case r == '\u200B', r == '\u2060', r == '\uFEFF', r == '\u00AD':
		return true
	}
	return false
}

// ScreenRule is the screen power schedule that the server manages.
type ScreenRule struct {
	OnTime  string   `json:"on_time"`
	OffTime string   `json:"off_time"`
	Days    []string `json:"days"`
}

// ReleaseRef names the release that the server approved.
type ReleaseRef struct {
	Version string `json:"version"`
	BaseURL string `json:"base_url"` // mirror path; files: portapixeld-<arch>, .minisig, SHA256SUMS
}

// Heartbeat is the device report that follows each poll.
type Heartbeat struct {
	DeviceID   string  `json:"device_id"`
	HardwareID string  `json:"hardware_id"`
	Name       string  `json:"name"`
	Version    string  `json:"version"`
	Status     Status  `json:"status"`
	Acks       []int64 `json:"acks"`
	SyncError  string  `json:"sync_error,omitempty"` // for example "needs 4.2 GB, has 1.1 GB"
}
