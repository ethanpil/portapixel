package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/playlist"
	"github.com/ethanpil/portapixel/internal/rnd"
	"github.com/ethanpil/portapixel/internal/slug"
	"github.com/ethanpil/portapixel/internal/store"
)

// The names of the two directories of a swap. A name that starts with a full stop
// is not a playlist and not content, so the library and the scheduler pass over it.
//
// The names of the fleet tree itself are library.FleetDir and library.FleetMediaDir:
// the library reads that tree, so it owns the names.
const (
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
//  1. Sweep. A staging or a trash directory that a power cut left is invisible to
//     the library and to the free space arithmetic, and a trash directory holds a
//     whole set of playlists.
//  2. Plan. An object with a bad hash, a size that is not a size, or an address that
//     is not on the server, is left out. A manifest is not trusted input.
//  3. Compare, in two halves. The content (the playlists and the objects) decides
//     if anything is written to the card. The rules (the default playlist, the
//     schedule and the screen times) only go to the scheduler. A schedule edit must
//     not make every device write every playlist again.
//  4. Space (D41). When the objects cannot fit, the objects that no item names go
//     first, oldest first, and only as many as the round needs. When they still
//     cannot fit, the sync fails before it downloads one byte: the device keeps the
//     playlists that it has and reports "needs X GB, has Y GB".
//  5. Objects. One at a time, with resume and a hash check. A failure ends the
//     round and keeps the part file and the old playlists.
//  6. Playlists. Only the ones that differ from the card are rendered into a
//     staging directory, and a rename puts the whole set in place, so a reader never
//     sees half a set.
//  7. The state, the schedule, one rescan and one player event.
func (s *Syncer) applyManifest(ctx context.Context, base, token string, m manifest.Manifest) error {
	gen := s.generation()
	fleetRoot := filepath.Join(s.opt.MediaRoot, library.FleetDir)
	mediaDir := filepath.Join(fleetRoot, library.FleetMediaDir)

	s.sweepLeftovers()

	s.mu.Lock()
	applied := s.applied
	sameContent := applied && s.contentKey == contentKey(&m)
	sameRules := applied && s.rulesKey == rulesKey(&m)
	s.mu.Unlock()

	// The plan is silent when the content did not change. Its only use then is the
	// presence check below, and one ops log line for each skipped object at every
	// poll would be a write to the card every minute.
	objects := s.planObjects(base, m, !sameContent)

	// The content of the card is checked on every poll, and not only when the
	// manifest changed. A file that somebody deleted from the card with a laptop
	// would otherwise never come back, not even after a reboot.
	need := s.missing(mediaDir, objects)
	playlistsOK := s.playlistsPresent(fleetRoot, objects, m)

	if sameContent && len(need) == 0 && playlistsOK {
		if sameRules {
			return nil // the steady state of a paired device writes nothing at all
		}
		// Only the rules changed. No render, no swap, no rescan: the scheduler takes
		// the new rules and the state file records them.
		return s.applyRules(gen, m, true)
	}

	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		return fmt.Errorf("make %s: %w", mediaDir, err)
	}

	if err := s.makeRoom(mediaDir, objects, need); err != nil {
		// The manifest does not fit (D41). The device keeps the playlists that it
		// has. The rules of the new manifest still go to the scheduler when every
		// playlist that they name is already on the card: a screen that must show
		// another playlist at 09:00 must not wait for a card that has room.
		if s.rulesFit(fleetRoot, m) {
			s.applyRules(gen, m, false)
		}
		return err
	}

	for _, o := range need {
		if err := s.fetch(ctx, mediaDir, token, o); err != nil {
			return err
		}
	}

	names, changed, err := s.writePlaylists(fleetRoot, objects, m)
	if err != nil {
		return err
	}

	if err := s.applyRules(gen, m, false); err != nil {
		return err
	}
	if changed && s.opt.Rescan != nil {
		s.opt.Rescan()
	}
	s.log("sync.apply", fmt.Sprintf("%d playlists, %d objects, %d downloaded",
		len(names), len(objects), len(need)))
	return nil
}

