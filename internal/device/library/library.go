package library

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/playlist"
)

// FleetDir is the directory that holds the playlists and the objects of the
// fleet server. FleetMediaDir holds the objects themselves.
const (
	FleetDir      = "_fleet"
	FleetMediaDir = "media"
)

// MediaURLPrefix is the URL space that serves the media root.
const MediaURLPrefix = "/media/"

// Item is one entry of a playlist, with the facts that the scan found.
type Item struct {
	Index int    `json:"index"`
	Kind  string `json:"kind"` // image | video | url | unknown
	// Name is what the admin UI shows: the file name or the URL.
	Name string `json:"name"`
	// File is the path that the playlist gave, relative to the playlist
	// directory. It is empty for a URL item.
	File string `json:"file,omitempty"`
	URL  string `json:"url,omitempty"`
	// Src is the URL that serves the file, for example
	// /media/default/welcome.jpg. It is empty for a URL item.
	Src string `json:"src,omitempty"`

	Duration       int  `json:"duration"`
	Mute           bool `json:"mute"`
	MaxDuration    int  `json:"max_duration"`
	RefreshSeconds int  `json:"refresh_seconds"`

	Size    int64  `json:"size"`
	SHA256  string `json:"sha256,omitempty"` // empty until the background hash finishes
	Missing bool   `json:"missing"`
	// Warning is a sentence for the admin UI, or "" when the item is good.
	Warning string `json:"warning,omitempty"`

	// path is the absolute path of the file and modNS is its modification time.
	// The hash cache needs both. They stay inside this package.
	path  string
	modNS int64
}

// Playlist is one directory that holds a playlist.toml.
type Playlist struct {
	// Name is the directory name. It is also the name in the API and in the
	// schedule rules.
	Name string `json:"name"`
	// Title is the name in the [playlist] table, or the directory name.
	Title      string `json:"title"`
	Transition string `json:"transition,omitempty"`
	Shuffle    *bool  `json:"shuffle,omitempty"`
	Items      []Item `json:"items"`
	// Fleet is true for a playlist under _fleet. The local admin cannot edit it.
	Fleet bool `json:"fleet"`
	// Kiosk is true for a playlist of exactly one URL item (D42).
	Kiosk bool `json:"kiosk"`

	// dir is the absolute path of the directory.
	dir string
}

// Problem is one fault that the scan found. The admin UI shows the list, so
// every message is a sentence for a person.
type Problem struct {
	// Playlist is the directory name, or "" for a fault of the media root.
	Playlist string `json:"playlist"`
	Message  string `json:"message"`
}

// Snapshot is the state of the media root after one scan. It is a value: a
// rescan makes a new one and never changes this one.
type Snapshot struct {
	Playlists []Playlist `json:"playlists"`
	Problems  []Problem  `json:"problems"`
	ScannedAt time.Time  `json:"scanned_at"`
	// Hashing is true while the background goroutine still has files to hash.
	Hashing bool `json:"hashing"`
}

// Find gives the playlist with this name.
func (s Snapshot) Find(name string) (Playlist, bool) {
	for _, p := range s.Playlists {
		if p.Name == name {
			return p, true
		}
	}
	return Playlist{}, false
}

// Options are the parameters of a Library.
type Options struct {
	// MediaRoot holds portapixel.toml, the playlist directories and _fleet.
	MediaRoot string
	// StateDir holds hashcache.json on ext4.
	StateDir string
	Log      *opslog.Log
	// Paired reports if the device has a fleet token. The fleet playlists play
	// only while the device is paired (plan section 13). A nil function means
	// "not paired".
	Paired func() bool
	// OnChange runs after each scan. The daemon tells the player to get the
	// manifest again: an upload, an edit of the active playlist, or a sideload
	// and a rescan must reach the screen without a person restarting anything.
	OnChange func()
}

// Library scans the media root and holds the last snapshot. It is safe for use
// by more than one goroutine.
type Library struct {
	opt Options

	mu   sync.RWMutex
	snap Snapshot

	cache *hashCache
	// wake tells the background goroutine that there is work. It holds one
	// slot, so a burst of rescans makes one pass.
	wake chan struct{}
}

