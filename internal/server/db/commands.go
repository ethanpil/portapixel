package db

import (
	"encoding/json"
	"errors"
	"strings"
)

// CommandTypes holds the commands that a device runs (contract section 5). The
// server refuses every other word: the device would not know it, and a word that
// goes into the queue and never runs is worse than an error at the button.
var CommandTypes = []string{"reboot", "restart-browser", "screen-on", "screen-off", "rescan", "update"}

// ValidCommand reports if name is a command that a device knows.
func ValidCommand(name string) bool {
	for _, t := range CommandTypes {
		if t == name {
			return true
		}
	}
	return false
}

// QueueCommand puts one command in the queue of one device.
func (d *DB) QueueCommand(deviceID, kind string, args map[string]string) (int64, error) {
	if !ValidCommand(kind) {
		return 0, Errors{{Field: "type", Message: "must be one of " + strings.Join(CommandTypes, ", ")}}
	}
	argsJSON := ""
	if len(args) > 0 {
		data, err := json.Marshal(args)
		if err != nil {
			return 0, err
		}
		argsJSON = string(data)
	}
	res, err := d.w.Exec(`INSERT INTO commands (device_id, type, args_json, queued_at)
		VALUES (?, ?, ?, ?)`, deviceID, kind, argsJSON, d.stamp(d.now()))
	if err != nil {
		// The foreign key refuses a device that is not there.
		return 0, errors.Join(ErrNotFound, err)
	}
	return res.LastInsertId()
}

// QueueGroupCommand puts one command in the queue of every device of a group. It
// gives the number of devices that got it.
func (d *DB) QueueGroupCommand(groupID int64, kind string, args map[string]string) (int, error) {
	if !ValidCommand(kind) {
		return 0, Errors{{Field: "type", Message: "must be one of " + strings.Join(CommandTypes, ", ")}}
	}
	argsJSON := ""
	if len(args) > 0 {
		data, err := json.Marshal(args)
		if err != nil {
			return 0, err
		}
		argsJSON = string(data)
	}
	res, err := d.w.Exec(`INSERT INTO commands (device_id, type, args_json, queued_at)
		SELECT id, ?, ?, ? FROM devices WHERE group_id = ?`,
		kind, argsJSON, d.stamp(d.now()), groupID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// TakeCommands gives the commands that this device has not seen yet and marks
// them as delivered. The manifest route calls it, so a command reaches a device
// on its next poll (D24).
func (d *DB) TakeCommands(deviceID string) ([]Command, error) {
	rows, err := d.r.Query(`SELECT id, type, args_json FROM commands
		WHERE device_id = ? AND delivered_at = '' ORDER BY id`, deviceID)
	if err != nil {
		return nil, err
	}
	var out []Command
	for rows.Next() {
		var (
			c        Command
			argsJSON string
		)
		if err := rows.Scan(&c.ID, &c.Type, &argsJSON); err != nil {
			rows.Close()
			return nil, err
		}
		if argsJSON != "" {
			json.Unmarshal([]byte(argsJSON), &c.Args)
		}
		c.DeviceID = deviceID
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	// One statement marks them all. The device ID is in the statement, so this
	// can never mark a command of another row.
	if _, err := d.w.Exec(`UPDATE commands SET delivered_at = ?
		WHERE device_id = ? AND delivered_at = ''`, d.stamp(d.now()), deviceID); err != nil {
		return nil, err
	}
	return out, nil
}

// Commands gives the newest commands of one device for the admin UI.
func (d *DB) Commands(deviceID string, limit int) ([]Command, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.r.Query(`SELECT id, type, args_json, queued_at, delivered_at, acked_at
		FROM commands WHERE device_id = ? ORDER BY id DESC LIMIT ?`, deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Command{}
	for rows.Next() {
		var (
			c                                  Command
			argsJSON, queued, delivered, acked string
		)
		if err := rows.Scan(&c.ID, &c.Type, &argsJSON, &queued, &delivered, &acked); err != nil {
			return nil, err
		}
		if argsJSON != "" {
			json.Unmarshal([]byte(argsJSON), &c.Args)
		}
		c.DeviceID = deviceID
		c.QueuedAt = parseTime(queued)
		c.DeliveredAt = parseTime(delivered)
		c.AckedAt = parseTime(acked)
		switch {
		case acked != "":
			c.State = "acked"
		case delivered != "":
			c.State = "delivered"
		default:
			c.State = "queued"
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
