package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// Heartbeat takes one device report. It is the hot path of the server: a fleet
// of many devices calls it on every poll, so it does one UPDATE of one row and
// one UPDATE for each command that the device acknowledges.
//
// It also holds the identity rules of D21:
//
//   - The hardware ID changed and the device was quiet for a while: a person
//     moved the card into another box. The row adopts the new ID and asks the
//     admin for one click of confirmation.
//   - The hardware ID changed and the device called from the old ID a moment
//     ago: two boxes run from one card. The row gets the conflict flag. The
//     server never resolves a conflict by itself, because it cannot know which
//     box is the true screen.
func (d *DB) Heartbeat(id string, hb manifest.Heartbeat, ip string) error {
	now := d.now()

	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var oldHardware, lastSeen string
	err = tx.QueryRow(`SELECT hardware_id, last_seen FROM devices WHERE id = ?`, id).
		Scan(&oldHardware, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	status := ""
	if data, err := json.Marshal(hb.Status); err == nil {
		status = string(data)
	}

	// The one cheap UPDATE of the hot path.
	if _, err := tx.Exec(`UPDATE devices
		SET last_seen = ?, status_json = ?, version = ?, sync_error = ?, last_ip = ?
		WHERE id = ?`,
		d.stamp(now), status, hb.Version, hb.SyncError, ip, id); err != nil {
		return err
	}

	if hb.HardwareID != "" && oldHardware != "" && hb.HardwareID != oldHardware {
		if err := d.hardwareChanged(tx, id, oldHardware, hb.HardwareID, parseTime(lastSeen), now); err != nil {
			return err
		}
	} else if hb.HardwareID != "" && oldHardware == "" {
		if _, err := tx.Exec(`UPDATE devices SET hardware_id = ? WHERE id = ?`, hb.HardwareID, id); err != nil {
			return err
		}
	}

	for _, cmdID := range hb.Acks {
		// The device ID is in the statement, so a device can only acknowledge a
		// command of its own row.
		if _, err := tx.Exec(`UPDATE commands SET acked_at = ?
			WHERE id = ? AND device_id = ? AND acked_at = ''`,
			d.stamp(now), cmdID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// hardwareChanged writes the repair case or the clone case.
func (d *DB) hardwareChanged(tx *sql.Tx, id, oldHardware, newHardware string, lastSeen, now time.Time) error {
	if !lastSeen.IsZero() && now.Sub(lastSeen) < cloneWindow {
		// Two hardware IDs inside the window: a clone. Keep the hardware ID that
		// the row already had, so the admin sees which ID is the new one.
		_, err := tx.Exec(`UPDATE devices SET conflict = 1, conflict_hardware_id = ? WHERE id = ?`,
			newHardware, id)
		return err
	}
	_, err := tx.Exec(`UPDATE devices SET hardware_id = ?, prev_hardware_id = ?, needs_confirm = 1
		WHERE id = ?`, newHardware, oldHardware, id)
	return err
}

// TouchSeen writes only the last contact time. The manifest route calls it, so
// that a device which polls but sends no heartbeat still counts as online.
func (d *DB) TouchSeen(id, ip string) error {
	_, err := d.w.Exec(`UPDATE devices SET last_seen = ?, last_ip = ? WHERE id = ?`,
		d.stamp(d.now()), ip, id)
	return err
}
