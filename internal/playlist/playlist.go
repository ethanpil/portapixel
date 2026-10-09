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
	// KenBurns turns on a slow zoom and pan on each image of the playlist while
	// it shows. The device has no setting for it: it is a choice of the playlist.
	KenBurns bool `toml:"ken_burns,omitempty" json:"ken_burns,omitempty"`
}

// Item is one [[item]] table: one image or video file.
type Item struct {
	File        string `toml:"file,omitempty" json:"file,omitempty"`
	Duration    int    `toml:"duration,omitempty" json:"duration,omitempty"`
	Mute        bool   `toml:"mute,omitempty" json:"mute,omitempty"`
	MaxDuration int    `toml:"max_duration,omitempty" json:"max_duration,omitempty"`
	// Transition is the transition INTO this item, from the item before it. For
	// the first item, that is the item at the end of the list. TransitionMS is
	// its length. An empty Transition and a zero TransitionMS mean "use the
	// value of the playlist, and then of the device". The two values are
	// independent: a length with no word changes the length only.
	Transition   string `toml:"transition,omitempty" json:"transition,omitempty"`
	TransitionMS int    `toml:"transition_ms,omitempty" json:"transition_ms,omitempty"`
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
		if it.File == "" {
			add(field, "needs a file")
		} else if msg := badFilePath(it.File, opt); msg != "" {
			add(field+".file", msg)
		}
		if it.Duration < 0 {
			add(field+".duration", "must not be less than zero")
		}
		if it.MaxDuration < 0 {
			add(field+".max_duration", "must not be less than zero")
		}
		if it.Transition != "" && !config.IsTransition(it.Transition) {
			add(field+".transition", "must be one of "+strings.Join(config.Transitions, ", "))
		}
		if it.TransitionMS < 0 {
			add(field+".transition_ms", "must not be less than zero")
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

// FleetRefPrefix is the one shape of a file reference in a fleet playlist:
// ../media/<name>, which points at the shared object store beside the playlist
// directory. exFAT has no hard links, so the playlist names the object by a path.
//
// The writer and the reader of that shape are two packages, and each one had the
// text of the path in it. One spelling here, so the two cannot drift.
const FleetRefPrefix = "../media/"

// FleetRef gives the reference of one object of the fleet store.
func FleetRef(objectName string) string { return FleetRefPrefix + objectName }

// isFleetRef reports if the path is the one shape that a fleet playlist may use.
func isFleetRef(file string) bool {
	parts := strings.Split(file, "/")
	if len(parts) != 3 {
		return false
	}
	return parts[0]+"/"+parts[1]+"/" == FleetRefPrefix && parts[2] != "" && parts[2] != ".."
}