// New makes a Library and reads the hash cache. It does not scan: the caller
// calls Rescan, so that the ops log line for a bad file comes after the boot
// line.
func New(opt Options) *Library {
	if opt.Paired == nil {
		opt.Paired = func() bool { return false }
	}
	return &Library{
		opt:   opt,
		cache: loadCache(opt.StateDir),
		wake:  make(chan struct{}, 1),
	}
}

// Snapshot gives the last scan. The SHA-256 values come from the cache, so a
// hash that the background goroutine finished after the scan is in the answer.
func (l *Library) Snapshot() Snapshot {
	l.mu.RLock()
	snap := l.snap
	l.mu.RUnlock()

	out := Snapshot{
		Problems:  snap.Problems,
		ScannedAt: snap.ScannedAt,
		Playlists: make([]Playlist, len(snap.Playlists)),
	}
	pending := 0
	for i, p := range snap.Playlists {
		p.Items = append([]Item(nil), p.Items...)
		for j := range p.Items {
			if p.Items[j].path == "" || p.Items[j].Missing {
				continue
			}
			rel := l.rel(p.Items[j].path)
			if sha, ok := l.cache.get(rel, p.Items[j].Size, p.Items[j].modNS); ok {
				p.Items[j].SHA256 = sha
			} else {
				pending++
			}
		}
		out.Playlists[i] = p
	}
	out.Hashing = pending > 0
	return out
}

// Rescan reads the media root again. It is synchronous, so the caller knows that
// the snapshot is current when it returns. The background hash goroutine picks up
// the new files after it.
func (l *Library) Rescan() Snapshot {
	snap := l.scan()

	l.mu.Lock()
	l.snap = snap
	l.mu.Unlock()

	// One slot: a second rescan while the goroutine works does not queue twice.
	select {
	case l.wake <- struct{}{}:
	default:
	}
	if l.opt.OnChange != nil {
		l.opt.OnChange()
	}
	return l.Snapshot()
}

// HashInBackground hashes the files that the cache does not know yet. It runs
// until ctx ends. It sleeps between files, because a hash must never take the
// processor away from the browser: playback beats bookkeeping.
func (l *Library) HashInBackground(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-l.wake:
			l.hashPending(done)
		}
	}
}

// scan reads the media root. It never returns an error: every fault is a
// Problem, because one bad directory must not hide the good ones.
func (l *Library) scan() Snapshot {
	snap := Snapshot{ScannedAt: time.Now()}

	entries, err := os.ReadDir(l.opt.MediaRoot)
	if err != nil {
		snap.Problems = append(snap.Problems, Problem{
			Message: "Cannot read the media directory: " + err.Error(),
		})
		l.log("library.scan.fail", err.Error())
		return snap
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if strings.HasPrefix(name, "_") {
			continue // _fleet and every other reserved name
		}
		l.readPlaylist(&snap, name, filepath.Join(l.opt.MediaRoot, name), false)
	}

	// The fleet playlists play only while the device is paired. An unpaired
	// device leaves the directory alone: the objects stay for the next pairing.
	if l.opt.Paired() {
		fleetRoot := filepath.Join(l.opt.MediaRoot, FleetDir)
		fleetEntries, err := os.ReadDir(fleetRoot)
		if err == nil {
			fleetNames := make([]string, 0, len(fleetEntries))
			for _, e := range fleetEntries {
				if e.IsDir() && e.Name() != FleetMediaDir {
					fleetNames = append(fleetNames, e.Name())
				}
			}
			sort.Strings(fleetNames)
			for _, name := range fleetNames {
				l.readPlaylist(&snap, name, filepath.Join(fleetRoot, name), true)
			}
		}
	}
	return snap
}

