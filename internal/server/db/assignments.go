package db

import (
	"database/sql"
	"errors"
	"strings"
)

// dayNames holds the day words of a schedule rule. The device uses the same
// words (contract section 5).
var dayNames = map[string]bool{
	"mon": true, "tue": true, "wed": true, "thu": true,
	"fri": true, "sat": true, "sun": true,
}

// Assignments gives every rule of one owner. Give a group ID or a device ID, not
// both.
func (d *DB) Assignments(groupID int64, deviceID string) ([]Assignment, error) {
	var (
		rows *sql.Rows
		err  error
	)
	const query = `SELECT a.id, a.group_id, a.device_id, a.playlist_id, p.name,
			a.days, a.start, a.end, a.priority
		FROM assignments a JOIN playlists p ON p.id = a.playlist_id `
	switch {
	case deviceID != "":
		rows, err = d.r.Query(query+`WHERE a.device_id = ? ORDER BY a.priority, a.id`, deviceID)
	case groupID != 0:
		rows, err = d.r.Query(query+`WHERE a.group_id = ? ORDER BY a.priority, a.id`, groupID)
	default:
		rows, err = d.r.Query(query + `ORDER BY a.group_id, a.device_id, a.priority, a.id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Assignment{}
	for rows.Next() {
		var (
			a       Assignment
			group   sql.NullInt64
			device  sql.NullString
			dayList string
		)
		if err := rows.Scan(&a.ID, &group, &device, &a.PlaylistID, &a.PlaylistName,
			&dayList, &a.Start, &a.End, &a.Priority); err != nil {
			return nil, err
		}
		a.GroupID = intFromNull(group)
		a.DeviceID = device.String
		a.Days = splitDays(dayList)
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveAssignment makes or replaces one rule. An ID of 0 makes a new rule.
func (d *DB) SaveAssignment(a Assignment) (int64, error) {
	if errs := d.validateAssignment(a); len(errs) > 0 {
		return 0, errs
	}
	if a.ID == 0 {
		res, err := d.w.Exec(`INSERT INTO assignments
			(group_id, device_id, playlist_id, days, start, end, priority)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			nullInt64(a.GroupID), nullString(a.DeviceID), a.PlaylistID,
			joinDays(a.Days), a.Start, a.End, a.Priority)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	err := d.affectOne(`UPDATE assignments SET group_id = ?, device_id = ?, playlist_id = ?,
		days = ?, start = ?, end = ?, priority = ? WHERE id = ?`,
		nullInt64(a.GroupID), nullString(a.DeviceID), a.PlaylistID,
		joinDays(a.Days), a.Start, a.End, a.Priority, a.ID)
	return a.ID, err
}

// DeleteAssignment removes one rule.
func (d *DB) DeleteAssignment(id int64) error {
	return d.affectOne(`DELETE FROM assignments WHERE id = ?`, id)
}

// validateAssignment checks one rule.
func (d *DB) validateAssignment(a Assignment) Errors {
	var errs Errors
	switch {
	case a.GroupID != 0 && a.DeviceID != "":
		errs = append(errs, FieldError{"group_id", "a rule belongs to a group or to a device, not to both"})
	case a.GroupID == 0 && a.DeviceID == "":
		errs = append(errs, FieldError{"group_id", "a rule needs a group or a device"})
	}
	if a.PlaylistID == 0 {
		errs = append(errs, FieldError{"playlist_id", "a rule needs a playlist"})
	}
	for _, day := range a.Days {
		if !dayNames[day] {
			errs = append(errs, FieldError{"days", day + " is not one of mon, tue, wed, thu, fri, sat, sun"})
		}
	}
	if msg := badClock(a.Start); msg != "" {
		errs = append(errs, FieldError{"start", msg})
	}
	if msg := badClock(a.End); msg != "" {
		errs = append(errs, FieldError{"end", msg})
	}
	return errs
}

// badClock says why a time value is not permitted, or "" when it is good. An
// empty value is permitted: a rule with no times covers the whole day.
func badClock(v string) string {
	if v == "" {
		return ""
	}
	if len(v) != 5 || v[2] != ':' {
		return "must have the form HH:MM"
	}
	h := int(v[0]-'0')*10 + int(v[1]-'0')
	m := int(v[3]-'0')*10 + int(v[4]-'0')
	for _, i := range []int{0, 1, 3, 4} {
		if v[i] < '0' || v[i] > '9' {
			return "must have the form HH:MM"
		}
	}
	if h > 23 || m > 59 {
		return "must be a time of day between 00:00 and 23:59"
	}
	return ""
}

// ValidScreenRule checks a screen power rule. All three values empty means "no
// rule", which is always permitted.
func ValidScreenRule(on, off, days string) error {
	if on == "" && off == "" {
		if days != "" {
			return errors.New("a day list needs an on time and an off time")
		}
		return nil
	}
	if on == "" || off == "" {
		return errors.New("a screen rule needs an on time and an off time")
	}
	if msg := badClock(on); msg != "" {
		return errors.New("the on time " + msg)
	}
	if msg := badClock(off); msg != "" {
		return errors.New("the off time " + msg)
	}
	for _, day := range splitDays(days) {
		if !dayNames[day] {
			return errors.New(day + " is not one of mon, tue, wed, thu, fri, sat, sun")
		}
	}
	return nil
}

// nullString turns an empty string into NULL, so a foreign key can be absent.
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// CleanDays gives a day list in the order of the week with no repeats and no
// unknown words. The admin UI sends the days as the user clicked them.
func CleanDays(days []string) []string {
	order := []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	seen := map[string]bool{}
	for _, day := range days {
		seen[strings.ToLower(strings.TrimSpace(day))] = true
	}
	var out []string
	for _, day := range order {
		if seen[day] {
			out = append(out, day)
		}
	}
	return out
}
