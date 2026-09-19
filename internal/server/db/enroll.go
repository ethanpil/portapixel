package db

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// ErrBadToken says that the token of an enroll request is not one that this
// server gave out, or that it is used up, expired or revoked. The route answers
// 401 and says nothing more: a device that guesses tokens must learn nothing
// about which of the reasons applies.
var ErrBadToken = errors.New("this token is not valid")

// ErrBadDeviceID says that the device ID has the wrong shape.
var ErrBadDeviceID = errors.New("the device ID must be 1 to 64 characters of a-z, 0-9 and the hyphen")

// codeAlphabet holds the characters of a pairing code. It leaves out 0, O, 1 and
// I, because a person reads the code off a screen and types it on a keyboard
// (D25).
const codeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// codeLength is the length of a pairing code.
const codeLength = 6

// cloneWindow is how close together two hardware IDs must use one token before
// the server calls it a clone (D21). A repair takes a person and a screwdriver,
// so the device is away for longer than this. Two boxes that both run from one
// flashed card answer inside it.
const cloneWindow = 10 * time.Minute

// HashToken gives the SHA-256 of a token as lower case hex. The tables hold this
// value and never the token itself.
//
// There is no salt and no password hash here. A token is 32 random bytes from
// crypto/rand, so a dictionary attack against the stored value has nothing to
// work with, and the enroll path has to look a token up by its value.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// equalHash compares two hex hashes in constant time.
func equalHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// NewToken makes a token of 32 random bytes as hex. Device tokens and
// enrollment tokens both use it.
func NewToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any system that we run on. A token of
		// zeros would be a token that anybody can guess, so stop instead.
		panic("db: the system gave no random bytes: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// newPairingCode makes a 6-character pairing code.
func newPairingCode() string {
	var b [codeLength]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("db: the system gave no random bytes: " + err.Error())
	}
	out := make([]byte, codeLength)
	for i, v := range b {
		out[i] = codeAlphabet[int(v)%len(codeAlphabet)]
	}
	return string(out)
}

// ValidDeviceID reports if the device ID has a shape that we accept. The value
// is the primary key of a table and part of a URL path, so it takes letters,
// digits and the hyphen and nothing else.
func ValidDeviceID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// SetClock replaces the clock of the database. Only a test calls it: a test of
// the quiet state or of the clone window cannot wait for real minutes.
func (d *DB) SetClock(f func() time.Time) { d.now = f }

// Enroll answers one enroll request. It is the whole pairing rule of the server
// (D25). The three flows all end in one row of the devices table:
//
//   - The token is an enrollment token. The mode of that token says if the
//     device gets a token now or waits for the admin.
//   - The token is a device token that this server gave out. The device pairs
//     again, with the token that it already holds.
//   - The token is the claim secret of a device that waits. The answer says
//     "still waiting", or it gives the token when the admin approved the device.
//   - The token is empty. The device asks to pair by code.
func (d *DB) Enroll(req manifest.EnrollRequest, ip string) (manifest.EnrollResponse, error) {
	if !ValidDeviceID(req.DeviceID) {
		return manifest.EnrollResponse{}, ErrBadDeviceID
	}
	now := d.now()

	tx, err := d.w.Begin()
	if err != nil {
		return manifest.EnrollResponse{}, err
	}
	defer tx.Rollback()

	var out manifest.EnrollResponse
	switch {
	case req.Token == "":
		out, err = d.enrollByCode(tx, req, ip, now)
	default:
		out, err = d.enrollWithToken(tx, req, ip, now)
	}
	if err != nil {
		return manifest.EnrollResponse{}, err
	}
	if err := tx.Commit(); err != nil {
		return manifest.EnrollResponse{}, err
	}
	return out, nil
}

// enrollWithToken handles a request that carries a token of one of the three
// kinds.
func (d *DB) enrollWithToken(tx *sql.Tx, req manifest.EnrollRequest, ip string, now time.Time) (manifest.EnrollResponse, error) {
	hash := HashToken(req.Token)

	// A device token. The device pairs again and keeps the token that it holds.
	var id string
	err := tx.QueryRow(`SELECT id FROM devices WHERE token_hash = ? AND token_hash <> ''`, hash).Scan(&id)
	if err == nil {
		if id != req.DeviceID {
			// The token belongs to another row. A device must never be able to
			// take over the row of a second device with its own ID.
			return manifest.EnrollResponse{}, ErrBadToken
		}
		if err := d.touchEnrolled(tx, req, ip, now); err != nil {
			return manifest.EnrollResponse{}, err
		}
		return manifest.EnrollResponse{Status: "paired", DeviceToken: req.Token}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return manifest.EnrollResponse{}, err
	}

	// A claim secret. The device asks if the admin approved it yet.
	var pending int
	err = tx.QueryRow(`SELECT id, pending FROM devices
		WHERE claim_secret = ? AND claim_secret <> ''`, req.Token).Scan(&id, &pending)
	if err == nil {
		if id != req.DeviceID {
			return manifest.EnrollResponse{}, ErrBadToken
		}
		return d.answerClaim(tx, req, ip, now, pending != 0)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return manifest.EnrollResponse{}, err
	}

	// An enrollment token.
	return d.enrollWithEnrollmentToken(tx, req, ip, now, hash)
}

