package library

import (
	"math/rand/v2"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/playlist"
)

// PlayerManifest is what the player must show now (ARCHITECTURE section 7a). The
// daemon applies every default and does the shuffle, so the player has no rules
// to know: it gives mpv the list in the order that it receives. A nil Playlist
// is the fallback screen (D18).
//
// The manifest holds only what the player uses. The player compares two
// manifests to decide if mpv gets a new list, and a field that it does not use
// decided it too: a new title restarted the playlist at its first item.
type PlayerManifest struct {
	Playlist *ManifestPlaylist
}

// ManifestPlaylist is the playlist that the player must show.
type ManifestPlaylist struct {
	Name string
	// KenBurns is the choice of the playlist. The player decides if the output
	// can show it.
	KenBurns bool
	Items    []ManifestItem
}

// ManifestItem is one item that the player can show.
type ManifestItem struct {
	Kind string // image | video
	Name string
	// Path is the absolute path of the file. mpv reads the file directly.
	Path string

	Duration    int
	Mute        bool
	MaxDuration int

	// Transition and TransitionMS are the transition INTO this item, from the item
	// before it in this list. The first item gets the transition from the last
	// item, because the list loops. BuildManifest has applied the defaults: the
	// word and the length of the item, else those of the playlist, else those of
	// the device.
	Transition   string
	TransitionMS int
}

// BuildManifest makes the manifest for one playlist.
//
// A nil playlist, or a playlist with nothing that the player can show, gives no
// playlist. The player then shows the fallback screen (D18). An item that
// names a missing file, or a file kind that the player does not know, is left
// out here and shown as a warning in the admin UI.
//
// seed makes the shuffle. The caller gives a new seed each time a playlist
// starts, so the order is different at each start but stable while it plays.
func BuildManifest(p *Playlist, cfg config.Config, seed uint64) PlayerManifest {
	if p == nil {
		return PlayerManifest{}
	}

	transition := cfg.Playback.Transition
	if p.Transition != "" {
		transition = p.Transition
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
		// An item can name its own transition and its own length. Each one falls
		// back by itself: an item with a length and no word changes the length of
		// the transition of the playlist.
		itemTransition, itemMS := transition, cfg.Playback.TransitionMS
		if it.Transition != "" {
			itemTransition = it.Transition
		}
		if it.TransitionMS > 0 {
			itemMS = it.TransitionMS
		}
		items = append(items, ManifestItem{
			Kind:         it.Kind,
			Name:         it.Name,
			Path:         it.path,
			Duration:     duration,
			Mute:         it.Mute,
			MaxDuration:  it.MaxDuration,
			Transition:   itemTransition,
			TransitionMS: itemMS,
		})
	}
	if len(items) == 0 {
		return PlayerManifest{}
	}

	shuffle := cfg.Playback.Shuffle
	if p.Shuffle != nil {
		shuffle = *p.Shuffle
	}
	if shuffle && len(items) > 1 {
		r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		r.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	}
	return PlayerManifest{Playlist: &ManifestPlaylist{Name: p.Name, KenBurns: p.KenBurns, Items: items}}
}
