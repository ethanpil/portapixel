package db

import (
	"database/sql"
	"errors"
)

// mediaColumns is the select list of a media row.
const mediaColumns = `sha256, orig_name, size, mime, width, height, has_thumb, uploaded_at`

// MediaList gives every object of the library, newest first, with the playlists
// that use each one.
//
// One query names the users of every object. A query for each row would cost 501
// queries for a library of 500 files, on one page load of the media library.
func (d *DB) MediaList() ([]Media, error) {
	rows, err := d.r.Query(`SELECT ` + mediaColumns + `
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

	users, err := d.allMediaUsers()
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Playlists = users[out[i].SHA256]
	}
	return out, nil
}

// allMediaUsers names the playlists of every object in one query.
func (d *DB) allMediaUsers() (map[string][]string, error) {
	rows, err := d.r.Query(`SELECT DISTINCT i.media_sha, p.name
		FROM playlist_items i JOIN playlists p ON p.id = i.playlist_id
		WHERE i.media_sha IS NOT NULL ORDER BY i.media_sha, p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]string{}
	for rows.Next() {
		var sha, name string
		if err := rows.Scan(&sha, &name); err != nil {
			return nil, err
		}
		out[sha] = append(out[sha], name)
	}
	return out, rows.Err()
}

// Media gives one object with the playlists that use it. The admin media page
// needs the list; a device path uses MediaRow or MediaExists instead.
func (d *DB) Media(sha string) (Media, error) {
	m, err := d.MediaRow(sha)
	if err != nil {
		return Media{}, err
	}
	m.Playlists, err = d.mediaUsers(sha)
	return m, err
}

// MediaRow gives one object with no list of playlists. One query, for the paths
// that only need the row: the upload answer and the manifest.
func (d *DB) MediaRow(sha string) (Media, error) {
	row := d.r.QueryRow(`SELECT `+mediaColumns+` FROM media WHERE sha256 = ?`, sha)
	m, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, ErrNotFound
	}
	return m, err
}

// MediaExists reports if the library holds this object. The download route of the
// device needs the answer and nothing else, and this is one query with no join.
func (d *DB) MediaExists(sha string) (bool, error) {
	var one int
	err := d.r.QueryRow(`SELECT 1 FROM media WHERE sha256 = ?`, sha).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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

// mediaUsers names the playlists that hold this object.
func (d *DB) mediaUsers(sha string) ([]string, error) {
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
//
// The check and the delete are one write transaction. Without it a playlist that
// takes the object between the two statements would keep an item whose file is
// gone.
func (d *DB) DeleteMedia(sha string) ([]string, error) {
	tx, err := d.w.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT DISTINCT p.name FROM playlist_items i
		JOIN playlists p ON p.id = i.playlist_id WHERE i.media_sha = ? ORDER BY p.name`, sha)
	if err != nil {
		return nil, err
	}
	var users []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		users = append(users, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(users) > 0 {
		return users, ErrInUse
	}

	res, err := tx.Exec(`DELETE FROM media WHERE sha256 = ?`, sha)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, ErrNotFound
	}
	return nil, tx.Commit()
}

// MediaTotals counts the library for the media page and the health page.
func (d *DB) MediaTotals() (files int, bytes int64, err error) {
	err = d.r.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size), 0) FROM media`).Scan(&files, &bytes)
	return files, bytes, err
}

// KnownHashes names every object that the table holds. The media store calls it
// when it sweeps: a blob with no row is a blob that nothing can reach, and only
// the table knows which ones those are.
func (d *DB) KnownHashes() (map[string]bool, error) {
	rows, err := d.r.Query(`SELECT sha256 FROM media`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		out[sha] = true
	}
	return out, rows.Err()
}
