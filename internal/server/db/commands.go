package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// nameRule is the rule of a display name in words, for a field error.
var nameRule = fmt.Sprintf("a screen needs a name of 1 to %d characters with no control character",
	manifest.MaxNameLength)

// commandTypes holds the commands that a device runs (contract section 5). The
// server refuses every other word: the device would not know it, and a word that
// goes into the queue and never runs is worse than an error at the button.
var commandTypes = []string{"reboot", "restart-browser", "screen-on", "screen-off", "rescan", "update", CommandRename}

// CommandRename gives a screen a new display name. The device owns its name, so
// the server does not write the row: the next heartbeat reports the new name, and
// Heartbeat writes it. Its one argument is "name".
const CommandRename = "rename"

// validCommand reports if name is a command that a device knows.
func validCommand(name string) bool {
	for _, t := range commandTypes {
		if t == name {
			return true
		}
	}
	return false
}

// redeliverAfter is how long a command waits for an acknowledgement before it
// goes out again. A device polls every minute or so, and a reboot takes it off
// the network for a while, so ten minutes covers a real restart.
const redeliverAfter = 10 * time.Minute

// maxDeliveries is how often one command goes out. After the last try the command
// is expired, and the admin UI says so. A queue entry that goes out for ever
// would reboot a screen every ten minutes after one lost answer.
const maxDeliveries = 3

// commandLife is how long a command waits for its screen. A screen that is off for
// a day and comes back must not run a stack of orders from yesterday: a reboot that
// arrives a day late is a screen that reboots for no reason a person can follow.
const commandLife = 24 * time.Hour

// commandKeep is how long an answered or expired row stays for the admin to read.
// Nothing removed these rows before, so the table only grew.
const commandKeep = 30 * 24 * time.Hour

// The states of a command that the admin UI shows.
const (
	CommandQueued    = "queued"
	CommandDelivered = "delivered"
	CommandAcked     = "acked"
	CommandExpired   = "expired"
)

// QueueCommand puts one command in the queue of one device.
func (d *DB) QueueCommand(deviceID, kind string, args map[string]string) (int64, error) {
	if !validCommand(kind) {
		return 0, Errors{{Field: "type", Message: "must be one of " + strings.Join(commandTypes, ", ")}}
	}
	if kind == CommandRename {
		name, ok := manifest.CleanName(args["name"])
		if !ok {
			return 0, Errors{{Field: "name", Message: nameRule}}
		}
		// The clean value goes out, and nothing else: the device reads only "name".
		args = map[string]string{"name": name}
	}
	argsJSON, err := encodeArgs(args)
	if err != nil {
		return 0, err
	}
	res, err := d.w.Exec(`INSERT INTO commands (device_id, type, args_json, queued_at)
		VALUES (?, ?, ?, ?)`, deviceID, kind, argsJSON, d.stamp(d.now()))
	if err != nil {
		// A foreign key violation means that the device is not there. Every other
		// failure is a fault of the server, and the route must not report it as a
		// missing record: a busy database is not a screen that went away.
		if isForeignKeyError(err) {
			return 0, errors.Join(ErrNotFound, err)
		}
		return 0, err
	}
	return res.LastInsertId()
}

