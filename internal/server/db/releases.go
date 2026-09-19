package db

import (
	"database/sql"
	"errors"
	"time"
)

// Releases gives every release row, newest first.
func (d *DB) Releases() ([]Release, error) {
	rows, err := d.r.Query(`SELECT version, approved, mirrored, notes, published_at,
		approved_at, mirror_state, mirror_error FROM releases ORDER BY published_at DESC, version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Release{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Release gives one release row.
func (d *DB) Release(version string) (Release, error) {
	row := d.r.QueryRow(`SELECT version, approved, mirrored, notes, published_at,
		approved_at, mirror_state, mirror_error FROM releases WHERE version = ?`, version)
	r, err := scanRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, ErrNotFound
	}
	return r, err
}

func scanRelease(s interface{ Scan(...any) error }) (Release, error) {
	var (
		r                       Release
		approved, mirrored      int
		publishedAt, approvedAt string
	)
	if err := s.Scan(&r.Version, &approved, &mirrored, &r.Notes, &publishedAt,
		&approvedAt, &r.MirrorState, &r.MirrorError); err != nil {
		return Release{}, err
	}
	r.Approved = approved != 0
	r.Mirrored = mirrored != 0
	r.PublishedAt = parseTime(publishedAt)
	r.ApprovedAt = parseTime(approvedAt)
	return r, nil
}

// ApprovedRelease gives the one approved release, or ErrNotFound when there is
// none. Only one release is approved at a time (D26, D28).
func (d *DB) ApprovedRelease() (Release, error) {
	var version string
	err := d.r.QueryRow(`SELECT version FROM releases WHERE approved = 1 LIMIT 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, ErrNotFound
	}
	if err != nil {
		return Release{}, err
	}
	return d.Release(version)
}

// NoteRelease writes what the release list of GitHub said. It never touches the
// approval or the mirror state: those belong to the admin and to the mirror.
func (d *DB) NoteRelease(version, notes string, published time.Time) error {
	stamp := ""
	if !published.IsZero() {
		stamp = d.stamp(published)
	}
	_, err := d.w.Exec(`INSERT INTO releases (version, notes, published_at)
		VALUES (?, ?, ?)
		ON CONFLICT(version) DO UPDATE SET notes = excluded.notes, published_at = excluded.published_at`,
		version, notes, stamp)
	return err
}

// Approve marks one release as the approved version and takes the approval away
// from every other release. Devices only ever install this version (D28).
func (d *DB) ApproveRelease(version string) error {
	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM releases WHERE version = ?`, version).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE releases SET approved = 0, approved_at = '' WHERE approved = 1`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE releases SET approved = 1, approved_at = ? WHERE version = ?`,
		d.stamp(d.now()), version); err != nil {
		return err
	}
	return tx.Commit()
}

// UnapproveRelease takes the approval away. The devices then stay where they are.
func (d *DB) UnapproveRelease(version string) error {
	return d.affectOne(`UPDATE releases SET approved = 0, approved_at = '' WHERE version = ?`, version)
}

// SetMirrorState writes the state of the mirror of one release. A state of
// MirrorDone also sets the mirrored flag, and every other state clears it: a
// release is only ready for devices when every file is there and verified.
func (d *DB) SetMirrorState(version, state, errText string) error {
	mirrored := 0
	if state == MirrorDone {
		mirrored = 1
	}
	_, err := d.w.Exec(`INSERT INTO releases (version, mirror_state, mirror_error, mirrored)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(version) DO UPDATE SET mirror_state = excluded.mirror_state,
			mirror_error = excluded.mirror_error, mirrored = excluded.mirrored`,
		version, state, errText, mirrored)
	return err
}
