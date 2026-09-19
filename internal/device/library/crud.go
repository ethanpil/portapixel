package library

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/playlist"
	"github.com/ethanpil/portapixel/internal/store"
)

// The faults that the API turns into a status code.
var (
	ErrNotFound = errors.New("there is no playlist with this name")
	ErrExists   = errors.New("a playlist with this name is on the device")
	ErrBadName  = errors.New("this name cannot be a directory name")
	ErrManaged  = errors.New("the fleet server manages this playlist")
	ErrNoFile   = errors.New("there is no file with this name")
)

// Slug makes a directory name from a title: lower case, letters, digits and
// hyphens. It gives "" when nothing is left, and the caller then reports
// ErrBadName. A directory name must be safe on exFAT, safe in a URL and safe in
// a shell, because a person reads this card on a laptop.
func Slug(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
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
	l.Rescan()
	return name, nil
}

// RenamePlaylist moves a playlist directory and sets the title in its
// playlist.toml. It gives the new directory name.
//
// The rename does not follow the references in the schedule rules: the
// configuration names playlists by directory name, so the admin UI must correct
// the rules. The API answer carries the new name for that reason.
func (l *Library) RenamePlaylist(name, title string) (string, error) {
	dir, p, err := l.localPlaylist(name)
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
	p.Meta.Name = strings.TrimSpace(title)
	if err := writePlaylistFile(target, p); err != nil {
		return "", err
	}
	l.log("playlist.rename", name+" -> "+next)
	l.Rescan()
	return next, nil
}

// DeletePlaylist removes a playlist directory and everything in it.
func (l *Library) DeletePlaylist(name string) error {
	dir, _, err := l.localPlaylist(name)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	l.log("playlist.delete", name)
	l.Rescan()
	return nil
}

// SavePlaylist writes playlist.toml again from p. The stock comments come back
// and the comments of the user are lost, which the admin UI says before it saves
// (D15).
//
// A playlist that breaks a rule gives playlist.Errors, which the API sends as
// 422 with one message for each field.
func (l *Library) SavePlaylist(name string, p playlist.Playlist) error {
	dir, _, err := l.localPlaylist(name)
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
	l.Rescan()
	return nil
}

// AddMedia writes an uploaded file into a playlist directory. It gives the name
// that the file has on the disk.
//
// The stream goes to a .part file and a rename commits it, so a connection that
// drops leaves no half file in the playlist (D41). The name is made safe, and a
// name that is already in use gets a number, so an upload can never replace a
// file that another item uses.
func (l *Library) AddMedia(name, filename string, r io.Reader) (string, int64, error) {
	dir, _, err := l.localPlaylist(name)
	if err != nil {
		return "", 0, err
	}
	safe := store.SafeName(filename)
	if safe == "" {
		return "", 0, ErrBadName
	}
	// store.SafeName removes every path separator, so the file cannot leave the
	// directory. This check is the second lock on that door.
	target := filepath.Join(dir, safe)
	if filepath.Dir(target) != dir {
		return "", 0, ErrBadName
	}
	target, safe = freeName(dir, safe)

	part := target + ".part"
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", 0, fmt.Errorf("create %s: %w", part, err)
	}
	size, copyErr := io.Copy(f, r)
	if copyErr == nil {
		copyErr = f.Sync()
	}
	if err := f.Close(); copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		os.Remove(part)
		return "", 0, fmt.Errorf("write %s: %w", part, copyErr)
	}
	if err := os.Rename(part, target); err != nil {
		os.Remove(part)
		return "", 0, fmt.Errorf("rename %s: %w", part, err)
	}
	fsutil.SyncDir(dir)

	l.log("media.upload", fmt.Sprintf("%s/%s %d bytes", name, safe, size))
	l.Rescan()
	return safe, size, nil
}

// DeleteMedia removes one file from a playlist directory. It does not change
// playlist.toml: the admin UI saves the playlist after it.
func (l *Library) DeleteMedia(name, file string) error {
	dir, _, err := l.localPlaylist(name)
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
	l.Rescan()
	return nil
}

// localPlaylist finds a playlist that the local admin may change. It refuses a
// name that is not a clean directory name and a playlist that the fleet server
// manages.
func (l *Library) localPlaylist(name string) (string, playlist.Playlist, error) {
	if name == "" || name != Slug(name) {
		return "", playlist.Playlist{}, ErrBadName
	}
	dir := filepath.Join(l.opt.MediaRoot, name)
	data, err := os.ReadFile(filepath.Join(dir, playlist.FileName))
	if err != nil {
		if p, ok := l.Snapshot().Find(name); ok && p.Fleet {
			return "", playlist.Playlist{}, ErrManaged
		}
		return "", playlist.Playlist{}, ErrNotFound
	}
	// A playlist that does not parse can still be replaced: the editor is how a
	// user repairs a bad hand edit. Keep the empty playlist in that case.
	p, err := playlist.Parse(data, playlist.Options{})
	if err != nil && !onlyEmptyItems(err) {
		p = playlist.Playlist{Meta: playlist.Meta{Name: name}}
	}
	return dir, p, nil
}

// writePlaylistFile renders and writes playlist.toml. The write is staged and
// committed with a rename, and it ends with an fsync (D41).
func writePlaylistFile(dir string, p playlist.Playlist) error {
	return fsutil.WriteFileAtomic(filepath.Join(dir, playlist.FileName), playlist.Render(p), 0o644)
}

// freeName gives a name that no file in dir uses. "promo.mp4" becomes
// "promo-2.mp4" when promo.mp4 is there. Replacing a file would change what
// every playlist that names it shows.
func freeName(dir, name string) (string, string) {
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); err != nil {
		return target, name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, n, ext)
		target = filepath.Join(dir, candidate)
		if _, err := os.Stat(target); err != nil {
			return target, candidate
		}
	}
	return target, name
}
