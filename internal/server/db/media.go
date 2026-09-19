package db

import (
	"database/sql"
	"errors"
)

// MediaList gives every object of the library, newest first, with the playlists
// that use each one.
func (d *DB) MediaList() ([]Media, error) {
	rows, err := d.r.Query(`SELECT sha256, orig_name, size, mime, width, height, has_thumb, uploaded_at
		FROM media ORDER BY uploaded_at DESC, orig_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Media{}
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Playlists, err = d.MediaUsers(out[i].SHA256); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Media gives one object.
func (d *DB) Media(sha string) (Media, error) {
	row := d.r.QueryRow(`SELECT sha256, orig_name, size, mime, width, height, has_thumb, uploaded_at
		FROM media WHERE sha256 = ?`, sha)
	m, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, ErrNotFound
	}
	if err != nil {
		return Media{}, err
	}
	m.Playlists, err = d.MediaUsers(sha)
	return m, err
}

func scanMedia(s interface{ Scan(...any) error }) (Media, error) {
	var (
		m          Media
		hasThumb   int
		uploadedAt string
	)
	if err := s.Scan(&m.SHA256, &m.OrigName, &m.Size, &m.MIME, &m.Width, &m.Height,
		&hasThumb, &uploadedAt); err != nil {
		return Media{}, err
	}
	m.HasThumb = hasThumb != 0
	m.UploadedAt = parseTime(uploadedAt)
	return m, nil
}

// MediaUsers names the playlists that hold this object.
func (d *DB) MediaUsers(sha string) ([]string, error) {
	rows, err := d.r.Query(`SELECT DISTINCT p.name FROM playlist_items i
		JOIN playlists p ON p.id = i.playlist_id WHERE i.media_sha = ? ORDER BY p.name`, sha)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// AddMedia writes the row of an object that the store now holds. An object that
// is already there keeps its first name and its first upload time: the store
// holds one copy of one file, and a second upload of the same bytes is the same
// object (D27).
func (d *DB) AddMedia(m Media) error {
	hasThumb := 0
	if m.HasThumb {
		hasThumb = 1
	}
	_, err := d.w.Exec(`INSERT INTO media
		(sha256, orig_name, size, mime, width, height, has_thumb, uploaded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(sha256) DO NOTHING`,
		m.SHA256, m.OrigName, m.Size, m.MIME, m.Width, m.Height, hasThumb, d.stamp(d.now()))
	return err
}

// DeleteMedia removes the row of an object. It refuses an object that a playlist
// holds and names the playlists (plan section 12: the UI shows the list).
func (d *DB) DeleteMedia(sha string) ([]string, error) {
	users, err := d.MediaUsers(sha)
	if err != nil {
		return nil, err
	}
	if len(users) > 0 {
		return users, ErrInUse
	}
	return nil, d.affectOne(`DELETE FROM media WHERE sha256 = ?`, sha)
}

// MediaTotals counts the library for the media page and the health page.
func (d *DB) MediaTotals() (files int, bytes int64, err error) {
	err = d.r.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM media`).Scan(&files, &bytes)
	return files, bytes, err
}
