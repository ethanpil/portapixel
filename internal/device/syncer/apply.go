package syncer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/playlist"
	"github.com/ethanpil/portapixel/internal/slug"
	"github.com/ethanpil/portapixel/internal/store"
)

// The names under the media root. The library reads the same names, so each of
// them is a constant of one package and a copy of no other (ARCHITECTURE 3).
const (
	// FleetDir holds the fleet playlists and the object store.
	FleetDir = "_fleet"
	// MediaDir holds the objects: _fleet/media/<sha8>-<safe name>.
	MediaDir = "media"
	// stagingPrefix and trashPrefix name the two directories of a swap. A name
	// that starts with a full stop is not a playlist and not content.
	stagingPrefix = ".staging-"
	trashPrefix   = ".trash-"
	// partSuffix is the extension of a download that did not finish.
	partSuffix = ".part"
)

// SpaceReserve is the free space that a sync must leave on the media partition.
// Every write of the device goes there: portapixel.toml, each playlist.toml, the
// ops log mirror and the objects. A partition that one sync filled is a device that
// can save nothing at all (D41).
const SpaceReserve = 64 << 20

// FleetRef is the one shape of a file reference in a fleet playlist. exFAT has no
// hard links, so the playlist names the object by a path.
const FleetRef = "../" + MediaDir + "/"

// object is one entry of the manifest media list, after the checks.
type object struct {
	sha  string
	name string
	size int64
	url  string
	// copyFrom is a file on the card that already holds these bytes. A copy costs
	// no download (plan section 12).
	copyFrom string
}

// applyManifest puts the manifest on the card, whole or not at all.
//
// The steps, in this order and for these reasons:
//
//  1. Plan. An object with a bad hash, or an address that is not on the server, is
//     left out with an ops log line. A manifest is not trusted input.
//  2. Compare. A manifest that is the same as the last one ends here. The steady
//     state of a paired device writes nothing to the flash medium.
//  3. Space (D41). When the objects cannot fit, the objects that no item names go
//     first, oldest first, and only as many as the round needs. When they still
//     cannot fit, the sync fails before it downloads one byte: the device keeps
//     the playlists that it has and reports "needs X GB, has Y GB".
//  4. Objects. One at a time, with resume and a hash check. A failure ends the
//     round and keeps the part file and the old playlists.
//  5. Playlists. They go into a staging directory and a rename puts them in place,
//     so a reader never sees half a set.
//  6. The schedule, one rescan and one player event.
func (s *Syncer) applyManifest(ctx context.Context, base, token string, m manifest.Manifest) error {
	fleetRoot := filepath.Join(s.opt.MediaRoot, FleetDir)
	mediaDir := filepath.Join(fleetRoot, MediaDir)

	objects := s.planObjects(base, m)

	// 2. Nothing changed. No write, no rescan, no player event.
	want := canonical(&m)
	s.mu.Lock()
	applied := s.applied
	s.mu.Unlock()
	if applied && bytes.Equal(want, canonical(s.opt.State().Fleet)) {
		return nil
	}

	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		return fmt.Errorf("make %s: %w", mediaDir, err)
	}

	// 3. What is missing, and does it fit?
	need := s.missing(mediaDir, objects)
	if err := s.makeRoom(mediaDir, objects, need); err != nil {
		return err
	}

	// 4. The objects.
	for _, o := range need {
		if err := s.fetch(ctx, mediaDir, token, o); err != nil {
			return err
		}
	}

	// 5. The playlists.
	names, err := s.writePlaylists(fleetRoot, objects, m)
	if err != nil {
		return err
	}

	// 6. The state, then the schedule and one message to the player.
	kept := m
	kept.Commands = nil
	if err := s.opt.SaveState(func(st *identity.State) { st.Fleet = &kept }); err != nil {
		return err
	}
	s.mu.Lock()
	s.applied = true
	s.mu.Unlock()

	if s.opt.SetFleetRules != nil {
		s.opt.SetFleetRules(m.DefaultPlaylist, m.Schedule, m.Screen)
	}
	if s.opt.Rescan != nil {
		s.opt.Rescan()
	}
	s.log("sync.apply", fmt.Sprintf("%d playlists, %d objects, %d downloaded",
		len(names), len(objects), len(need)))
	return nil
}

