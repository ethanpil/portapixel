package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// maxAcks is the largest number of command acknowledgements that one heartbeat
// may carry. A device has a few commands in flight, so a list of a hundred is
// already more than any real screen sends.
const maxAcks = 100

// Heartbeat takes one device report. It is the hot path of the server: a fleet of
// many devices calls it on every poll, so it does one UPDATE of one row and one
// UPDATE for the commands that the device acknowledges.
//
// It also holds the identity rules of D21. The rules do not use last_seen,
// because the manifest route and the enroll route both move last_seen: a device
// that polls and then reports would always look like a clone.
//
//   - A hardware ID that is not the stored one asks to be this device. The row
//     takes needs_confirm and records the value in pending_hardware_id. The
//     stored hardware ID does not move. That is the repair: a person put the card
//     in another box, and one click of the admin agrees with it.
//   - A conflict needs the two IDs to take turns. While one ID waits for
//     confirmation, a request with the stored ID, or with a third ID, inside
//     cloneWindow is two boxes that run from one card. Then the row takes the
//     conflict flag, and the server never resolves it by itself: it cannot know
//     which box is the true screen.
//
// One machine that changed is never a conflict, however soon it answers. Only the
// two of them in turn is one.
func (d *DB) Heartbeat(id string, hb manifest.Heartbeat, ip string) error {
	if len(hb.Acks) > maxAcks {
		return Errors{{Field: "acks", Message: fmt.Sprintf("a heartbeat may acknowledge %d commands at most", maxAcks)}}
	}
	now := d.now()

	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var (
		stored, pendingHW, pendingAt string
		needsConfirm                 int
	)
	err = tx.QueryRow(`SELECT hardware_id, pending_hardware_id, pending_hardware_at, needs_confirm
		FROM devices WHERE id = ?`, id).Scan(&stored, &pendingHW, &pendingAt, &needsConfirm)
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

	if err := d.hardwareRules(tx, id, hb.HardwareID, stored, pendingHW, parseTime(pendingAt), now); err != nil {
		return err
	}

	if len(hb.Acks) > 0 {
		// One statement with the IDs in an IN list. The device ID is in the
		// statement as well, so a device can only acknowledge a command of its own
		// row.
		args := make([]any, 0, len(hb.Acks)+2)
		args = append(args, d.stamp(now))
		marks := make([]string, len(hb.Acks))
		for i, cmdID := range hb.Acks {
			marks[i] = "?"
			args = append(args, cmdID)
		}
		args = append(args, id)
		query := `UPDATE commands SET acked_at = ? WHERE id IN (` +
			strings.Join(marks, ",") + `) AND device_id = ? AND acked_at = ''`
		if _, err := tx.Exec(query, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// hardwareRules writes the repair case and the clone case of D21. See Heartbeat.
func (d *DB) hardwareRules(tx *tx, id, reported, stored, pendingHW string, pendingAt, now time.Time) error {
	if reported == "" {
		return nil
	}
	stamp := d.stamp(now)

	if stored == "" {
		// The first hardware ID of this row. There is nothing to compare with.
		_, err := tx.Exec(`UPDATE devices SET hardware_id = ? WHERE id = ?`, reported, id)
		return err
	}

	// One ID waits for confirmation and another one answers: the two take turns,
	// so two machines hold one token.
	if pendingHW != "" && pendingHW != reported &&
		!pendingAt.IsZero() && now.Sub(pendingAt) < cloneWindow {
		conflictWith := pendingHW
		if reported != stored {
			// A third machine. Name the one that just arrived: it is the news.
			conflictWith = reported
		}
		_, err := tx.Exec(`UPDATE devices SET conflict = 1, conflict_hardware_id = ? WHERE id = ?`,
			conflictWith, id)
		return err
	}

	if reported == stored {
		// The machine that we know, and nothing waits. There is nothing to write.
		return nil
	}

	// The repair. The stored ID does not move until the admin confirms.
	_, err := tx.Exec(`UPDATE devices SET needs_confirm = 1,
		pending_hardware_id = ?, pending_hardware_at = ? WHERE id = ?`, reported, stamp, id)
	return err
}

// TouchSeen writes only the last contact time. The manifest route calls it, so
// that a device which polls but sends no heartbeat still counts as online. It
// takes no part in the identity rules of D21: the manifest route knows the device
// by its token and not by its hardware ID.
func (d *DB) TouchSeen(id, ip string) error {
	_, err := d.w.Exec(`UPDATE devices SET last_seen = ?, last_ip = ? WHERE id = ?`,
		d.stamp(d.now()), ip, id)
	return err
}