// QueueGroupCommand puts one command in the queue of every device of a group. It
// gives the number of devices that got it.
func (d *DB) QueueGroupCommand(groupID int64, kind string, args map[string]string) (int, error) {
	if !validCommand(kind) {
		return 0, Errors{{Field: "type", Message: "must be one of " + strings.Join(commandTypes, ", ")}}
	}
	// One name for each screen of a group gives many screens one name, and mDNS
	// then has two answers for one name.
	if kind == CommandRename {
		return 0, Errors{{Field: "type", Message: "a group cannot take rename: each screen has a name of its own"}}
	}
	argsJSON, err := encodeArgs(args)
	if err != nil {
		return 0, err
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

// encodeArgs turns the argument map into the JSON that the row holds.
func encodeArgs(args map[string]string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	data, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// TakeCommands gives the commands that this device must run and counts them as
// delivered. The manifest route calls it, so a command reaches a device on its
// next poll (D24).
//
// The select and the update are one write transaction. Two things depend on that:
// a command that an admin queues while the select runs must not be marked as
// delivered, and a device that polls twice at once must not take one command
// twice.
//
// A command that went out and that the device never acknowledged goes out again
// after redeliverAfter, up to maxDeliveries times. After the last try it is
// expired. A reboot that a screen missed is worth one more try; a reboot that
// goes out for ever is a screen that never comes up.
func (d *DB) TakeCommands(deviceID string) ([]Command, error) {
	now := d.now()
	stamp := d.stamp(now)
	retryCut := d.stamp(now.Add(-redeliverAfter))

	tx, err := d.w.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// A command that used up its tries and that nobody acknowledged is expired, and
	// so is a command that waited longer than commandLife. The second half also holds
	// for a command that never went out at all.
	if _, err := tx.Exec(`UPDATE commands SET expired = 1
		WHERE device_id = ? AND acked_at = '' AND expired = 0
		  AND ((deliveries >= ? AND delivered_at <> '' AND delivered_at < ?)
		       OR queued_at < ?)`,
		deviceID, maxDeliveries, retryCut, d.stamp(now.Add(-commandLife))); err != nil {
		return nil, err
	}

	rows, err := tx.Query(`SELECT id, type, args_json FROM commands
		WHERE device_id = ? AND acked_at = '' AND expired = 0
		  AND (delivered_at = '' OR (deliveries < ? AND delivered_at < ?))
		ORDER BY id`, deviceID, maxDeliveries, retryCut)
	if err != nil {
		return nil, err
	}
	var (
		out []Command
		ids []int64
	)
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
		ids = append(ids, c.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, tx.Commit()
	}

	// Only the rows that went out in this answer are marked. A command that
	// arrived between the select and here keeps its place in the queue.
	args := make([]any, 0, len(ids)+2)
	args = append(args, stamp)
	marks := make([]string, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args = append(args, id)
	}
	args = append(args, deviceID)
	query := `UPDATE commands SET delivered_at = ?, deliveries = deliveries + 1
		WHERE id IN (` + strings.Join(marks, ",") + `) AND device_id = ?`
	if _, err := tx.Exec(query, args...); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// SweepCommands answers the clock for the whole fleet and takes the old rows away.
// A timer of the server calls it.
//
// TakeCommands can only expire the rows of the device that polls. A screen that
// never comes back therefore left its commands at "delivered" for ever, and the
// admin UI never showed the "expired" state that the contract names. Nothing at all
// removed an answered row, so the table only grew.
func (d *DB) SweepCommands() error {
	now := d.now()
	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE commands SET expired = 1
		WHERE acked_at = '' AND expired = 0
		  AND ((deliveries >= ? AND delivered_at <> '' AND delivered_at < ?)
		       OR queued_at < ?)`,
		maxDeliveries, d.stamp(now.Add(-redeliverAfter)),
		d.stamp(now.Add(-commandLife))); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM commands
		WHERE (acked_at <> '' OR expired = 1) AND queued_at < ?`,
		d.stamp(now.Add(-commandKeep))); err != nil {
		return err
	}
	return tx.Commit()
}

// Commands gives the newest commands of one device with their state.
func (d *DB) Commands(deviceID string, limit int) ([]Command, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.r.Query(`SELECT id, type, args_json, queued_at, delivered_at, acked_at,
			deliveries, expired
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
			deliveries, expired                int
		)
		if err := rows.Scan(&c.ID, &c.Type, &argsJSON, &queued, &delivered, &acked,
			&deliveries, &expired); err != nil {
			return nil, err
		}
		if argsJSON != "" {
			json.Unmarshal([]byte(argsJSON), &c.Args)
		}
		c.DeviceID = deviceID
		c.QueuedAt = parseTime(queued)
		c.DeliveredAt = parseTime(delivered)
		c.AckedAt = parseTime(acked)
		c.Deliveries = deliveries
		switch {
		case acked != "":
			c.State = CommandAcked
		case expired != 0:
			c.State = CommandExpired
		case delivered != "":
			c.State = CommandDelivered
		default:
			c.State = CommandQueued
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// isForeignKeyError reports if err is a foreign key violation of SQLite.
//
// It reads the result code and not the text of the message. The text of a driver
// changes between versions, and a check on it turns a busy database into "there
// is no such record" the day that the wording moves.
func isForeignKeyError(err error) bool {
	return sqliteCode(err) == sqliteConstraintForeignKey
}

// isUniqueError reports if err is a unique constraint violation of SQLite.
func isUniqueError(err error) bool {
	switch sqliteCode(err) {
	case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
		return true
	}
	return false
}

// The extended result codes of SQLite that we act on.
const (
	sqliteConstraintUnique     = 2067
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintForeignKey = 787
)

// sqliteCode gives the extended result code of an error of the driver, or 0.
//
// modernc.org/sqlite gives an error type with a Code method. The interface is
// here and not the concrete type, so this file needs no import of the driver
// package and a driver change needs no edit.
func sqliteCode(err error) int {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code()
	}
	return 0
}