// planObjects turns the media list into the objects that this device must hold.
//
// An entry that names a hash which is not a hash, or an address that is not on the
// server, is left out. store.ObjectName refuses a value such as "../../x", so a
// manifest can never write a file outside the object store.
func (s *Syncer) planObjects(base string, m manifest.Manifest) map[string]object {
	out := make(map[string]object, len(m.Media))
	for _, ref := range m.Media {
		name, err := store.ObjectName(ref.SHA256, ref.Name)
		if err != nil {
			s.log("sync.object.skipped", err.Error())
			continue
		}
		address, err := objectURL(base, ref.URL)
		if err != nil {
			s.log("sync.object.skipped", ref.SHA256[:min(8, len(ref.SHA256))]+": "+err.Error())
			continue
		}
		if ref.Size < 0 {
			s.log("sync.object.skipped", name+": the manifest gives a size below zero")
			continue
		}
		out[ref.SHA256] = object{sha: ref.SHA256, name: name, size: ref.Size, url: address}
	}
	return out
}

// missing gives the objects that the store does not hold yet, in name order.
//
// An object counts as present only when the file is there, the size matches and the
// SHA-256 matches. The hash comes from the cache of the library when the cache
// knows the file, so the steady state reads no file content at all.
func (s *Syncer) missing(mediaDir string, objects map[string]object) []object {
	var out []object
	for _, o := range objects {
		dest := filepath.Join(mediaDir, o.name)
		if s.present(dest, o) {
			continue
		}
		// A file that is already on the card under another name is never fetched
		// again. It can be in a local playlist or under an older object name.
		if from, ok := s.opt.FindSHA(o.sha); ok && from != dest {
			o.copyFrom = from
		}
		out = append(out, o)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].name < out[b].name })
	return out
}

// present reports if the store already holds this object.
func (s *Syncer) present(dest string, o object) bool {
	info, err := os.Stat(dest)
	if err != nil || info.IsDir() {
		return false
	}
	if o.size > 0 && info.Size() != o.size {
		return false
	}
	rel := s.rel(dest)
	if sha, ok := s.opt.CachedSHA(rel, info.Size(), info.ModTime().UnixNano()); ok {
		return strings.EqualFold(sha, o.sha)
	}
	// The cache does not know this file. Read it one time and record the answer, so
	// that the next poll costs one stat call.
	sha, err := store.HashFile(dest)
	if err != nil {
		return false
	}
	if !strings.EqualFold(sha, o.sha) {
		return false
	}
	s.opt.NoteSHA(dest, sha)
	return true
}

// makeRoom is the space rule of D41.
//
// It gives an error before anything is downloaded when the objects cannot fit. The
// sentence names what the device needs and what it has, and the next heartbeat and
// the local UI both show it. A half sync and a thrash of the eviction are both
// worse than a screen that says why.
func (s *Syncer) makeRoom(mediaDir string, objects map[string]object, need []object) error {
	var want int64
	for _, o := range need {
		want += o.size - partBytes(filepath.Join(mediaDir, o.name))
	}
	if want <= 0 {
		return nil
	}

	room, err := s.room()
	if err != nil {
		// The free space is not known, which happens on a development machine. The
		// hash check of each download is the guard that is left.
		return nil
	}
	if want <= room {
		return nil
	}
	room += s.evict(mediaDir, objects, want-room)
	if want <= room {
		return nil
	}
	return fmt.Errorf("needs %s, has %s", gigabytes(want), gigabytes(room))
}

// room gives the number of bytes that a sync may take.
func (s *Syncer) room() (int64, error) {
	free, err := s.opt.FreeBytes(s.opt.MediaRoot)
	if err != nil {
		return 0, err
	}
	room := int64(free) - SpaceReserve
	if room < 0 {
		room = 0
	}
	return room, nil
}

// evictable is one file of the object store that no item of this manifest names.
type evictable struct {
	path  string
	size  int64
	order int64 // modification time in nanoseconds; the oldest goes first
}

