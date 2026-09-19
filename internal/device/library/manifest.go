package library

import (
	"math/rand/v2"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/playlist"
)

// PlayerManifest is the body of GET /api/player/manifest (ARCHITECTURE section
// 7a). The daemon applies every default before it sends the manifest, and the
// daemon does the shuffle, so the SPA has no rules to know: it plays the list in
// the order that it receives.
type PlayerManifest struct {
	Fallback bool              `json:"fallback"`
	Tier     string            `json:"tier"` // low | high
	Playlist *ManifestPlaylist `json:"playlist"`
}

// ManifestPlaylist is the playlist that the player must show.
type ManifestPlaylist struct {
	Name         string         `json:"name"`
	Title        string         `json:"title"`
	Transition   string         `json:"transition"`
	TransitionMS int            `json:"transition_ms"`
	Shuffle      bool           `json:"shuffle"`
	Items        []ManifestItem `json:"items"`
}

// ManifestItem is one item that the player can show. Index is the position in
// this list, not the position in playlist.toml: the resume index that the daemon
// sends after a URL item counts in this list.
type ManifestItem struct {
	Index int    `json:"index"`
	Kind  string `json:"kind"` // image | video | url
	Name  string `json:"name"`
	Src   string `json:"src,omitempty"` // media item
	URL   string `json:"url,omitempty"` // url item

	Duration       int  `json:"duration"`
	Mute           bool `json:"mute"`
	MaxDuration    int  `json:"max_duration"`
	RefreshSeconds int  `json:"refresh_seconds,omitempty"`
}

// BuildManifest makes the manifest for one playlist.
//
// A nil playlist, or a playlist with nothing that the player can show, gives
// fallback: true. The SPA then shows the fallback screen (D18). An item that
// names a missing file, or a file kind that the player does not know, is left
// out here and shown as a warning in the admin UI.
//
// seed makes the shuffle. The caller gives a new seed each time a playlist
// starts, so the order is different at each start but stable while it plays.
func BuildManifest(p *Playlist, cfg config.Config, tier string, seed uint64) PlayerManifest {
	out := PlayerManifest{Fallback: true, Tier: tier}
	if p == nil {
		return out
	}

	items := make([]ManifestItem, 0, len(p.Items))
	for _, it := range p.Items {
		if it.Kind == playlist.KindURL {
			items = append(items, ManifestItem{
				Kind:           it.Kind,
				Name:           it.Name,
				URL:            it.URL,
				Duration:       it.Duration,
				RefreshSeconds: it.RefreshSeconds,
			})
			continue
		}
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
			Src:         it.Src,
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
		Tier:     tier,
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