// applyRules records the manifest and hands its rules to the scheduler.
//
// rulesOnly is true for the pass that changed no file on the card. The state file is
// still written, because the state is what the next start restores.
func (s *Syncer) applyRules(gen uint64, m manifest.Manifest, rulesOnly bool) error {
	// An Unpair that landed while the round ran. Writing now would give the device
	// the fleet state of a server that it left, and the scheduler would then name a
	// playlist that nothing serves.
	if s.stale(gen) {
		return nil
	}

	kept := m
	kept.Commands = nil
	if err := s.opt.SaveState(func(st *identity.State) {
		if st.Paired() {
			st.Fleet = &kept
		}
	}); err != nil {
		return err
	}
	if s.stale(gen) {
		return nil
	}

	s.mu.Lock()
	s.applied = true
	s.contentKey = contentKey(&m)
	s.rulesKey = rulesKey(&m)
	s.mu.Unlock()

	if s.opt.SetFleetRules != nil {
		s.opt.SetFleetRules(m.DefaultPlaylist, m.Schedule, m.Screen)
	}
	if rulesOnly {
		s.log("sync.rules", "the schedule and the screen times of the server changed; no file on the card changed")
	}
	return nil
}

// planObjects turns the media list into the objects that this device must hold.
//
// An entry that names a hash which is not a hash, a size of zero or below, or an
// address that is not on the server, is left out. store.ObjectName refuses a value
// such as "../../x", so a manifest can never write a file outside the object store.
//
// noise is false for a poll that changed nothing. The skips are then not written to
// the ops log, which is a file on the same flash card.
func (s *Syncer) planObjects(base string, m manifest.Manifest, noise bool) map[string]object {
	skip := func(text string) {
		if noise {
			s.log("sync.object.skipped", text)
		}
	}
	out := make(map[string]object, len(m.Media))
	for _, ref := range m.Media {
		name, err := store.ObjectName(ref.SHA256, ref.Name)
		if err != nil {
			skip(err.Error())
			continue
		}
		address, err := objectURL(base, ref.URL)
		if err != nil {
			skip(name + ": " + err.Error())
			continue
		}
		// A size of zero is not a size. It would take the space check of D41 out of
		// the round for that object, and the download would run with no limit at
		// all: one manifest could then fill the card, and a device with a full card
		// can write no configuration and no state.
		if ref.Size <= 0 {
			skip(name + ": the manifest gives no size for this object")
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
	if sha, ok := s.opt.CachedSHA(dest, info.Size(), info.ModTime().UnixNano()); ok {
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

// playlistsPresent reports if every fleet playlist of the manifest has its
// playlist.toml on the card, and that no directory of an older manifest is left.
//
// It is a stat for each playlist on every poll. Without it a directory that
// somebody removed with a laptop never came back, because the manifest had not
// changed and the compare ended the round.
func (s *Syncer) playlistsPresent(fleetRoot string, objects map[string]object, m manifest.Manifest) bool {
	want := make(map[string]bool, len(m.Playlists))
	for _, p := range m.Playlists {
		if !usableFleetName(p.Name) {
			continue // the render leaves it out as well
		}
		// The render is the only answer to "does this playlist become a directory".
		// A check that asked a different question could never be true. A playlist
		// whose items all fell away renders to nothing and gets no directory, so
		// this function said "not present" at every poll for ever. The round then
		// never took the short circuit. It wrote one sync.apply line to the flash
		// every minute (D2). buildPlaylist writes no log line, so this stays a read.
		if _, _, ok := s.buildPlaylist(objects, p); !ok {
			continue
		}
		want[p.Name] = true
		if _, err := os.Stat(filepath.Join(fleetRoot, p.Name, playlist.FileName)); err != nil {
			return false
		}
	}
	stale, err := s.stalePlaylists(fleetRoot, keys(want))
	return err == nil && len(stale) == 0
}

// rulesFit reports if every playlist that the rules of this manifest name is on the
// card already. A rule that names a playlist which is not there leaves the screen on
// the fallback picture, so the rules wait for the content in that case.
func (s *Syncer) rulesFit(fleetRoot string, m manifest.Manifest) bool {
	names := []string{m.DefaultPlaylist}
	for _, r := range m.Schedule {
		names = append(names, r.Playlist)
	}
	for _, name := range names {
		// The same rule as the render. A name that never becomes a directory must not
		// hold the rules back for ever.
		if !usableFleetName(name) {
			continue
		}
		if _, err := os.Stat(filepath.Join(fleetRoot, name, playlist.FileName)); err != nil {
			return false
		}
	}
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
		if o.copyFrom != "" {
			// A copy of bytes that the card already holds. It is a copy and not a
			// rename, even inside the store: the playlists that play now name the old
			// file, and a rename before the swap would leave them pointing at a name
			// that is gone. So the space is really needed, and the old name goes at
			// the next eviction.
			want += o.size
			continue
		}
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
	room += s.evict(mediaDir, objects, need, want-room)
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
// dropped connection stopped (D24). A file that this round will copy from stays as
// well: it holds the bytes of an object of this manifest under an older name. Remove
// it, and the copy fails and the object is on the card under no name at all.
func (s *Syncer) evict(mediaDir string, objects map[string]object, need []object, want int64) int64 {
	keep := make(map[string]bool, len(objects)*2+len(need))
	for _, o := range objects {
		keep[o.name] = true
		keep[o.name+partSuffix] = true
	}
	for _, o := range need {
		if o.copyFrom != "" && filepath.Dir(o.copyFrom) == mediaDir {
			keep[filepath.Base(o.copyFrom)] = true
		}
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
		if err := fsutil.CopyFileSync(o.copyFrom, dest); err != nil {
			return fmt.Errorf("copy %s: %w", o.name, err)
		}
		fsutil.SyncDir(mediaDir)
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

// writePlaylists renders the fleet playlists and swaps them into place. It gives the
// names that the manifest holds and whether anything on the card changed.
//
// A playlist whose rendered bytes are the bytes that the card already holds is not
// written. When no file differs and no directory is stale, the whole swap is skipped:
// the never-a-mixture rule is kept by doing nothing at all, and a card that changes
// nothing costs no flash write and no rescan.
func (s *Syncer) writePlaylists(fleetRoot string, objects map[string]object, m manifest.Manifest) (names []string, changed bool, err error) {
	rendered := make(map[string][]byte, len(m.Playlists))
	for _, p := range m.Playlists {
		name := p.Name
		if !usableFleetName(name) {
			s.log("sync.playlist.skipped", fmt.Sprintf("%q is not a directory name that this device accepts", p.Name))
			continue
		}
		body, ok := s.renderPlaylist(objects, p)
		if !ok {
			continue
		}
		rendered[name] = body
		names = append(names, name)
	}
	sort.Strings(names)

	stale, err := s.stalePlaylists(fleetRoot, names)
	if err != nil {
		return nil, false, err
	}
	if len(stale) == 0 && !s.anyPlaylistDiffers(fleetRoot, rendered) {
		return names, false, nil
	}

	staging := filepath.Join(fleetRoot, stagingPrefix+rnd.Hex(6))
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return nil, false, fmt.Errorf("make %s: %w", staging, err)
	}
	defer os.RemoveAll(staging)

	for _, name := range names {
		dir := filepath.Join(staging, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, false, fmt.Errorf("make %s: %w", dir, err)
		}
		if err := fsutil.WriteFileAtomic(filepath.Join(dir, playlist.FileName), rendered[name], 0o644); err != nil {
			return nil, false, err
		}
	}
	if err := s.swap(fleetRoot, staging, names, stale); err != nil {
		return nil, false, err
	}
	return names, true, nil
}

// anyPlaylistDiffers reports if one of the rendered playlists is not the file that
// the card holds now.
func (s *Syncer) anyPlaylistDiffers(fleetRoot string, rendered map[string][]byte) bool {
	for name, body := range rendered {
		have, err := os.ReadFile(filepath.Join(fleetRoot, name, playlist.FileName))
		if err != nil || !bytes.Equal(have, body) {
			return true
		}
	}
	return false
}

// renderPlaylist turns one manifest playlist into the bytes of a playlist.toml.
//
// An item whose object is not in the plan is left out: the hash was bad, or the
// address was not on the server. A playlist with no item left is left out too,
// because playlist.Validate refuses it and the library would report it as a fault
// at every scan.
func (s *Syncer) renderPlaylist(objects map[string]object, p manifest.Playlist) ([]byte, bool) {
	body, notes, ok := s.buildPlaylist(objects, p)
	for _, n := range notes {
		s.log(n.event, n.text)
	}
	return body, ok
}

// note is one line that the caller may put in the ops log.
type note struct{ event, text string }

// buildPlaylist turns a manifest playlist into the bytes of a local playlist.toml.
// The second value holds the lines for the ops log and this function writes NONE of
// them itself.
//
// Why the split: playlistsPresent asks the same question on every poll, and it must
// stay a read. A version of this function that logged put one line on the flash
// every minute for as long as the manifest held a playlist that renders to nothing
// (D2).
func (s *Syncer) buildPlaylist(objects map[string]object, p manifest.Playlist) ([]byte, []note, bool) {
	var notes []note
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
				notes = append(notes, note{"sync.item.skipped", p.Name + ": this device has no object for one item"})
				continue
			}
			out.Items = append(out.Items, playlist.Item{
				File:        playlist.FleetRef(o.name),
				Duration:    it.Duration,
				Mute:        it.Mute,
				MaxDuration: it.MaxDuration,
			})
		}
	}
	if errs := out.Validate(playlist.Options{AllowFleetRefs: true}); len(errs) > 0 {
		notes = append(notes, note{"sync.playlist.skipped", p.Name + ": " + errs.Error()})
		return nil, notes, false
	}
	return playlist.Render(out), notes, true
}

// usableFleetName is the ONE rule that says which manifest playlist name may become
// a directory under _fleet.
//
// library.FleetMediaDir is the reserved name: _fleet/media holds the objects
// themselves. A fleet playlist with the title "Media" becomes the slug "media", and
// the swap then moved the whole object store into the trash directory and removed
// it. Every fleet item became a 404, and the next poll fetched every object again,
// over the link of the site and onto the card.
//
// The three passes that walk the manifest playlists - the render, the presence check
// and the rules check - must all use this one rule, or one pass expects a directory
// that another pass never makes.
func usableFleetName(name string) bool {
	return name != "" && name == slug.Make(name) && name != library.FleetMediaDir
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
		if !e.IsDir() || name == library.FleetMediaDir || strings.HasPrefix(name, ".") || live[name] {
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
// with a full stop, which is never a playlist, and sweepLeftovers takes it away at
// the next start.
func (s *Syncer) swap(fleetRoot, staging string, names, stale []string) error {
	trash := filepath.Join(fleetRoot, trashPrefix+rnd.Hex(6))
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

// sweepLeftovers removes the staging and the trash directories of a swap that a
// power cut stopped.
//
// A defer is not enough. Such a directory is invisible to the library, to the
// eviction and to the free space arithmetic. A trash directory holds a whole set of
// playlists, so a device could lose hundreds of megabytes for ever.
func (s *Syncer) sweepLeftovers() {
	fleetRoot := filepath.Join(s.opt.MediaRoot, library.FleetDir)
	entries, err := os.ReadDir(fleetRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || (!strings.HasPrefix(name, stagingPrefix) && !strings.HasPrefix(name, trashPrefix)) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(fleetRoot, name)); err != nil {
			s.log("sync.sweep.fail", name+": "+err.Error())
			continue
		}
		s.log("sync.sweep", name+" is left over from a sync that a power cut stopped")
	}
}

// contentKey says if two manifests ask for the same files on the card: the
// playlists and the object list.
//
// It is the half of the manifest that decides a write. The rules are not in it: a
// schedule edit used to make every device render and fsync every playlist.toml,
// swap the directories, rescan the card and reload the player.
func contentKey(m *manifest.Manifest) string {
	if m == nil {
		return ""
	}
	return marshalKey(struct {
		Playlists []manifest.Playlist `json:"p"`
		Media     []manifest.MediaRef `json:"m"`
	}{m.Playlists, m.Media})
}

// rulesKey says if two manifests ask for the same schedule. Nothing on the card
// changes when only this value changes.
func rulesKey(m *manifest.Manifest) string {
	if m == nil {
		return ""
	}
	return marshalKey(struct {
		Default  string               `json:"d"`
		Schedule []manifest.Rule      `json:"s"`
		Screen   *manifest.ScreenRule `json:"c"`
	}{m.DefaultPlaylist, m.Schedule, m.Screen})
}

// marshalKey gives one comparable value of a part of the manifest. A value that
// cannot be JSON gives "", which counts as "not the same" and makes the caller do
// the work.
func marshalKey(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

// keys gives the keys of a set, in order.
func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
