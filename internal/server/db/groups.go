package db

import (
	"database/sql"
	"errors"
	"strings"
)

// ErrInUse says that a row cannot go away because another row points at it.
var ErrInUse = errors.New("this record is in use")

// Groups gives every group with the number of devices in it.
func (d *DB) Groups() ([]Group, error) {
	rows, err := d.r.Query(`SELECT g.id, g.name, g.default_playlist_id,
			g.screen_on, g.screen_off, g.screen_days, g.created_at,
			(SELECT COUNT(*) FROM devices WHERE group_id = g.id)
		FROM groups g ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Group gives one group.
func (d *DB) Group(id int64) (Group, error) {
	row := d.r.QueryRow(`SELECT g.id, g.name, g.default_playlist_id,
			g.screen_on, g.screen_off, g.screen_days, g.created_at,
			(SELECT COUNT(*) FROM devices WHERE group_id = g.id)
		FROM groups g WHERE g.id = ?`, id)
	g, err := scanGroup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	return g, err
}

func scanGroup(s interface{ Scan(...any) error }) (Group, error) {
	var (
		g          Group
		playlistID sql.NullInt64
		createdAt  string
	)
	err := s.Scan(&g.ID, &g.Name, &playlistID, &g.ScreenOn, &g.ScreenOff, &g.ScreenDays,
		&createdAt, &g.Devices)
	if err != nil {
		return Group{}, err
	}
	g.DefaultPlaylistID = intFromNull(playlistID)
	g.CreatedAt = parseTime(createdAt)
	return g, nil
}

// CreateGroup makes a group and gives its ID.
func (d *DB) CreateGroup(name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("a group needs a name")
	}
	res, err := d.w.Exec(`INSERT INTO groups (name, created_at) VALUES (?, ?)`,
		name, d.stamp(d.now()))
	if err != nil {
		if isUniqueError(err) {
			return 0, errors.Join(ErrDuplicate, err)
		}
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateGroup sets the name, the default playlist and the screen rule.
func (d *DB) UpdateGroup(g Group) error {
	name := strings.TrimSpace(g.Name)
	if name == "" {
		return errors.New("a group needs a name")
	}
	return d.affectOne(`UPDATE groups SET name = ?, default_playlist_id = ?,
		screen_on = ?, screen_off = ?, screen_days = ? WHERE id = ?`,
		name, nullInt64(g.DefaultPlaylistID), g.ScreenOn, g.ScreenOff, g.ScreenDays, g.ID)
}

// DeleteGroup removes a group. A group that holds devices stays: moving the
// devices first is a decision for the admin, not for the server.
//
// The count and the delete are one write transaction. Without it a device that
// joins the group between the two statements would lose its group without
// anybody asking for it.
func (d *DB) DeleteGroup(id int64) error {
	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM devices WHERE group_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	res, err := tx.Exec(`DELETE FROM groups WHERE id = ?`, id)
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
