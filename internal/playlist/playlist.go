package playlist

import (
	"fmt"
	"path"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/ethanpil/portapixel/internal/config"
)

// FileName is the name of the file that makes a directory a playlist.
const FileName = "playlist.toml"

// Playlist is one playlist.toml file.
type Playlist struct {
	Meta  Meta   `toml:"playlist" json:"playlist"`
	Items []Item `toml:"item" json:"items"`
}

// Meta is the [playlist] table.
type Meta struct {
	// Name is the title of the playlist. An empty name means "use the name of
	// the directory".
	Name string `toml:"name" json:"name"`
	// Shuffle and Transition replace the values in [playback] of
	// portapixel.toml. A nil or empty value means "use the device value".
	Shuffle    *bool  `toml:"shuffle,omitempty" json:"shuffle,omitempty"`
	Transition string `toml:"transition,omitempty" json:"transition,omitempty"`
}

// Item is one [[item]] table. It holds a file or a URL, never both.
type Item struct {
	File           string `toml:"file,omitempty" json:"file,omitempty"`
	URL            string `toml:"url,omitempty" json:"url,omitempty"`
	Duration       int    `toml:"duration,omitempty" json:"duration,omitempty"`
	Mute           bool   `toml:"mute,omitempty" json:"mute,omitempty"`
	MaxDuration    int    `toml:"max_duration,omitempty" json:"max_duration,omitempty"`
	RefreshSeconds int    `toml:"refresh_seconds,omitempty" json:"refresh_seconds,omitempty"`
}

// Options changes what Parse and Validate permit.
type Options struct {
	// AllowFleetRefs permits a file path in the form ../media/<name>. The fleet
	// client writes playlists under _fleet/<playlist>/ that point at the shared
	// object store in _fleet/media/ (D24). exFAT has no hard links, so the
	// reference is a path.
	AllowFleetRefs bool
}

// FieldError is one thing that is wrong with a playlist file.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// Errors is the list of things that are wrong with a playlist file.
type Errors []FieldError

func (e Errors) Error() string {
	parts := make([]string, len(e))
	for i, fe := range e {
		parts[i] = fe.Error()
	}
	return strings.Join(parts, "; ")
}

// Parse reads a playlist.toml and checks it. It gives an error for bad TOML and
// for a file that breaks a rule. It never panics.
func Parse(data []byte, opt Options) (Playlist, error) {
	var p Playlist
	if err := toml.Unmarshal(data, &p); err != nil {
		return Playlist{}, fmt.Errorf("bad playlist file: %w", err)
	}
	if errs := p.Validate(opt); len(errs) > 0 {
		return p, errs
	}
	return p, nil
}

// Validate gives every rule that the playlist breaks. An empty list means that
// the playlist is good.
func (p Playlist) Validate(opt Options) Errors {
	var errs Errors
	add := func(field, message string) {
		errs = append(errs, FieldError{Field: field, Message: message})
	}

	if p.Meta.Transition != "" && !config.IsTransition(p.Meta.Transition) {
		add("playlist.transition", "must be one of "+strings.Join(config.Transitions, ", "))
	}
	if len(p.Items) == 0 {
		add("item", "a playlist needs one item or more")
	}

	for i, it := range p.Items {
		field := fmt.Sprintf("item[%d]", i)
		switch {
		case it.File == "" && it.URL == "":
			add(field, "needs a file or a url")
		case it.File != "" && it.URL != "":
			add(field, "has a file and a url; use one of them")
		case it.File != "":
			if msg := badFilePath(it.File, opt); msg != "" {
				add(field+".file", msg)
			}
			if it.RefreshSeconds != 0 {
				add(field+".refresh_seconds", "belongs to a url item")
			}
		case it.URL != "":
			if !strings.HasPrefix(it.URL, "http://") && !strings.HasPrefix(it.URL, "https://") {
				add(field+".url", "must start with http:// or https://")
			}
			if it.MaxDuration != 0 {
				add(field+".max_duration", "belongs to a file item; a url item uses duration")
			}
			if it.Duration <= 0 && len(p.Items) > 1 {
				add(field+".duration", "a url item in a playlist of more than one item needs a duration")
			}
		}
		if it.Duration < 0 {
			add(field+".duration", "must not be less than zero")
		}
		if it.MaxDuration < 0 {
			add(field+".max_duration", "must not be less than zero")
		}
		if it.RefreshSeconds < 0 {
			add(field+".refresh_seconds", "must not be less than zero")
		}
	}
	return errs
}

// badFilePath says why a file reference is not permitted, or "" when it is good.
// The rule is strict, because a playlist file is hand-edited and the daemon runs
// as root: a path must stay inside the playlist directory.
func badFilePath(file string, opt Options) string {
	if strings.Contains(file, `\`) {
		return "must use / between directory names"
	}
	if strings.HasPrefix(file, "/") || strings.Contains(file, ":") {
		return "must be a relative path"
	}
	if path.Clean(file) != file {
		return "must be a clean path, for example media/a.jpg"
	}
	if hasParentStep(file) {
		if opt.AllowFleetRefs && isFleetRef(file) {
			return ""
		}
		return "must not go outside the playlist directory"
	}
	return ""
}

// hasParentStep reports if any element of the path is "..".
func hasParentStep(file string) bool {
	for _, part := range strings.Split(file, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// isFleetRef reports if the path is the one shape that a fleet playlist may
// use: ../media/<name>, which points at the shared object store.
func isFleetRef(file string) bool {
	parts := strings.Split(file, "/")
	if len(parts) != 3 {
		return false
	}
	return parts[0] == ".." && parts[1] == "media" && parts[2] != "" && parts[2] != ".."
}

// IsKiosk reports if this playlist is the single-URL kiosk mode (D42). The
// daemon then parks the browser on the page and never uses the player SPA.
func (p Playlist) IsKiosk() bool {
	return len(p.Items) == 1 && p.Items[0].URL != "" && p.Items[0].File == ""
}