// evict removes objects that no current item names, oldest first, and only as many
// as the round needs. It gives the number of bytes that it freed.
//
// The part file of a needed object stays: it is the resume of a download that a
// dropped connection stopped (D24).
func (s *Syncer) evict(mediaDir string, objects map[string]object, want int64) int64 {
	keep := make(map[string]bool, len(objects)*2)
	for _, o := range objects {
		keep[o.name] = true
		keep[o.name+partSuffix] = true
	}

	entries, err := os.ReadDir(mediaDir)
	if err != nil {
		return 0
	}
	var list []evictable
	for _, e := range entries {
		if e.IsDir() || keep[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, evictable{
			path:  filepath.Join(mediaDir, e.Name()),
			size:  info.Size(),
			order: info.ModTime().UnixNano(),
		})
	}
	sort.Slice(list, func(a, b int) bool { return list[a].order < list[b].order })

	var freed int64
	for _, item := range list {
		if freed >= want {
			break
		}
		if err := os.Remove(item.path); err != nil {
			continue
		}
		freed += item.size
		s.log("sync.evict", filepath.Base(item.path)+" is no longer in the manifest")
	}
	if freed > 0 {
		fsutil.SyncDir(mediaDir)
	}
	return freed
}

// fetch puts one object in the store: a copy of bytes that the card already holds,
// or a download with resume and a hash check.
func (s *Syncer) fetch(ctx context.Context, mediaDir, token string, o object) error {
	dest := filepath.Join(mediaDir, o.name)
	if o.copyFrom != "" {
		if err := s.take(o.copyFrom, dest, mediaDir); err != nil {
			return fmt.Errorf("copy %s: %w", o.name, err)
		}
		s.opt.NoteSHA(dest, o.sha)
		s.log("sync.object.reused", o.name+" came from a file that is already on the card")
		return nil
	}
	if err := store.Download(ctx, s.opt.Client, o.url, token, dest, o.sha, o.size); err != nil {
		return fmt.Errorf("get %s: %w", o.name, err)
	}
	s.opt.NoteSHA(dest, o.sha)
	s.log("sync.object", fmt.Sprintf("%s %d bytes", o.name, o.size))
	return nil
}

// take gets the bytes of a file that the card already holds.
//
// A file inside the object store is moved: it is the same object under an older
// name, and a copy would need the space twice. A file of a local playlist is
// copied, because the playlist of the person must keep it.
func (s *Syncer) take(from, dest, mediaDir string) error {
	if filepath.Dir(from) == mediaDir {
		if err := s.opt.Rename(from, dest); err != nil {
			return err
		}
		fsutil.SyncDir(mediaDir)
		return nil
	}
	return fsutil.CopyFileSync(from, dest)
}

// writePlaylists renders the fleet playlists into a staging directory and swaps
// them into place. It gives the names that the manifest holds.
func (s *Syncer) writePlaylists(fleetRoot string, objects map[string]object, m manifest.Manifest) ([]string, error) {
	staging := filepath.Join(fleetRoot, stagingPrefix+randomSuffix())
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return nil, fmt.Errorf("make %s: %w", staging, err)
	}
	defer os.RemoveAll(staging)

	var names []string
	for _, p := range m.Playlists {
		name := p.Name
		if name == "" || name != slug.Make(name) {
			s.log("sync.playlist.skipped", fmt.Sprintf("%q is not a directory name that this device accepts", p.Name))
			continue
		}
		rendered, ok := s.renderPlaylist(objects, p)
		if !ok {
			continue
		}
		dir := filepath.Join(staging, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("make %s: %w", dir, err)
		}
		if err := fsutil.WriteFileAtomic(filepath.Join(dir, playlist.FileName), rendered, 0o644); err != nil {
			return nil, err
		}
		names = append(names, name)
	}

	stale, err := s.stalePlaylists(fleetRoot, names)
	if err != nil {
		return nil, err
	}
	if err := s.swap(fleetRoot, staging, names, stale); err != nil {
		return nil, err
	}
	return names, nil
}

