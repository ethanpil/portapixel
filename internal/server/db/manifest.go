package db

import (
	"errors"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// ManifestOptions carries the values that the manifest needs and that the
// database does not hold.
type ManifestOptions struct {
	// ServerName is the name that the device shows for this server.
	ServerName string
	// DefaultPoll is the poll interval of a device with no value of its own.
	DefaultPoll int
	// MediaBase and ReleaseBase are the URL paths of the two file routes. The
	// device joins them to the base URL of the server.
	MediaBase   string
	ReleaseBase string
	// Release is the approved release, or nil when no release goes out. The
	// caller reads it once per poll from a cached value, so the hot path costs no
	// query for it.
	Release *Release
}

// Manifest builds everything that one device must do (contract section 5).
//
// This is the resolution rule of the fleet, and it lives here so that a route
// holds no part of it:
//
//   - A rule of the device wins. A device that has one rule of its own uses
//     only its own rules; the rules of its group then do not apply. Without that
//     rule an override would add to the group and not replace it, and a screen
//     that the admin moved off the group loop would still play it.
//   - The rules go out in priority order, lowest number first. The device takes
//     the first rule that matches (D17).
//   - The default playlist of the device wins over the default of the group.
//   - The screen power rule of the device wins over the rule of the group (D48
//     gives screen power to the server).
//   - The release goes out only when the admin approved it, the mirror holds
//     every verified file, and the device does not run it yet (D28).
//   - An item whose media row went away is left out, with its media entry. A
//     device cannot name, size or fetch such an item, and an item with a hash and
//     no media entry would be a download that always fails.
//
// Manifest also takes the queued commands and marks them as delivered, so one
// poll gives one delivery (D24).
//
// The cost of one poll is a fixed handful of queries: the group, the rules, one
// query for each playlist that a rule names, and one write for the commands. The
// media rows come out of the same query as the items, because a fleet of 200
// screens with a 50-item loop would otherwise cost about 10000 queries a minute
// on one write connection.
func (d *DB) Manifest(dev Device, opt ManifestOptions) (manifest.Manifest, error) {
	m := manifest.Manifest{
		ServerName:  opt.ServerName,
		PollSeconds: dev.PollSeconds,
		Playlists:   []manifest.Playlist{},
		Schedule:    []manifest.Rule{},
		Media:       []manifest.MediaRef{},
		Commands:    []manifest.Command{},
	}
	if m.PollSeconds <= 0 {
		m.PollSeconds = opt.DefaultPoll
	}

	group, hasGroup, err := d.deviceGroup(dev)
	if err != nil {
		return m, err
	}

	// The rules. A device rule replaces the group rules.
	rules, err := d.Assignments(0, dev.ID)
	if err != nil {
		return m, err
	}
	if len(rules) == 0 && hasGroup {
		if rules, err = d.Assignments(group.ID, ""); err != nil {
			return m, err
		}
	}

	// The default playlist.
	defaultID := dev.DefaultPlaylistID
	if defaultID == 0 && hasGroup {
		defaultID = group.DefaultPlaylistID
	}

	// Load each playlist one time, whatever number of rules name it.
	wanted := make([]int64, 0, len(rules)+1)
	if defaultID != 0 {
		wanted = append(wanted, defaultID)
	}
	for _, r := range rules {
		wanted = append(wanted, r.PlaylistID)
	}
	loaded := map[int64]Playlist{}
	for _, id := range wanted {
		if _, ok := loaded[id]; ok {
			continue
		}
		p, err := d.PlaylistNoCount(id)
		if errors.Is(err, ErrNotFound) {
			// A playlist that went away between two queries. Leave it out: a
			// missing playlist must not stop the whole manifest.
			continue
		}
		if err != nil {
			return m, err
		}
		loaded[id] = p
	}

	// The playlists go out in a fixed order: the default first, then the rules.
	// The device writes one directory for each of them.
	seen := map[int64]bool{}
	mediaSeen := map[string]bool{}
	for _, id := range wanted {
		p, ok := loaded[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true

		out := manifest.Playlist{
			Name:       p.Name,
			Title:      p.Title,
			Transition: p.Transition,
			Shuffle:    p.Shuffle,
			Items:      make([]manifest.Item, 0, len(p.Items)),
		}
		for _, it := range p.Items {
			if it.SHA256 != "" && !it.MediaRow {
				// The media row went away. The item goes with it.
				continue
			}
			out.Items = append(out.Items, manifest.Item{
				SHA256:         it.SHA256,
				URL:            it.URL,
				Duration:       it.Duration,
				Mute:           it.Mute,
				MaxDuration:    it.MaxDuration,
				RefreshSeconds: it.RefreshSeconds,
			})
			if it.SHA256 == "" || mediaSeen[it.SHA256] {
				continue
			}
			mediaSeen[it.SHA256] = true
			name := it.MediaName
			if name == "" {
				name = it.Name
			}
			m.Media = append(m.Media, manifest.MediaRef{
				SHA256: it.SHA256,
				Size:   it.Size,
				Name:   name,
				URL:    opt.MediaBase + it.SHA256,
			})
		}
		m.Playlists = append(m.Playlists, out)
	}

	if p, ok := loaded[defaultID]; ok {
		m.DefaultPlaylist = p.Name
	}
	for _, r := range rules {
		p, ok := loaded[r.PlaylistID]
		if !ok {
			continue
		}
		m.Schedule = append(m.Schedule, manifest.Rule{
			Playlist: p.Name,
			Days:     r.Days,
			Start:    r.Start,
			End:      r.End,
		})
	}

	// The screen power rule.
	on, off, days := dev.ScreenOn, dev.ScreenOff, dev.ScreenDays
	if on == "" && off == "" && hasGroup {
		on, off, days = group.ScreenOn, group.ScreenOff, group.ScreenDays
	}
	if on != "" || off != "" {
		m.Screen = &manifest.ScreenRule{OnTime: on, OffTime: off, Days: splitDays(days)}
	}

	// The release. The mirror state is the one answer: a release reaches a device
	// when every file is there and verified.
	if rel := opt.Release; rel != nil && rel.Approved &&
		rel.MirrorState == MirrorDone && rel.Version != dev.Version {
		m.Release = &manifest.ReleaseRef{Version: rel.Version, BaseURL: opt.ReleaseBase + rel.Version}
	}

	// The commands. This is the one write of the manifest path.
	commands, err := d.TakeCommands(dev.ID)
	if err != nil {
		return m, err
	}
	for _, c := range commands {
		m.Commands = append(m.Commands, manifest.Command{ID: c.ID, Type: c.Type, Args: c.Args})
	}
	return m, nil
}

// deviceGroup gives the group of a device.
func (d *DB) deviceGroup(dev Device) (Group, bool, error) {
	if dev.GroupID == 0 {
		return Group{}, false, nil
	}
	g, err := d.Group(dev.GroupID)
	if errors.Is(err, ErrNotFound) {
		return Group{}, false, nil
	}
	if err != nil {
		return Group{}, false, err
	}
	return g, true, nil
}