// answerClaim answers a device that polls with its claim secret.
func (d *DB) answerClaim(tx *sql.Tx, req manifest.EnrollRequest, ip string, now time.Time, stillPending bool) (manifest.EnrollResponse, error) {
	if stillPending {
		var code string
		if err := tx.QueryRow(`SELECT pending_code FROM devices WHERE id = ?`, req.DeviceID).Scan(&code); err != nil {
			return manifest.EnrollResponse{}, err
		}
		if _, err := tx.Exec(`UPDATE devices SET last_ip = ?, version = ? WHERE id = ?`,
			ip, req.Version, req.DeviceID); err != nil {
			return manifest.EnrollResponse{}, err
		}
		return manifest.EnrollResponse{Status: "pending", PairingCode: code, ClaimSecret: req.Token}, nil
	}

	// The admin approved it. Make the device token now.
	//
	// The token is made at this moment and not at the moment of approval, so the
	// server never holds a device token in plain form, not even for the minute
	// between an approval and the next poll of the device.
	token := NewToken()
	if err := d.pairRow(tx, req, ip, now, HashToken(token)); err != nil {
		return manifest.EnrollResponse{}, err
	}
	return manifest.EnrollResponse{Status: "paired", DeviceToken: token}, nil
}

// enrollWithEnrollmentToken handles a fleet-wide enrollment token. One token
// pairs any number of cards (D25).
func (d *DB) enrollWithEnrollmentToken(tx *sql.Tx, req manifest.EnrollRequest, ip string, now time.Time, hash string) (manifest.EnrollResponse, error) {
	var (
		tokenID int64
		mode    string
		groupID sql.NullInt64
	)
	err := tx.QueryRow(`SELECT id, mode, group_id FROM enrollment_tokens WHERE token_hash = ?`, hash).
		Scan(&tokenID, &mode, &groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return manifest.EnrollResponse{}, ErrBadToken
	}
	if err != nil {
		return manifest.EnrollResponse{}, err
	}

	// Count the use and check the limits in one statement. Two devices that boot
	// together cannot both take the last use of a token this way.
	res, err := tx.Exec(`UPDATE enrollment_tokens SET uses = uses + 1
		WHERE id = ? AND revoked = 0
		  AND (max_uses = 0 OR uses < max_uses)
		  AND (expires_at = '' OR expires_at > ?)`, tokenID, d.stamp(now))
	if err != nil {
		return manifest.EnrollResponse{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return manifest.EnrollResponse{}, err
	} else if n == 0 {
		return manifest.EnrollResponse{}, ErrBadToken
	}

	if mode == "auto" {
		token := NewToken()
		if err := d.upsertDevice(tx, req, ip, now, intFromNull(groupID)); err != nil {
			return manifest.EnrollResponse{}, err
		}
		if err := d.pairRow(tx, req, ip, now, HashToken(token)); err != nil {
			return manifest.EnrollResponse{}, err
		}
		return manifest.EnrollResponse{Status: "paired", DeviceToken: token}, nil
	}

	// Mode "pending": the device waits in the list with a code.
	if err := d.upsertDevice(tx, req, ip, now, intFromNull(groupID)); err != nil {
		return manifest.EnrollResponse{}, err
	}
	return d.makePending(tx, req, now)
}

// enrollByCode handles a request with no token at all: the device shows a code
// on its fallback screen and the admin approves it.
func (d *DB) enrollByCode(tx *sql.Tx, req manifest.EnrollRequest, ip string, now time.Time) (manifest.EnrollResponse, error) {
	if err := d.upsertDevice(tx, req, ip, now, 0); err != nil {
		return manifest.EnrollResponse{}, err
	}
	// A device that already waits keeps its code and its claim secret, so that
	// the code on the screen does not change at every poll.
	var code, secret string
	var pending int
	err := tx.QueryRow(`SELECT pending, pending_code, claim_secret FROM devices WHERE id = ?`, req.DeviceID).
		Scan(&pending, &code, &secret)
	if err != nil {
		return manifest.EnrollResponse{}, err
	}
	if pending != 0 && code != "" && secret != "" {
		return manifest.EnrollResponse{Status: "pending", PairingCode: code, ClaimSecret: secret}, nil
	}
	return d.makePending(tx, req, now)
}

// makePending puts the device in the pending list with a new code and a new
// claim secret. It does not touch the token of the row: a device that lost its
// state and asks to pair again must keep playing while it waits.
func (d *DB) makePending(tx *sql.Tx, req manifest.EnrollRequest, now time.Time) (manifest.EnrollResponse, error) {
	code, err := d.freeCode(tx)
	if err != nil {
		return manifest.EnrollResponse{}, err
	}
	secret := NewToken()
	if _, err := tx.Exec(`UPDATE devices
		SET pending = 1, pending_code = ?, claim_secret = ? WHERE id = ?`,
		code, secret, req.DeviceID); err != nil {
		return manifest.EnrollResponse{}, err
	}
	return manifest.EnrollResponse{Status: "pending", PairingCode: code, ClaimSecret: secret}, nil
}

// freeCode makes a pairing code that no other pending device holds. Two devices
// that show one code would be two devices that the admin cannot tell apart.
func (d *DB) freeCode(tx *sql.Tx) (string, error) {
	for try := 0; try < 20; try++ {
		code := newPairingCode()
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM devices WHERE pending_code = ?`, code).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return code, nil
		}
	}
	// The alphabet gives 32^6 codes. Twenty collisions in a row means that the
	// random source is broken, which is not a case to paper over.
	return "", errors.New("could not make a free pairing code")
}

// upsertDevice makes the row of a device or updates the values that the device
// reports. It never touches the token, the group of a device that is already
// there, or the pending state.
func (d *DB) upsertDevice(tx *sql.Tx, req manifest.EnrollRequest, ip string, now time.Time, groupID int64) error {
	name := strings.TrimSpace(req.Name)
	stamp := d.stamp(now)
	res, err := tx.Exec(`UPDATE devices SET hardware_id = ?, version = ?, last_ip = ?,
		name = CASE WHEN name = '' THEN ? ELSE name END WHERE id = ?`,
		req.HardwareID, req.Version, ip, name, req.DeviceID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err = tx.Exec(`INSERT INTO devices (id, name, group_id, hardware_id, version, last_ip, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		req.DeviceID, name, nullInt64(groupID), req.HardwareID, req.Version, ip, stamp)
	return err
}

// pairRow marks the row as paired with this token hash. It clears the pending
// state and the claim secret: the device holds a token now.
func (d *DB) pairRow(tx *sql.Tx, req manifest.EnrollRequest, ip string, now time.Time, tokenHash string) error {
	if err := d.touchEnrolled(tx, req, ip, now); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE devices SET token_hash = ?, claim_secret = '',
		pending = 0, pending_code = '', paired_at = ? WHERE id = ?`,
		tokenHash, d.stamp(now), req.DeviceID)
	return err
}

// touchEnrolled writes the values that an enroll request reports.
func (d *DB) touchEnrolled(tx *sql.Tx, req manifest.EnrollRequest, ip string, now time.Time) error {
	_, err := tx.Exec(`UPDATE devices SET hardware_id = ?, version = ?, last_ip = ?, last_seen = ?
		WHERE id = ?`, req.HardwareID, req.Version, ip, d.stamp(now), req.DeviceID)
	return err
}

// Approve lets a pending device in. The device gets its token at its next poll.
// A groupID of 0 leaves the group as it is.
func (d *DB) Approve(id string, groupID int64) error {
	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var pending int
	var secret string
	err = tx.QueryRow(`SELECT pending, claim_secret FROM devices WHERE id = ?`, id).Scan(&pending, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if pending == 0 {
		return fmt.Errorf("%s does not wait for approval", id)
	}
	if secret == "" {
		// The row has no claim secret, so the device has no way to come back for
		// its token. That is a fault of the server, not of the request.
		return errors.New("this device has no claim secret; it must enroll again")
	}
	if _, err := tx.Exec(`UPDATE devices SET pending = 0 WHERE id = ?`, id); err != nil {
		return err
	}
	if groupID != 0 {
		if _, err := tx.Exec(`UPDATE devices SET group_id = ? WHERE id = ?`, groupID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Reject turns a pending device away. A device that never paired goes away with
// its row. A device that paired before keeps the row and its token: the admin
// said "this request is not one of mine", not "delete the screen".
func (d *DB) Reject(id string) error {
	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var pairedAt string
	err = tx.QueryRow(`SELECT paired_at FROM devices WHERE id = ? AND pending = 1`, id).Scan(&pairedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if pairedAt == "" {
		if _, err := tx.Exec(`DELETE FROM devices WHERE id = ?`, id); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`UPDATE devices
		SET pending = 0, pending_code = '', claim_secret = '' WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// DeviceByCode gives the pending device that shows this code. The admin UI
// approves by code, because the code is what a person can read off the screen.
func (d *DB) DeviceByCode(code string) (Device, error) {
	var id string
	err := d.r.QueryRow(`SELECT id FROM devices WHERE pending_code = ? AND pending = 1`,
		strings.ToUpper(strings.TrimSpace(code))).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, err
	}
	return d.Device(id)
}
