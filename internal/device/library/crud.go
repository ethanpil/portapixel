package library

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/playlist"
	"github.com/ethanpil/portapixel/internal/slug"
	"github.com/ethanpil/portapixel/internal/store"
)

// The faults that the API turns into a status code.
var (
	ErrNotFound = errors.New("there is no playlist with this name")
	ErrExists   = errors.New("a playlist with this name is on the device")
	ErrBadName  = errors.New("this name cannot be a directory name")
	ErrManaged  = errors.New("the fleet server manages this playlist")
	ErrNoFile   = errors.New("there is no file with this name")
	ErrNoSpace  = errors.New("there is not enough free space on the media partition")
)

// maxNameTries is how many numbered names an upload may try. A directory with a
// thousand files of one name is a fault of the caller, not a name to find.
const maxNameTries = 1000

// SpaceReserve is the free space that an upload must leave. Every write of the
// device goes to the media partition: portapixel.toml, each playlist.toml and the
// fleet objects. A partition that one upload filled is a device that can save
// nothing at all (D41).
const SpaceReserve = 64 << 20

// Slug makes a directory name from a title. It gives "" when nothing is left, and
// the caller then reports ErrBadName. The length is capped, because a name goes in
// a path, in a URL and in a schedule rule.
func Slug(title string) string {
	out := slug.Make(title)
	if len(out) > 48 {
		out = strings.Trim(out[:48], "-")
	}
	return out
}

// CreatePlaylist makes a directory and an empty playlist.toml. It gives the
// directory name, which is the name that the API and the schedule rules use.
func (l *Library) CreatePlaylist(title string) (string, error) {
	name := Slug(title)
	if name == "" {
		return "", ErrBadName
	}
	dir := filepath.Join(l.opt.MediaRoot, name)
	if _, err := os.Stat(dir); err == nil {
		return "", ErrExists
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("make %s: %w", dir, err)
	}
	p := playlist.Playlist{Meta: playlist.Meta{Name: strings.TrimSpace(title)}}
	if err := writePlaylistFile(dir, p); err != nil {
		return "", err
	}
	l.log("playlist.create", name)
	l.changed()
	return name, nil
}

// RenamePlaylist moves a playlist directory and sets the title in its
// playlist.toml. It gives the new directory name.
//
// A playlist.toml that does not parse is moved and nothing else. The bytes are
// what the user repairs. A rewrite from an empty placeholder deletes every item.
//
// The schedule rules and playback.default_playlist name a playlist by its
// directory name, so the daemon must correct them. Options.OnRename does that.
func (l *Library) RenamePlaylist(name, title string) (string, error) {
	dir, p, parsed, err := l.localPlaylist(name)
	if err != nil {
		return "", err
	}
	next := Slug(title)
	if next == "" {
		return "", ErrBadName
	}
	target := filepath.Join(l.opt.MediaRoot, next)
	if next != name {
		if _, err := os.Stat(target); err == nil {
			return "", ErrExists
		}
		if err := os.Rename(dir, target); err != nil {
			return "", fmt.Errorf("rename %s: %w", dir, err)
		}
	}
	if parsed {
		p.Meta.Name = strings.TrimSpace(title)
		if err := writePlaylistFile(target, p); err != nil {
			return "", err
		}
	} else {
		l.log("playlist.rename.keep", next+": playlist.toml does not parse, so only the directory moved")
	}
	l.log("playlist.rename", name+" -> "+next)
	if l.opt.OnRename != nil && next != name {
		l.opt.OnRename(name, next)
	}
	l.changed()
	return next, nil
}

// DeletePlaylist removes a playlist directory and everything in it.
func (l *Library) DeletePlaylist(name string) error {
	dir, _, _, err := l.localPlaylist(name)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	l.log("playlist.delete", name)
	l.changed()
	return nil
}

// SavePlaylist writes playlist.toml again from p. The stock comments come back
// and the comments of the user are lost, which the admin UI says before it saves
// (D15).
//
// A playlist that breaks a rule gives playlist.Errors, which the API sends as
// 422 with one message for each field.
func (l *Library) SavePlaylist(name string, p playlist.Playlist) error {
	dir, _, _, err := l.localPlaylist(name)
	if err != nil {
		return err
	}
	if errs := p.Validate(playlist.Options{}); len(errs) > 0 && !onlyEmptyItems(errs) {
		return errs
	}
	if err := writePlaylistFile(dir, p); err != nil {
		return err
	}
	l.log("playlist.save", fmt.Sprintf("%s items=%d", name, len(p.Items)))
	l.changed()
	return nil
}