// readPlaylist reads one directory. A directory with no playlist.toml is not a
// playlist and is not a fault: it is scratch space (D15).
func (l *Library) readPlaylist(snap *Snapshot, name, dir string, fleet bool) {
	file := filepath.Join(dir, playlist.FileName)
	data, err := os.ReadFile(file)
	if err != nil {
		if !os.IsNotExist(err) {
			snap.Problems = append(snap.Problems, Problem{
				Playlist: name,
				Message:  "Cannot read " + playlist.FileName + ": " + err.Error(),
			})
			l.log("library.playlist.unreadable", name+": "+err.Error())
		}
		return
	}

	p, err := playlist.Parse(data, playlist.Options{AllowFleetRefs: fleet})
	if err != nil && !onlyEmptyItems(err) {
		snap.Problems = append(snap.Problems, Problem{
			Playlist: name,
			Message:  "This playlist is skipped: " + err.Error(),
		})
		l.log("library.playlist.skipped", name+": "+err.Error())
		return
	}

	out := Playlist{
		Name:       name,
		Title:      p.Meta.Name,
		Transition: p.Meta.Transition,
		Shuffle:    p.Meta.Shuffle,
		Fleet:      fleet,
		Kiosk:      p.IsKiosk(),
		dir:        dir,
	}
	if out.Title == "" {
		out.Title = name
	}
	for i, it := range p.Items {
		out.Items = append(out.Items, l.readItem(i, it, dir))
	}
	snap.Playlists = append(snap.Playlists, out)
}

// readItem turns one playlist entry into an Item and looks at the file.
func (l *Library) readItem(index int, it playlist.Item, dir string) Item {
	out := Item{
		Index:          index,
		Kind:           playlist.Kind(it),
		File:           it.File,
		URL:            it.URL,
		Duration:       it.Duration,
		Mute:           it.Mute,
		MaxDuration:    it.MaxDuration,
		RefreshSeconds: it.RefreshSeconds,
	}
	if out.Kind == playlist.KindURL {
		out.Name = it.URL
		return out
	}

	out.Name = path.Base(it.File)
	out.path = filepath.Join(dir, filepath.FromSlash(it.File))
	out.Src = l.srcURL(out.path)

	info, err := os.Stat(out.path)
	switch {
	case err != nil:
		out.Missing = true
		out.Warning = "This file is not on the device."
	case info.IsDir():
		out.Missing = true
		out.Warning = "This name is a directory, not a file."
	default:
		out.Size = info.Size()
		out.modNS = info.ModTime().UnixNano()
	}
	if !out.Missing && out.Kind == playlist.KindUnknown {
		out.Warning = "The player does not know this kind of file. It is skipped."
	}
	return out
}

// srcURL makes the URL that serves a file under the media root.
func (l *Library) srcURL(abs string) string {
	rel := l.rel(abs)
	if rel == "" {
		return ""
	}
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = urlEscape(p)
	}
	return MediaURLPrefix + strings.Join(parts, "/")
}

// rel gives the path of abs under the media root, with forward slashes. It
// gives "" when the path is outside the media root, which the playlist package
// already stops.
func (l *Library) rel(abs string) string {
	rel, err := filepath.Rel(l.opt.MediaRoot, abs)
	if err != nil {
		return ""
	}
	slashed := filepath.ToSlash(rel)
	if strings.HasPrefix(slashed, "../") || slashed == ".." {
		return ""
	}
	return slashed
}

func (l *Library) log(event, details string) {
	if l.opt.Log != nil {
		l.opt.Log.Log(event, details)
	}
}

// onlyEmptyItems reports if the only fault of a playlist is that it holds no
// items. The admin UI makes an empty playlist before the user adds the first
// item, so an empty playlist is a normal state and not a fault.
func onlyEmptyItems(err error) bool {
	var errs playlist.Errors
	if !errors.As(err, &errs) || len(errs) != 1 {
		return false
	}
	return errs[0].Field == "item"
}

// urlEscape escapes the characters that would change the meaning of a path
// element in a URL. url.PathEscape escapes too much: it turns a space into
// "%20" but leaves "&" and "?" ready to break the query string, and it escapes
// the plus sign in a way that some clients read back as a space.
func urlEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == '(', c == ')', c == '@':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}
