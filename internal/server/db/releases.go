package db

import (
	"database/sql"
	"errors"
	"time"
)

// releaseColumns is the select list of a release row.
const releaseColumns = `version, approved, notes, published_at, approved_at,
	mirror_state, mirror_error`

// Releases gives every release row, newest first.
func (d *DB) Releases() ([]Release, error) {
	rows, err := d.r.Query(`SELECT ` + releaseColumns + `
		FROM releases ORDER BY published_at DESC, version DESC`)
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
	row := d.r.QueryRow(`SELECT `+releaseColumns+` FROM releases WHERE version = ?`, version)
	r, err := scanRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, ErrNotFound
	}
	return r, err
}

func scanRelease(s interface{ Scan(...any) error }) (Release, error) {
	var (
		r                       Release
		approved                int
		publishedAt, approvedAt string
	)
	if err := s.Scan(&r.Version, &approved, &r.Notes, &publishedAt,
		&approvedAt, &r.MirrorState, &r.MirrorError); err != nil {
		return Release{}, err
	}
	r.Approved = approved != 0
	r.PublishedAt = parseTime(publishedAt)
	r.ApprovedAt = parseTime(approvedAt)
	// Mirrored is computed and never stored. One value cannot disagree with
	// itself, and the device gate reads the same state as the admin page.
	r.Mirrored = r.MirrorState == MirrorDone
	return r, nil
}

// ApprovedRelease gives the one approved release, or ErrNotFound when there is
// none. Only one release is approved at a time (D26, D28). One query gives the
// whole row.
func (d *DB) ApprovedRelease() (Release, error) {
	row := d.r.QueryRow(`SELECT ` + releaseColumns + ` FROM releases WHERE approved = 1 LIMIT 1`)
	r, err := scanRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, ErrNotFound
	}
	return r, err
}

// NoteReleases writes what the release list of GitHub said, in one transaction.
// It never touches the approval or the mirror state: those belong to the admin and
// to the mirror.
//
// One transaction and not one statement for each release: the write pool holds one
// connection, and a page load that wrote thirty rows one at a time would fight
// every heartbeat of the fleet for the lock.
func (d *DB) NoteReleases(list []ReleaseNote) error {
	if len(list) == 0 {
		return nil
	}
	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, rel := range list {
		stamp := ""
		if !rel.PublishedAt.IsZero() {
			stamp = d.stamp(rel.PublishedAt)
		}
		if _, err := tx.Exec(`INSERT INTO releases (version, notes, published_at)
			VALUES (?, ?, ?)
			ON CONFLICT(version) DO UPDATE SET notes = excluded.notes,
				published_at = excluded.published_at`,
			rel.Version, rel.Notes, stamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReleaseNote is what the release list of GitHub says about one version.
type ReleaseNote struct {
	Version     string
	Notes       string
	PublishedAt time.Time
}

// ApproveRelease makes one version the approved version and takes the approval
// away from every other release. Devices only ever install this version (D28).
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

// SetMirrorState writes the state of the mirror of one release.
func (d *DB) SetMirrorState(version, state, errText string) error {
	_, err := d.w.Exec(`INSERT INTO releases (version, mirror_state, mirror_error)
		VALUES (?, ?, ?)
		ON CONFLICT(version) DO UPDATE SET mirror_state = excluded.mirror_state,
			mirror_error = excluded.mirror_error`,
		version, state, errText)
	return err
}

// resetWorkingMirrors turns every mirror that says "working" into a failure. Open
// calls it.
//
// A mirror runs in a goroutine of the process. A process that stopped in the
// middle of one leaves a row that says "working" for ever, and the admin page then
// offers no button: the mirror route answers 409 because it believes that a mirror
// runs.
func (d *DB) resetWorkingMirrors() error {
	_, err := d.w.Exec(`UPDATE releases SET mirror_state = ?, mirror_error = ?
		WHERE mirror_state = ?`,
		MirrorFailed, "the server stopped while it mirrored this version", MirrorWorking)
	return err
}