// AddMedia writes an uploaded file into a playlist directory. It gives the name
// that the file has on the disk.
//
// The stream goes to a staging file and a rename commits it. A connection that
// drops then leaves no half file in the playlist (D41). The staging name is
// unique: two uploads of one file name arrive together when a person clicks twice
// or a request is retried. One shared staging file took both streams and gave one
// file of mixed bytes.
//
// declared is Content-Length, or a value below zero when the size is not known.
// The upload never takes the last SpaceReserve bytes of the partition.
func (l *Library) AddMedia(name, filename string, r io.Reader, declared int64) (string, int64, error) {
	dir, _, _, err := l.localPlaylist(name)
	if err != nil {
		return "", 0, err
	}
	safe := store.SafeName(filename)
	if safe == "" {
		return "", 0, ErrBadName
	}
	// store.SafeName removes every path separator, so the file cannot leave the
	// directory. This check is the second lock on that door.
	if filepath.Dir(filepath.Join(dir, safe)) != dir {
		return "", 0, ErrBadName
	}

	room := l.room()
	if declared >= 0 && declared > room {
		return "", 0, fmt.Errorf("%w: the file needs %d bytes and %d are free", ErrNoSpace, declared, room)
	}

	// The name is reserved and the staging file is made under one lock. Without
	// the reservation two uploads of one file name both saw the name free, took
	// it, and the second rename threw the first file away.
	l.upload.Lock()
	target, safe, err := freeName(dir, safe, l.reserved)
	var f *os.File
	if err == nil {
		l.reserved[target] = true
		f, err = os.CreateTemp(dir, ".upload-*")
		if err != nil {
			delete(l.reserved, target)
		}
	}
	l.upload.Unlock()
	if err != nil {
		return "", 0, err
	}
	defer l.release(target)
	part := f.Name()

	// The limit is one byte over the room, so that a stream which is exactly one
	// byte too long is caught and not written.
	limited := io.LimitReader(r, room+1)
	size, copyErr := io.Copy(f, limited)
	if copyErr == nil && size > room {
		copyErr = fmt.Errorf("%w: the upload needs more than %d bytes", ErrNoSpace, room)
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	if err := f.Close(); copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		os.Remove(part)
		if errors.Is(copyErr, ErrNoSpace) {
			return "", 0, copyErr
		}
		return "", 0, fmt.Errorf("write %s: %w", part, copyErr)
	}
	if err := os.Chmod(part, 0o644); err != nil {
		os.Remove(part)
		return "", 0, fmt.Errorf("set the mode of %s: %w", part, err)
	}
	if err := os.Rename(part, target); err != nil {
		os.Remove(part)
		return "", 0, fmt.Errorf("rename %s: %w", part, err)
	}
	fsutil.SyncDir(dir)

	l.log("media.upload", fmt.Sprintf("%s/%s %d bytes", name, safe, size))
	l.changed()
	return safe, size, nil
}

// room gives the number of bytes that an upload may take.
func (l *Library) room() int64 {
	free, err := fsutil.FreeBytes(l.opt.MediaRoot)
	if err != nil {
		// The free space is not known. An upload must still be possible, so the
		// only guard left is the reader limit, which this value makes generous.
		return 1 << 62
	}
	room := int64(free) - SpaceReserve
	if room < 0 {
		return 0
	}
	return room
}

// MediaFile is one file in a playlist directory, for GET /api/media/{playlist}.
type MediaFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Kind string `json:"kind"` // image | video | unknown
	// Src is the URL that serves the file, so the editor can show a picture.
	Src string `json:"src"`
	// InPlaylist is false for a file that nobody added to playlist.toml yet. A
	// person who copied a folder of pictures onto the stick from a laptop has a
	// directory full of them.
	InPlaylist bool `json:"in_playlist"`
}

