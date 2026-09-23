package db

import (
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound says that no row has this key. Every route turns it into a 404.
var ErrNotFound = errors.New("there is no record with this name")

// ErrDuplicate says that another row already holds this name. Every route turns
// it into a 409 or a 422 on the name field.
var ErrDuplicate = errors.New("another record already has this name")

// deviceColumns is the select list of a device row. One constant keeps the order
// of the columns and the order of scanDevice together.
const deviceColumns = `d.id, d.name, d.group_id, d.hardware_id,
	d.needs_confirm, d.pending_hardware_id, d.conflict, d.conflict_hardware_id,
	d.version, d.status_json, d.sync_error, d.last_ip, d.poll_seconds,
	d.default_playlist_id, d.screen_on, d.screen_off, d.screen_days,
	d.created_at, d.paired_at, d.last_seen,
	COALESCE(g.name, '')`

// scanDevice reads one device row.
//
// While needs_confirm is set, HardwareID is the machine that asks and
// PrevHardwareID is the machine that the server knows. The UI shows the two
// values on the hardware-swap card, and the server stores the change at the
// confirmation and not before (D21).
func scanDevice(s interface{ Scan(...any) error }) (Device, error) {
	var (
		d                             Device
		groupID, playlistID           sql.NullInt64
		createdAt, pairedAt, lastSeen string
		needsConfirm, conflictInt     int
		pendingHardware               string
	)
	err := s.Scan(&d.ID, &d.Name, &groupID, &d.HardwareID,
		&needsConfirm, &pendingHardware, &conflictInt, &d.ConflictHardwareID,
		&d.Version, &d.Status, &d.SyncError, &d.LastIP, &d.PollSeconds,
		&playlistID, &d.ScreenOn, &d.ScreenOff, &d.ScreenDays,
		&createdAt, &pairedAt, &lastSeen,
		&d.GroupName)
	if err != nil {
		return Device{}, err
	}
	d.GroupID = intFromNull(groupID)
	d.DefaultPlaylistID = intFromNull(playlistID)
	d.NeedsConfirm = needsConfirm != 0
	d.Conflict = conflictInt != 0
	if d.NeedsConfirm && pendingHardware != "" {
		d.PrevHardwareID = d.HardwareID
		d.HardwareID = pendingHardware
	}
	d.CreatedAt = parseTime(createdAt)
	d.PairedAt = parseTime(pairedAt)
	d.LastSeen = parseTime(lastSeen)
	return d, nil
}

// State says what word the admin UI shows for this device.
//
// A device is online while its last call is inside 2.5 poll intervals. The
// factor gives one missed poll before the row changes: a device that answers
// every 60 s and misses one call at 61 s is not a fault worth a red dot.
func State(d Device, defaultPoll int, now time.Time) string {
	switch {
	case d.Pending:
		return StatePending
	case d.Conflict:
		return StateConflict
	case d.NeedsConfirm:
		return StateNeedsConfirm
	}
	if d.LastSeen.IsZero() {
		return StateOffline
	}
	poll := d.PollSeconds
	if poll <= 0 {
		poll = defaultPoll
	}
	if poll <= 0 {
		poll = 60
	}
	age := now.Sub(d.LastSeen)
	switch {
	case age <= time.Duration(float64(poll)*2.5)*time.Second:
		return StateOnline
	case age <= 24*time.Hour:
		return StateQuiet
	default:
		return StateOffline
	}
}

// Devices gives every device, newest contact first. The caller fills State.
func (d *DB) Devices() ([]Device, error) {
	rows, err := d.r.Query(`SELECT ` + deviceColumns + `
		FROM devices d LEFT JOIN groups g ON g.id = d.group_id
		ORDER BY d.last_seen DESC, d.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		dev, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dev)
	}
	return out, rows.Err()
}

// Device gives one device by its ID.
func (d *DB) Device(id string) (Device, error) {
	row := d.r.QueryRow(`SELECT `+deviceColumns+`
		FROM devices d LEFT JOIN groups g ON g.id = d.group_id WHERE d.id = ?`, id)
	dev, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	return dev, err
}

// DeviceByToken gives the device that holds this token. The token arrives in
// plain form and the table holds its SHA-256, so the lookup uses the hash.
//
// One query gives the whole row. Every authenticated call of a device goes
// through here, so a second query for the same row would double the cost of the
// hot path.
//
// The hash that came back is compared with the hash that we asked for in constant
// time. The index lookup found the row, so the second step adds little by itself.
// It is here so that the one place that accepts a device token can never become a
// comparison that tells an attacker how much of a token is right.
func (d *DB) DeviceByToken(token string) (Device, error) {
	if token == "" {
		return Device{}, ErrNotFound
	}
	want := hashSecret(token)
	row := d.r.QueryRow(`SELECT `+deviceColumns+`, d.token_hash
		FROM devices d LEFT JOIN groups g ON g.id = d.group_id
		WHERE d.token_hash = ? AND d.token_hash <> ''`, want)

	// scanDevice reads the columns of deviceColumns. The token hash is one column
	// more, so this wrapper takes it off the end.
	var got string
	dev, err := scanDevice(scanWithTail{row: row, tail: []any{&got}})
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, err
	}
	if !equalHash(got, want) {
		return Device{}, ErrNotFound
	}
	return dev, nil
}

// scanWithTail lets scanDevice read a row that carries columns of its own after
// the columns of deviceColumns.
type scanWithTail struct {
	row  interface{ Scan(...any) error }
	tail []any
}

func (s scanWithTail) Scan(dest ...any) error {
	return s.row.Scan(append(dest, s.tail...)...)
}

// MoveDevice puts the device in a group. A groupID of 0 takes it out of every
// group.
func (d *DB) MoveDevice(id string, groupID int64) error {
	return d.affectOne(`UPDATE devices SET group_id = ? WHERE id = ?`, nullInt64(groupID), id)
}

// SetDeviceOverrides sets the per-device default playlist and screen rule.
func (d *DB) SetDeviceOverrides(id string, playlistID int64, on, off, days string) error {
	return d.affectOne(`UPDATE devices
		SET default_playlist_id = ?, screen_on = ?, screen_off = ?, screen_days = ?
		WHERE id = ?`, nullInt64(playlistID), on, off, days, id)
}

// DeleteDevice removes the device. The device token goes with the row, so the
// device must enroll again before the server answers it (plan section 12).
func (d *DB) DeleteDevice(id string) error {
	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM pending_enrollments WHERE device_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ConfirmHardware stores the hardware ID that asked to be this device. It is the
// one click of D21: the admin agrees that the card moved into another box.
func (d *DB) ConfirmHardware(id string) error {
	return d.affectOne(`UPDATE devices
		SET hardware_id = CASE WHEN pending_hardware_id <> '' THEN pending_hardware_id ELSE hardware_id END,
		    needs_confirm = 0, pending_hardware_id = '', pending_hardware_at = ''
		WHERE id = ?`, id)
}

// ResolveConflict clears the clone conflict and revokes the token (D21). The
// server cannot know which of the two boxes is the true device, so both must
// enroll again and the admin sees which one comes back.
func (d *DB) ResolveConflict(id string) error {
	return d.affectOne(`UPDATE devices
		SET conflict = 0, conflict_hardware_id = '', token_hash = '',
		    needs_confirm = 0, pending_hardware_id = '', pending_hardware_at = ''
		WHERE id = ?`, id)
}

// affectOne runs a statement that must change exactly one row.
func (d *DB) affectOne(query string, args ...any) error {
	res, err := d.w.Exec(query, args...)
	if err != nil {
		if isUniqueError(err) {
			return errors.Join(ErrDuplicate, err)
		}
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ContactStats counts the device contacts for the health page.
type ContactStats struct {
	Total     int            `json:"total"`
	LastHour  int            `json:"last_hour"`
	LastDay   int            `json:"last_day"`
	NeverSeen int            `json:"never_seen"`
	Pending   int            `json:"pending"`
	Conflict  int            `json:"conflict"`
	Versions  map[string]int `json:"versions"`
	// QuietestSeen is the oldest last_seen of the devices that did call.
	QuietestSeen time.Time `json:"quietest_seen"`
}

// Stats counts the devices for the health page and the sidebar totals.
func (d *DB) Stats(now time.Time) (ContactStats, error) {
	s := ContactStats{Versions: map[string]int{}}
	rows, err := d.r.Query(`SELECT last_seen, version, conflict FROM devices`)
	if err != nil {
		return s, err
	}
	defer rows.Close()

	hour := now.Add(-time.Hour)
	day := now.Add(-24 * time.Hour)
	for rows.Next() {
		var (
			lastSeen, version string
			conflict          int
		)
		if err := rows.Scan(&lastSeen, &version, &conflict); err != nil {
			return s, err
		}
		s.Total++
		if conflict != 0 {
			s.Conflict++
		}
		if version != "" {
			s.Versions[version]++
		}
		seen := parseTime(lastSeen)
		if seen.IsZero() {
			s.NeverSeen++
			continue
		}
		if seen.After(hour) {
			s.LastHour++
		}
		if seen.After(day) {
			s.LastDay++
		}
		if s.QuietestSeen.IsZero() || seen.Before(s.QuietestSeen) {
			s.QuietestSeen = seen
		}
	}
	if err := rows.Err(); err != nil {
		return s, err
	}
	s.Pending, err = d.CountPending()
	return s, err
}

// Totals counts the devices by state for the sidebar of the admin UI.
type Totals struct {
	Screens    int `json:"screens"`
	CheckedIn  int `json:"checked_in"`
	Quiet      int `json:"quiet"`
	NeedsALook int `json:"needs_a_look"`
}
