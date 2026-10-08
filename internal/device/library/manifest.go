package library

import (
	"math/rand/v2"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/playlist"
)

// PlayerManifest is what the player must show now (ARCHITECTURE section 7a). The
// daemon applies every default and does the shuffle, so the player has no rules
// to know: it gives mpv the list in the order that it receives.
type PlayerManifest struct {
	Fallback bool
	Playlist *ManifestPlaylist
}

// ManifestPlaylist is the playlist that the player must show.
type ManifestPlaylist struct {
	Name         string
	Title        string
	Transition   string
	TransitionMS int
	Shuffle      bool
	Items        []ManifestItem
}

// ManifestItem is one item that the player can show. Index is the position in
// this list, not the position in playlist.toml.
type ManifestItem struct {
	Index int
	Kind  string // image | video
	Name  string
	// Path is the absolute path of the file. mpv reads the file directly.
	Path string

	Duration    int
	Mute        bool
	MaxDuration int
}

// BuildManifest makes the manifest for one playlist.
//
// A nil playlist, or a playlist with nothing that the player can show, gives
// Fallback: true. The player then shows the fallback screen (D18). An item that
// names a missing file, or a file kind that the player does not know, is left
// out here and shown as a warning in the admin UI.
//
// seed makes the shuffle. The caller gives a new seed each time a playlist
// starts, so the order is different at each start but stable while it plays.
func BuildManifest(p *Playlist, cfg config.Config, seed uint64) PlayerManifest {
	out := PlayerManifest{Fallback: true}
	if p == nil {
		return out
	}

	items := make([]ManifestItem, 0, len(p.Items))
	for _, it := range p.Items {
		if it.Missing || it.Kind == playlist.KindUnknown {
			continue
		}
		duration := it.Duration
		if it.Kind == playlist.KindImage && duration <= 0 {
			duration = cfg.Playback.ImageDuration
		}
		items = append(items, ManifestItem{
			Kind:        it.Kind,
			Name:        it.Name,
			Path:        it.path,
			Duration:    duration,
			Mute:        it.Mute,
			MaxDuration: it.MaxDuration,
		})
	}
	if len(items) == 0 {
		return out
	}

	shuffle := cfg.Playback.Shuffle
	if p.Shuffle != nil {
		shuffle = *p.Shuffle
	}
	if shuffle && len(items) > 1 {
		r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		r.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	}
	for i := range items {
		items[i].Index = i
	}

	transition := cfg.Playback.Transition
	if p.Transition != "" {
		transition = p.Transition
	}
	return PlayerManifest{
		Fallback: false,
		Playlist: &ManifestPlaylist{
			Name:         p.Name,
			Title:        p.Title,
			Transition:   transition,
			TransitionMS: cfg.Playback.TransitionMS,
			Shuffle:      shuffle,
			Items:        items,
		},
	}
}
