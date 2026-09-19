package db

import (
	"strings"
	"time"
)

// prefixLength is how much of an enrollment token the list shows. The admin sees
// the whole value one time, at creation, so the prefix is what tells two tokens
// apart later. Eight characters of a 64-character token are not enough to guess
// the rest.
const prefixLength = 8

// EnrollTokens gives every enrollment token, newest first.
func (d *DB) EnrollTokens() ([]EnrollToken, error) {
	rows, err := d.r.Query(`SELECT id, name, prefix, mode, COALESCE(group_id, 0),
		expires_at, max_uses, uses, revoked, created_at
		FROM enrollment_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []EnrollToken{}
	for rows.Next() {
		var (
			t                    EnrollToken
			revoked              int
			expiresAt, createdAt string
		)
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.Mode, &t.GroupID,
			&expiresAt, &t.MaxUses, &t.Uses, &revoked, &createdAt); err != nil {
			return nil, err
		}
		t.Revoked = revoked != 0
		t.ExpiresAt = parseTime(expiresAt)
		t.CreatedAt = parseTime(createdAt)
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateEnrollToken makes an enrollment token. It gives the row number and the
// token itself. The token is the one time that the value leaves this package: the
// table holds only its SHA-256.
//
// An expiresAt of the zero time means "no expiry". A maxUses of 0 means "no
// limit" (D25).
func (d *DB) CreateEnrollToken(name, mode string, groupID int64, expiresAt time.Time, maxUses int) (int64, string, error) {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode != "auto" && mode != "pending" {
		return 0, "", Errors{{Field: "mode", Message: "must be auto or pending"}}
	}
	if maxUses < 0 {
		return 0, "", Errors{{Field: "max_uses", Message: "must not be less than zero"}}
	}
	if !expiresAt.IsZero() && expiresAt.Before(d.now()) {
		return 0, "", Errors{{Field: "expires_at", Message: "is in the past"}}
	}
	expires := ""
	if !expiresAt.IsZero() {
		expires = d.stamp(expiresAt)
	}

	token := newToken()
	res, err := d.w.Exec(`INSERT INTO enrollment_tokens
		(name, token_hash, prefix, mode, group_id, expires_at, max_uses, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(name), hashSecret(token), token[:prefixLength], mode,
		nullInt64(groupID), expires, maxUses, d.stamp(d.now()))
	if err != nil {
		return 0, "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, "", err
	}
	return id, token, nil
}

// RevokeEnrollToken stops a token. A card that already paired keeps its own
// device token, so a revoke does not take screens off the fleet.
func (d *DB) RevokeEnrollToken(id int64) error {
	return d.affectOne(`UPDATE enrollment_tokens SET revoked = 1 WHERE id = ?`, id)
}

// DeleteEnrollToken removes the row of a token.
func (d *DB) DeleteEnrollToken(id int64) error {
	return d.affectOne(`DELETE FROM enrollment_tokens WHERE id = ?`, id)
}
