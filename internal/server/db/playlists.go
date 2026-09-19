package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/ethanpil/portapixel/internal/playlist"
	"github.com/ethanpil/portapixel/internal/slug"
	"github.com/ethanpil/portapixel/internal/store"
)

// FieldError is one thing that is wrong with a request. The API sends a list of
// them with status 422.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Errors is the list of things that are wrong with a request.
type Errors []FieldError

func (e Errors) Error() string {
	parts := make([]string, len(e))
	for i, fe := range e {
		parts[i] = fe.Field + ": " + fe.Message
	}
	return strings.Join(parts, "; ")
}

// Playlists gives every playlist with its items.
func (d *DB) Playlists() ([]Playlist, error) {
	rows, err := d.r.Query(`SELECT id, name, title, transition, shuffle, updated_at
		FROM playlists ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Playlist
	for rows.Next() {
		p, err := scanPlaylist(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Items, err = d.playlistItems(out[i].ID); err != nil {
			return nil, err
		}
		if out[i].Devices, err = d.PlaylistDeviceCount(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Playlist gives one playlist with its items and the number of devices that get
// it. The admin UI shows the count.
func (d *DB) Playlist(id int64) (Playlist, error) {
	p, err := d.PlaylistNoCount(id)
	if err != nil {
		return Playlist{}, err
	}
	p.Devices, err = d.PlaylistDeviceCount(id)
	return p, err
}

// PlaylistNoCount gives one playlist with its items and no device count.
//
// The count is a scan of the devices table with four correlated EXISTS clauses.
// The manifest path of a device throws the number away, and it asks for one
// playlist for each of its rules on every poll, so the count belongs to the admin
// callers only. An error in an admin-only count must also never stop a manifest:
// then one bad number would take the whole fleet off the air.
func (d *DB) PlaylistNoCount(id int64) (Playlist, error) {
	row := d.r.QueryRow(`SELECT id, name, title, transition, shuffle, updated_at
		FROM playlists WHERE id = ?`, id)
	p, err := scanPlaylist(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Playlist{}, ErrNotFound
	}
	if err != nil {
		return Playlist{}, err
	}
	p.Items, err = d.playlistItems(id)
	return p, err
}

func scanPlaylist(s interface{ Scan(...any) error }) (Playlist, error) {
	var (
		p         Playlist
		shuffle   int
		updatedAt string
	)
	if err := s.Scan(&p.ID, &p.Name, &p.Title, &p.Transition, &shuffle, &updatedAt); err != nil {
		return Playlist{}, err
	}
	if shuffle >= 0 {
		v := shuffle != 0
		p.Shuffle = &v
	}
	p.UpdatedAt = parseTime(updatedAt)
	return p, nil
}

// playlistItems gives the items of one playlist in their order.
func (d *DB) playlistItems(id int64) ([]PlaylistItem, error) {
	rows, err := d.r.Query(`SELECT COALESCE(i.media_sha, ''), i.url, i.name, i.duration, i.mute,
			i.max_duration, i.refresh_seconds, COALESCE(m.has_thumb, 0), COALESCE(m.orig_name, ''),
			COALESCE(m.size, 0), m.sha256 IS NOT NULL
		FROM playlist_items i LEFT JOIN media m ON m.sha256 = i.media_sha
		WHERE i.playlist_id = ? ORDER BY i.position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []PlaylistItem{}
	for rows.Next() {
		var (
			it        PlaylistItem
			mute      int
			hasThumb  int
			origName  string
			haveMedia int
		)
		if err := rows.Scan(&it.SHA256, &it.URL, &it.Name, &it.Duration, &mute,
			&it.MaxDuration, &it.RefreshSeconds, &hasThumb, &origName,
			&it.Size, &haveMedia); err != nil {
			return nil, err
		}
		it.Mute = mute != 0
		it.MediaRow = haveMedia != 0
		it.MediaName = origName
		if it.Name == "" {
			it.Name = origName
			if it.Name == "" {
				it.Name = it.URL
			}
		}
		it.Kind = itemKind(it)
		if hasThumb != 0 {
			it.Thumb = "/api/admin/media/" + it.SHA256 + "/thumb"
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// itemKind says what an item is. It asks internal/playlist, so the words that the
// server uses and the words that the device uses are the same words.
//
// A media item carries a hash and a name, and the extension of the name is what
// says image or video. A url item carries no hash. The name of a url item is the
// URL itself, so the two cases go to Kind apart: an item with a file and a url
// together is not a kind, it is an error, and the editor catches it.
func itemKind(it PlaylistItem) string {
	if it.SHA256 == "" && it.URL != "" {
		return playlist.Kind(playlist.Item{URL: it.URL})
	}
	return playlist.Kind(playlist.Item{File: it.Name})
}

// PlaylistDeviceCount counts the devices that get this playlist. The editor
// shows the number, because a save reaches every one of them.
//
// A device gets a playlist when a device rule of its own names it, or when it
// has no device rule and a rule or the default of its group names it.
func (d *DB) PlaylistDeviceCount(id int64) (int, error) {
	var n int
	err := d.r.QueryRow(`
		SELECT COUNT(*) FROM devices d WHERE
		  EXISTS (SELECT 1 FROM assignments a WHERE a.device_id = d.id AND a.playlist_id = ?)
		  OR d.default_playlist_id = ?
		  OR (NOT EXISTS (SELECT 1 FROM assignments a WHERE a.device_id = d.id)
		      AND d.default_playlist_id IS NULL
		      AND (EXISTS (SELECT 1 FROM assignments a WHERE a.group_id = d.group_id AND a.playlist_id = ?)
		           OR EXISTS (SELECT 1 FROM groups g WHERE g.id = d.group_id AND g.default_playlist_id = ?)))
		`, id, id, id, id).Scan(&n)
	return n, err
}

// SavePlaylist makes or replaces a playlist and all its items. The items always
// go in as a whole list: a playlist is one document in the editor, and a
// partial update would need a second set of rules for no gain.
//
// An ID of 0 makes a new playlist. The name comes from the title, and a name
// that another playlist already holds is an error.
func (d *DB) SavePlaylist(p Playlist) (int64, error) {
	if errs := d.validatePlaylist(p); len(errs) > 0 {
		return 0, errs
	}
	name := p.Name
	if name == "" {
		name = slug.Make(p.Title)
	} else {
		name = slug.Make(name)
	}
	if name == "" {
		return 0, Errors{{Field: "name", Message: "the name must hold a letter or a digit"}}
	}
	shuffle := -1
	if p.Shuffle != nil {
		if *p.Shuffle {
			shuffle = 1
		} else {
			shuffle = 0
		}
	}
	title := p.Title
	if title == "" {
		title = name
	}

	tx, err := d.w.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var taken int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM playlists WHERE name = ? AND id <> ?`,
		name, p.ID).Scan(&taken); err != nil {
		return 0, err
	}
	if taken > 0 {
		return 0, Errors{{Field: "name", Message: "another playlist already has the name " + name}}
	}

	id := p.ID
	stamp := d.stamp(d.now())
	if id == 0 {
		res, err := tx.Exec(`INSERT INTO playlists (name, title, transition, shuffle, updated_at)
			VALUES (?, ?, ?, ?, ?)`, name, title, p.Transition, shuffle, stamp)
		if err != nil {
			return 0, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, err
		}
	} else {
		res, err := tx.Exec(`UPDATE playlists SET name = ?, title = ?, transition = ?,
			shuffle = ?, updated_at = ? WHERE id = ?`, name, title, p.Transition, shuffle, stamp, id)
		if err != nil {
			return 0, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return 0, err
		} else if n == 0 {
			return 0, ErrNotFound
		}
		if _, err := tx.Exec(`DELETE FROM playlist_items WHERE playlist_id = ?`, id); err != nil {
			return 0, err
		}
	}

	for i, it := range p.Items {
		mute := 0
		if it.Mute {
			mute = 1
		}
		if _, err := tx.Exec(`INSERT INTO playlist_items
			(playlist_id, position, media_sha, url, name, duration, mute, max_duration, refresh_seconds)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i, nullString(it.SHA256), it.URL, it.Name, it.Duration, mute,
			it.MaxDuration, it.RefreshSeconds); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// validatePlaylist checks a playlist from the editor.
//
// The item rules come from internal/playlist. Each item becomes the item that
// the device will write into _fleet/<name>/playlist.toml, and Validate of that
// package gives the answer. One set of rules holds for the two ends, so the
// server cannot accept a playlist that the device would then refuse.
func (d *DB) validatePlaylist(p Playlist) Errors {
	var errs Errors
	local := playlist.Playlist{
		Meta:  playlist.Meta{Name: p.Title, Transition: p.Transition, Shuffle: p.Shuffle},
		Items: make([]playlist.Item, len(p.Items)),
	}
	for i, it := range p.Items {
		field := fmt.Sprintf("items[%d]", i)
		switch {
		case it.SHA256 != "" && it.URL != "":
			errs = append(errs, FieldError{field, "has a file and a url; use one of them"})
		case it.SHA256 != "":
			if !store.IsSHA256(it.SHA256) {
				errs = append(errs, FieldError{field + ".sha256", "is not a SHA-256 value of 64 lower case hex characters"})
				continue
			}
			var n int
			if err := d.r.QueryRow(`SELECT COUNT(*) FROM media WHERE sha256 = ?`, it.SHA256).Scan(&n); err != nil {
				errs = append(errs, FieldError{field + ".sha256", "could not be checked: " + err.Error()})
				continue
			}
			if n == 0 {
				errs = append(errs, FieldError{field + ".sha256", "names a file that the media library does not hold"})
				continue
			}
			// The name carries the extension, which is what says image or video.
			local.Items[i] = playlist.Item{File: it.Name, Duration: it.Duration,
				Mute: it.Mute, MaxDuration: it.MaxDuration, RefreshSeconds: it.RefreshSeconds}
		default:
			local.Items[i] = playlist.Item{URL: it.URL, Duration: it.Duration,
				Mute: it.Mute, MaxDuration: it.MaxDuration, RefreshSeconds: it.RefreshSeconds}
		}
	}
	if len(errs) > 0 {
		return errs
	}
	for _, fe := range local.Validate(playlist.Options{}) {
		// internal/playlist names the list "item"; the editor names it "items".
		field := strings.Replace(fe.Field, "item[", "items[", 1)
		if field == "item" {
			field = "items"
		}
		field = strings.Replace(field, ".file", ".sha256", 1)
		field = strings.Replace(field, "playlist.transition", "transition", 1)
		errs = append(errs, FieldError{field, fe.Message})
	}
	return errs
}

// DeletePlaylist removes a playlist and its items. A playlist that an
// assignment names stays: the foreign key would take the assignment with it, and
// a screen would then quietly change what it plays.
// The check and the delete are one write transaction. Without it a rule that
// takes the playlist between the two statements would go away with it, and a
// screen would quietly change what it plays.
func (d *DB) DeletePlaylist(id int64) error {
	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow(`SELECT
		(SELECT COUNT(*) FROM assignments WHERE playlist_id = ?)
		+ (SELECT COUNT(*) FROM groups WHERE default_playlist_id = ?)
		+ (SELECT COUNT(*) FROM devices WHERE default_playlist_id = ?)`,
		id, id, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	res, err := tx.Exec(`DELETE FROM playlists WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err != nil {
		return err
	} else if rows == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}