// renderPlaylist turns one manifest playlist into the bytes of a playlist.toml.
//
// An item whose object is not in the plan is left out: the hash was bad, or the
// address was not on the server. A playlist with no item left is left out too,
// because playlist.Validate refuses it and the library would report it as a fault
// at every scan.
func (s *Syncer) renderPlaylist(objects map[string]object, p manifest.Playlist) ([]byte, bool) {
	out := playlist.Playlist{Meta: playlist.Meta{
		Name:       p.Title,
		Transition: p.Transition,
		Shuffle:    p.Shuffle,
	}}
	if out.Meta.Name == "" {
		out.Meta.Name = p.Name
	}
	for _, it := range p.Items {
		switch {
		case it.URL != "":
			out.Items = append(out.Items, playlist.Item{
				URL:            it.URL,
				Duration:       it.Duration,
				RefreshSeconds: it.RefreshSeconds,
			})
		case it.SHA256 != "":
			o, ok := objects[it.SHA256]
			if !ok {
				s.log("sync.item.skipped", p.Name+": this device has no object for one item")
				continue
			}
			out.Items = append(out.Items, playlist.Item{
				File:        FleetRef + o.name,
				Duration:    it.Duration,
				Mute:        it.Mute,
				MaxDuration: it.MaxDuration,
			})
		}
	}
	if errs := out.Validate(playlist.Options{AllowFleetRefs: true}); len(errs) > 0 {
		s.log("sync.playlist.skipped", p.Name+": "+errs.Error())
		return nil, false
	}
	return playlist.Render(out), true
}

// stalePlaylists gives the fleet playlist directories that the manifest no longer
// holds.
func (s *Syncer) stalePlaylists(fleetRoot string, names []string) ([]string, error) {
	entries, err := os.ReadDir(fleetRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", fleetRoot, err)
	}
	live := make(map[string]bool, len(names))
	for _, n := range names {
		live[n] = true
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || name == MediaDir || strings.HasPrefix(name, ".") || live[name] {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// swap puts the staged playlists in place with renames.
//
// Every old directory goes to a trash directory first, then every new directory
// takes its place. A rename that fails puts everything back: a reader must see the
// old set or the new set, never a mixture of the two. The trash directory is
// removed at the end, so a power cut in the middle leaves a directory that starts
// with a full stop, which is never a playlist.
func (s *Syncer) swap(fleetRoot, staging string, names, stale []string) error {
	trash := filepath.Join(fleetRoot, trashPrefix+randomSuffix())
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return fmt.Errorf("make %s: %w", trash, err)
	}
	defer os.RemoveAll(trash)

	// moved holds the steps that worked, newest first, so a rollback walks it in
	// order.
	type step struct{ from, to string }
	var moved []step
	undo := func() {
		for i := len(moved) - 1; i >= 0; i-- {
			s.opt.Rename(moved[i].to, moved[i].from)
		}
	}

	move := func(from, to string) error {
		if err := s.opt.Rename(from, to); err != nil {
			undo()
			return fmt.Errorf("move %s to %s: %w", filepath.Base(from), filepath.Base(to), err)
		}
		moved = append(moved, step{from: from, to: to})
		return nil
	}

	// The old set goes aside. stale goes with it, which is how a playlist that the
	// manifest dropped goes away.
	for _, name := range append(append([]string{}, names...), stale...) {
		target := filepath.Join(fleetRoot, name)
		if _, err := os.Stat(target); err != nil {
			continue
		}
		if err := move(target, filepath.Join(trash, name)); err != nil {
			return err
		}
	}
	// The new set comes in.
	for _, name := range names {
		if err := move(filepath.Join(staging, name), filepath.Join(fleetRoot, name)); err != nil {
			return err
		}
	}
	fsutil.SyncDir(fleetRoot)
	return nil
}

// rel gives the path of a file under the media root, with forward slashes. The
// hash cache of the library is keyed that way.
func (s *Syncer) rel(abs string) string {
	rel, err := filepath.Rel(s.opt.MediaRoot, abs)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

// canonical gives the bytes that say if two manifests ask for the same thing.
//
// The commands are not in it. A command is one piece of work and not a state, so a
// manifest that holds only a new command must not make the device write its
// playlists again.
func canonical(m *manifest.Manifest) []byte {
	if m == nil {
		return nil
	}
	copyOf := *m
	copyOf.Commands = nil
	data, err := json.Marshal(copyOf)
	if err != nil {
		return nil
	}
	return data
}

// partBytes gives the size of the part file of a download that did not finish, or
// zero. Those bytes are already on the card, so the space rule must not count them
// twice.
func partBytes(dest string) int64 {
	info, err := os.Stat(dest + partSuffix)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

// gigabytes writes a number of bytes as the sentence of D41 wants it.
func gigabytes(n int64) string {
	if n < 0 {
		n = 0
	}
	return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
}

// randomSuffix names a staging or trash directory. Two syncs never run at one
// time, but a directory that a power cut left must not be the directory that the
// next sync writes into.
func randomSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any system that we run on.
		panic("syncer: the system gave no random bytes: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