// MediaFiles lists the files of a playlist directory and says which ones the
// playlist names.
//
// It exists so that the editor can offer a file that is on the stick and not in the
// playlist. Before it, a person who dropped twenty pictures in from a laptop saw an
// empty playlist and no way to add them but a hand edit of playlist.toml.
func (l *Library) MediaFiles(name string) ([]MediaFile, error) {
	dir, p, _, err := l.localPlaylist(name)
	if err != nil {
		return nil, err
	}
	used := make(map[string]bool, len(p.Items))
	for _, it := range p.Items {
		if it.File != "" {
			used[path.Base(it.File)] = true
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	out := make([]MediaFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !listableMedia(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, MediaFile{
			Name:       e.Name(),
			Size:       info.Size(),
			Kind:       playlist.Kind(playlist.Item{File: e.Name()}),
			Src:        l.srcURL(filepath.Join(dir, e.Name())),
			InPlaylist: used[e.Name()],
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

// listableMedia reports if a file name belongs in the list. playlist.toml is the
// description of the playlist and not content, and a name that starts with a full
// stop is a name that a person does not see. The staging file of an upload starts
// with ".upload-", so it is out for the same reason.
func listableMedia(name string) bool {
	if name == playlist.FileName || strings.HasPrefix(name, ".") {
		return false
	}
	switch playlist.Kind(playlist.Item{File: name}) {
	case playlist.KindImage, playlist.KindVideo:
		return true
	default:
		return false
	}
}

// DeleteMedia removes one file from a playlist directory. It does not change
// playlist.toml: the admin UI saves the playlist after it.
func (l *Library) DeleteMedia(name, file string) error {
	dir, _, _, err := l.localPlaylist(name)
	if err != nil {
		return err
	}
	safe := store.SafeName(file)
	if safe == "" || safe != file {
		// The name that the caller gave is not the name on the disk. A path
		// step, a separator or a strange character: refuse all of them.
		return ErrBadName
	}
	target := filepath.Join(dir, safe)
	if filepath.Dir(target) != dir {
		return ErrBadName
	}
	if info, err := os.Stat(target); err != nil || info.IsDir() {
		return ErrNoFile
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("remove %s: %w", target, err)
	}
	l.log("media.delete", name+"/"+safe)
	l.changed()
	return nil
}

// localPlaylist finds a playlist that the local admin may change. It refuses a
// name that is not a clean directory name and a playlist that the fleet server
// manages.
//
// parsed says if playlist.toml gave a playlist. A caller that writes the file
// again must not write the empty placeholder over a file that only needs a
// repair.
func (l *Library) localPlaylist(name string) (dir string, p playlist.Playlist, parsed bool, err error) {
	if name == "" || name != Slug(name) {
		return "", playlist.Playlist{}, false, ErrBadName
	}
	dir = filepath.Join(l.opt.MediaRoot, name)
	data, readErr := os.ReadFile(filepath.Join(dir, playlist.FileName))
	if readErr != nil {
		if found, ok := l.Snapshot().Find(name); ok && found.Fleet {
			return "", playlist.Playlist{}, false, ErrManaged
		}
		return "", playlist.Playlist{}, false, ErrNotFound
	}
	// A playlist that does not parse can still be replaced: the editor is how a
	// user repairs a bad hand edit. Keep the empty playlist in that case.
	p, parseErr := playlist.Parse(data, playlist.Options{})
	if parseErr != nil && !onlyEmptyItems(parseErr) {
		return dir, playlist.Playlist{Meta: playlist.Meta{Name: name}}, false, nil
	}
	return dir, p, true, nil
}

// writePlaylistFile renders and writes playlist.toml. The write is staged and
// committed with a rename, and it ends with an fsync (D41).
func writePlaylistFile(dir string, p playlist.Playlist) error {
	return fsutil.WriteFileAtomic(filepath.Join(dir, playlist.FileName), playlist.Render(p), 0o644)
}

// release gives a reserved upload name back.
func (l *Library) release(target string) {
	l.upload.Lock()
	delete(l.reserved, target)
	l.upload.Unlock()
}

// freeName gives a name that no file in dir uses and that no other upload holds.
// "promo.mp4" becomes "promo-2.mp4" when promo.mp4 is there. Replacing a file
// would change what every playlist that names it shows.
//
// The caller holds the upload lock, because reserved is read here and written by
// the caller.
func freeName(dir, name string, reserved map[string]bool) (string, string, error) {
	taken := func(path string) bool {
		if reserved[path] {
			return true
		}
		_, err := os.Stat(path)
		return err == nil
	}
	target := filepath.Join(dir, name)
	if !taken(target) {
		return target, name, nil
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 2; n < maxNameTries; n++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, n, ext)
		target = filepath.Join(dir, candidate)
		if !taken(target) {
			return target, candidate, nil
		}
	}
	// Every name is taken. Giving back the last one would write over a file that
	// a playlist item names.
	return "", "", fmt.Errorf("%w: %s and %d numbered names are in use", ErrExists, name, maxNameTries-2)
}
