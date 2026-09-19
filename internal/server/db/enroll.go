package db

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
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

// ErrTooManyPending says that the pending list is full. It stops an
// unauthenticated caller from filling the database with requests (R5).
var ErrTooManyPending = errors.New("too many screens wait for approval")

// ErrNotPending says that a row does not wait for approval. A second click on
// the approve button lands here, so the route answers 409 and not 500.
var ErrNotPending = errors.New("this request does not wait for approval")

// codeAlphabet holds the characters of a pairing code. It leaves out 0, O, 1 and
// I, because a person reads the code off a screen and types it on a keyboard
// (D25).
const codeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// codeLength is the length of a pairing code.
const codeLength = 6

// pendingLife is how long a request waits for the admin. After it the row goes
// away, and a screen that still asks gets a new code. Without an expiry the
// pending list is a table that only grows, and an open route fills it.
const pendingLife = 24 * time.Hour

// maxPending is the largest number of requests that may wait at one time. A
// homelab fleet is a few hundred screens, so two hundred requests are already
// more than one batch of cards.
const maxPending = 200

// cloneWindow is how close together two hardware IDs must take turns on one
// token before the server calls it a clone (D21). A repair takes a person and a
// screwdriver, so the old box does not answer again after it. Two boxes that both
// run from one flashed card answer inside it.
const cloneWindow = 10 * time.Minute

// hashSecret gives the SHA-256 of a secret as lower case hex. The tables hold
// this value and never the secret itself.
//
// There is no salt and no password hash here. A secret is 32 random bytes from
// crypto/rand, so a dictionary attack against the stored value has nothing to
// work with, and the enroll path has to look a secret up by its value.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// equalHash compares two hex hashes in constant time.
func equalHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// newToken makes a secret of 32 random bytes as hex. Device tokens, enrollment
// tokens and claim secrets all use it.
func newToken() string {
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

// validDeviceID reports if the device ID has a shape that we accept. The value
// is the primary key of a table and part of a URL path, so it takes letters,
// digits and the hyphen and nothing else.
func validDeviceID(id string) bool {
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

// EnrollResult is what an enroll request did. The route reads Created to decide
// if the attempt counts against the rate limit of its address (R5).
type EnrollResult struct {
	manifest.EnrollResponse
	// Created is true when this request made a new row in the pending list. A
	// poll of a request that already waits does not count.
	Created bool
}

// Enroll answers one enroll request.
//
// These are the whole pairing rules of the server (D25). They are here and not in
// a route, because each of them is a rule about rows.
//
// R1. An enroll request never changes a devices row that was ever paired, except
// through R2. A request that waits lives in pending_enrollments, and the devices
// table changes at approval only. So a screen on the wall keeps its token, its
// playlists and its group while somebody else asks to be that screen.
//
// R2. A known device pairs again when the request carries a valid enrollment
// token in auto mode, the device ID names a paired row, and the hardware ID of
// the request is the hardware ID of that row. That is the reflashed card: the
// card lost its token with the ext4 state, and the box is the same box. The
// hardware ID is the full SHA-256 of the hardware source, and a device ID shows
// only its first 8 hex characters, so knowing a device ID does not give the
// hardware ID.
//
// R3. A device ID with no row pairs at once with an auto token. With a
// pending-mode token, and with no token at all, it waits in the pending list. A
// device ID that names a paired row and that does not pass R2 also waits, with
// collides_with set, and the row of the screen is untouched.
//
// R4. A claim secret is stored as its SHA-256 and compared in constant time. A
// request that waits longer than pendingLife goes away.
//
// R5. Every request that makes a pending row counts against the limit of its
// address. A poll with a good claim secret does not.
func (d *DB) Enroll(req manifest.EnrollRequest, ip string) (EnrollResult, error) {
	if !validDeviceID(req.DeviceID) {
		return EnrollResult{}, ErrBadDeviceID
	}
	now := d.now()

	tx, err := d.w.Begin()
	if err != nil {
		return EnrollResult{}, err
	}
	defer tx.Rollback()

	// The sweep runs here as well as on the timer: a server that nobody restarts
	// and that has no timer must still lose its old requests (R4).
	if err := sweepPending(tx, d.stamp(now.Add(-pendingLife))); err != nil {
		return EnrollResult{}, err
	}

	var out EnrollResult
	switch {
	case req.Token == "":
		out, err = d.enrollNoToken(tx, req, ip, now)
	default:
		out, err = d.enrollWithToken(tx, req, ip, now)
	}
	if err != nil {
		return EnrollResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EnrollResult{}, err
	}
	return out, nil
}

// sweepPending removes the requests that waited longer than pendingLife. A row
// that the admin approved stays: the admin made it deliberately, and its device
// may be off for a week.
func sweepPending(tx *tx, cutoff string) error {
	_, err := tx.Exec(`DELETE FROM pending_enrollments
		WHERE approved_at = '' AND created_at < ?`, cutoff)
	return err
}

// SweepPending removes the requests that waited longer than pendingLife. A timer
// of the server calls it, so a quiet server also loses its old requests.
func (d *DB) SweepPending() error {
	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := sweepPending(tx, d.stamp(d.now().Add(-pendingLife))); err != nil {
		return err
	}
	return tx.Commit()
}

// enrollWithToken handles a request that carries a token of one of the three
// kinds: a device token, a claim secret, or an enrollment token.
func (d *DB) enrollWithToken(tx *tx, req manifest.EnrollRequest, ip string, now time.Time) (EnrollResult, error) {
	hash := hashSecret(req.Token)

	// A device token. The device holds a token already, so nothing changes.
	var id, got string
	err := tx.QueryRow(`SELECT id, token_hash FROM devices WHERE token_hash = ? AND token_hash <> ''`, hash).
		Scan(&id, &got)
	if err == nil && equalHash(got, hash) {
		if id != req.DeviceID {
			// The token belongs to another row. A device must never be able to
			// take over the row of a second device with its own ID.
			return EnrollResult{}, ErrBadToken
		}
		if err := d.touchEnrolled(tx, req, ip, now); err != nil {
			return EnrollResult{}, err
		}
		return EnrollResult{EnrollResponse: manifest.EnrollResponse{
			Status: "paired", DeviceToken: req.Token}}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return EnrollResult{}, err
	}

	// A claim secret. The device asks if the admin approved it yet.
	if out, found, err := d.answerClaim(tx, req, ip, now, hash); err != nil {
		return EnrollResult{}, err
	} else if found {
		return out, nil
	}

	// An enrollment token.
	return d.enrollWithEnrollmentToken(tx, req, ip, now, hash)
}

// answerClaim answers a device that polls with its claim secret. It gives false
// when no waiting request matches the secret.
func (d *DB) answerClaim(tx *tx, req manifest.EnrollRequest, ip string, now time.Time, hash string) (EnrollResult, bool, error) {
	var (
		rowID               int64
		code                string
		deviceID, claimHash string
		approvedAt          string
	)
	err := tx.QueryRow(`SELECT id, pairing_code, device_id, claim_hash, approved_at
		FROM pending_enrollments WHERE claim_hash = ?`, hash).
		Scan(&rowID, &code, &deviceID, &claimHash, &approvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return EnrollResult{}, false, nil
	}
	if err != nil {
		return EnrollResult{}, false, err
	}
	if deviceID != req.DeviceID || !equalHash(claimHash, hash) {
		return EnrollResult{}, false, ErrBadToken
	}

	if approvedAt == "" {
		if _, err := tx.Exec(`UPDATE pending_enrollments
			SET ip = ?, version = ?, last_poll_at = ? WHERE id = ?`,
			ip, req.Version, d.stamp(now), rowID); err != nil {
			return EnrollResult{}, false, err
		}
		return EnrollResult{EnrollResponse: manifest.EnrollResponse{
			Status: "pending", PairingCode: code, ClaimSecret: req.Token}}, true, nil
	}

	// The admin approved it. Make the device token now.
	//
	// The token is made at this moment and not at the moment of approval, so the
	// server never holds a device token in plain form, not even for the minute
	// between an approval and the next poll of the device.
	token := newToken()
	if err := d.pairRow(tx, req, ip, now, hashSecret(token)); err != nil {
		return EnrollResult{}, false, err
	}
	return EnrollResult{EnrollResponse: manifest.EnrollResponse{
		Status: "paired", DeviceToken: token}}, true, nil
}

// enrollWithEnrollmentToken handles a fleet-wide enrollment token. One token
// pairs any number of cards (D25).
func (d *DB) enrollWithEnrollmentToken(tx *tx, req manifest.EnrollRequest, ip string, now time.Time, hash string) (EnrollResult, error) {
	var (
		tokenID int64
		mode    string
		groupID sql.NullInt64
	)
	err := tx.QueryRow(`SELECT id, mode, group_id FROM enrollment_tokens WHERE token_hash = ?`, hash).
		Scan(&tokenID, &mode, &groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return EnrollResult{}, ErrBadToken
	}
	if err != nil {
		return EnrollResult{}, err
	}

	// Count the use and check the limits in one statement. Two devices that boot
	// together cannot both take the last use of a token this way.
	res, err := tx.Exec(`UPDATE enrollment_tokens SET uses = uses + 1
		WHERE id = ? AND revoked = 0
		  AND (max_uses = 0 OR uses < max_uses)
		  AND (expires_at = '' OR expires_at > ?)`, tokenID, d.stamp(now))
	if err != nil {
		return EnrollResult{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return EnrollResult{}, err
	} else if n == 0 {
		return EnrollResult{}, ErrBadToken
	}

	row, have, err := d.enrollTarget(tx, req.DeviceID)
	if err != nil {
		return EnrollResult{}, err
	}

	switch {
	case have && row.everPaired && mode == "auto" && req.HardwareID != "" &&
		equalHash(row.hardwareID, req.HardwareID):
		// R2: the reflashed card of a box that we know. The hardware ID matches,
		// so this is the same machine and it gets a new token.
		token := newToken()
		if err := d.pairRow(tx, req, ip, now, hashSecret(token)); err != nil {
			return EnrollResult{}, err
		}
		return EnrollResult{EnrollResponse: manifest.EnrollResponse{
			Status: "paired", DeviceToken: token}}, nil

	case have && row.everPaired:
		// R3: another machine asks to be a screen that works, or the token is in
		// pending mode. The row of the screen is untouched.
		return d.makePending(tx, req, ip, now, tokenID, req.DeviceID)

	case mode == "auto":
		// R3: a device ID with no paired row. Make or take the row and pair it.
		if err := d.upsertDevice(tx, req, ip, now, intFromNull(groupID), true); err != nil {
			return EnrollResult{}, err
		}
		token := newToken()
		if err := d.pairRow(tx, req, ip, now, hashSecret(token)); err != nil {
			return EnrollResult{}, err
		}
		return EnrollResult{EnrollResponse: manifest.EnrollResponse{
			Status: "paired", DeviceToken: token}}, nil

	default:
		return d.makePending(tx, req, ip, now, tokenID, "")
	}
}

// enrollNoToken handles a request with no token at all: the device shows a code
// on its fallback screen and the admin approves it (D25).
func (d *DB) enrollNoToken(tx *tx, req manifest.EnrollRequest, ip string, now time.Time) (EnrollResult, error) {
	row, have, err := d.enrollTarget(tx, req.DeviceID)
	if err != nil {
		return EnrollResult{}, err
	}
	collides := ""
	if have && row.everPaired {
		collides = req.DeviceID
	}
	return d.makePending(tx, req, ip, now, 0, collides)
}

// deviceRowState is the part of a devices row that the enroll rules read.
type deviceRowState struct {
	everPaired bool
	hardwareID string
}

// enrollTarget reads the row of the device ID of a request, if there is one.
func (d *DB) enrollTarget(tx *tx, id string) (deviceRowState, bool, error) {
	var (
		row        deviceRowState
		everPaired int
	)
	err := tx.QueryRow(`SELECT ever_paired, hardware_id FROM devices WHERE id = ?`, id).
		Scan(&everPaired, &row.hardwareID)
	if errors.Is(err, sql.ErrNoRows) {
		return deviceRowState{}, false, nil
	}
	if err != nil {
		return deviceRowState{}, false, err
	}
	row.everPaired = everPaired != 0
	return row, true, nil
}

// makePending puts the request in the pending list with a new code and a new
// claim secret. A device that already waits keeps its code, so that the code on
// the screen does not change at every poll.
func (d *DB) makePending(tx *tx, req manifest.EnrollRequest, ip string, now time.Time, tokenID int64, collides string) (EnrollResult, error) {
	// A request of this device that already waits keeps its code. The claim
	// secret does not come back: the table holds only its hash, so a device that
	// lost the secret takes a new one.
	var rowID int64
	var code string
	err := tx.QueryRow(`SELECT id, pairing_code FROM pending_enrollments
		WHERE device_id = ? AND approved_at = '' ORDER BY id LIMIT 1`, req.DeviceID).Scan(&rowID, &code)
	switch {
	case err == nil:
		secret := newToken()
		if _, err := tx.Exec(`UPDATE pending_enrollments
			SET claim_hash = ?, hardware_id = ?, name = ?, version = ?, ip = ?,
			    token_id = ?, collides_with = ?, last_poll_at = ?
			WHERE id = ?`,
			hashSecret(secret), req.HardwareID, strings.TrimSpace(req.Name), req.Version, ip,
			nullInt64(tokenID), collides, d.stamp(now), rowID); err != nil {
			return EnrollResult{}, err
		}
		return EnrollResult{EnrollResponse: manifest.EnrollResponse{
			Status: "pending", PairingCode: code, ClaimSecret: secret}}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return EnrollResult{}, err
	}

	var waiting int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pending_enrollments`).Scan(&waiting); err != nil {
		return EnrollResult{}, err
	}
	if waiting >= maxPending {
		return EnrollResult{}, ErrTooManyPending
	}

	code, err = freeCode(tx)
	if err != nil {
		return EnrollResult{}, err
	}
	secret := newToken()
	if _, err := tx.Exec(`INSERT INTO pending_enrollments
		(claim_hash, pairing_code, device_id, hardware_id, name, version, ip,
		 token_id, collides_with, created_at, last_poll_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hashSecret(secret), code, req.DeviceID, req.HardwareID, strings.TrimSpace(req.Name),
		req.Version, ip, nullInt64(tokenID), collides, d.stamp(now), d.stamp(now)); err != nil {
		return EnrollResult{}, err
	}
	return EnrollResult{
		EnrollResponse: manifest.EnrollResponse{
			Status: "pending", PairingCode: code, ClaimSecret: secret},
		Created: true,
	}, nil
}

// freeCode makes a pairing code that no other request holds. Two screens that
// show one code would be two screens that the admin cannot tell apart.
func freeCode(tx *tx) (string, error) {
	for try := 0; try < 20; try++ {
		code := newPairingCode()
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM pending_enrollments WHERE pairing_code = ?`, code).
			Scan(&n); err != nil {
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
// reports. It never touches the token. takeGroup puts a row that was never
// paired in the group of its enrollment token; a row that was ever paired keeps
// the group that the admin gave it.
func (d *DB) upsertDevice(tx *tx, req manifest.EnrollRequest, ip string, now time.Time, groupID int64, takeGroup bool) error {
	name := strings.TrimSpace(req.Name)
	stamp := d.stamp(now)
	res, err := tx.Exec(`UPDATE devices SET hardware_id = ?, version = ?, last_ip = ?,
		name = CASE WHEN name = '' THEN ? ELSE name END WHERE id = ? AND ever_paired = 0`,
		req.HardwareID, req.Version, ip, name, req.DeviceID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		if takeGroup && groupID != 0 {
			if _, err := tx.Exec(`UPDATE devices SET group_id = ?
				WHERE id = ? AND ever_paired = 0`, groupID, req.DeviceID); err != nil {
				return err
			}
		}
		return nil
	}
	// No row changed: either the row is there and was paired before, which R1
	// protects, or there is no row at all.
	var have int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM devices WHERE id = ?`, req.DeviceID).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return nil
	}
	_, err = tx.Exec(`INSERT INTO devices (id, name, group_id, hardware_id, version, last_ip, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		req.DeviceID, name, nullInt64(groupID), req.HardwareID, req.Version, ip, stamp)
	return err
}

// pairRow marks the row as paired with this token hash.
func (d *DB) pairRow(tx *tx, req manifest.EnrollRequest, ip string, now time.Time, tokenHash string) error {
	if err := d.touchEnrolled(tx, req, ip, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE devices SET token_hash = ?, ever_paired = 1, paired_at = ?,
		needs_confirm = 0, pending_hardware_id = '', pending_hardware_at = ''
		WHERE id = ?`, tokenHash, d.stamp(now), req.DeviceID); err != nil {
		return err
	}
	// The request that asked for this token is finished.
	_, err := tx.Exec(`DELETE FROM pending_enrollments WHERE device_id = ?`, req.DeviceID)
	return err
}

// touchEnrolled writes the values that an enroll request reports. It never moves
// the stored hardware ID of a paired row: only the heartbeat rules of D21 may do
// that, and only with the agreement of the admin.
func (d *DB) touchEnrolled(tx *tx, req manifest.EnrollRequest, ip string, now time.Time) error {
	_, err := tx.Exec(`UPDATE devices SET version = ?, last_ip = ?, last_seen = ?,
		hardware_id = CASE WHEN hardware_id = '' THEN ? ELSE hardware_id END
		WHERE id = ?`, req.Version, ip, d.stamp(now), req.HardwareID, req.DeviceID)
	return err
}

// PendingEnrollment is one row of the pending list. The admin UI shows it.
type PendingEnrollment struct {
	ID          int64  `json:"id"`
	DeviceID    string `json:"device_id"`
	PairingCode string `json:"pairing_code"`
	HardwareID  string `json:"hardware_id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	IP          string `json:"ip"`
	TokenID     int64  `json:"token_id"`
	// CollidesWith names a device that is already paired and that this request
	// asks to be. The UI warns before the admin approves it.
	CollidesWith string    `json:"collides_with"`
	CreatedAt    time.Time `json:"created_at"`
	LastPollAt   time.Time `json:"last_poll_at"`
	// ApprovedAt is set while the request waits for its device to come back for
	// its token. Such a row is not in the pending list of the UI any more.
	ApprovedAt time.Time `json:"approved_at"`
}

// pendingColumns is the select list of a pending row.
const pendingColumns = `id, device_id, pairing_code, hardware_id, name, version, ip,
	COALESCE(token_id, 0), collides_with, created_at, last_poll_at, approved_at`

func scanPending(s interface{ Scan(...any) error }) (PendingEnrollment, error) {
	var (
		p                               PendingEnrollment
		createdAt, lastPoll, approvedAt string
	)
	if err := s.Scan(&p.ID, &p.DeviceID, &p.PairingCode, &p.HardwareID, &p.Name,
		&p.Version, &p.IP, &p.TokenID, &p.CollidesWith, &createdAt, &lastPoll, &approvedAt); err != nil {
		return PendingEnrollment{}, err
	}
	p.CreatedAt = parseTime(createdAt)
	p.LastPollAt = parseTime(lastPoll)
	p.ApprovedAt = parseTime(approvedAt)
	return p, nil
}

// PendingEnrollments gives every request that waits for the admin, oldest first.
// A request that the admin approved is not in the list: it waits for its device
// and not for a person.
func (d *DB) PendingEnrollments() ([]PendingEnrollment, error) {
	rows, err := d.r.Query(`SELECT ` + pendingColumns + ` FROM pending_enrollments
		WHERE approved_at = '' ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PendingEnrollment{}
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PendingByCode gives the request that shows this code. The admin UI approves by
// code, because the code is what a person can read off the screen.
func (d *DB) PendingByCode(code string) (PendingEnrollment, error) {
	row := d.r.QueryRow(`SELECT `+pendingColumns+` FROM pending_enrollments
		WHERE pairing_code = ? AND approved_at = ''`,
		strings.ToUpper(strings.TrimSpace(code)))
	p, err := scanPending(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PendingEnrollment{}, ErrNotFound
	}
	return p, err
}

// PendingByDevice gives the request of one device ID.
func (d *DB) PendingByDevice(id string) (PendingEnrollment, error) {
	row := d.r.QueryRow(`SELECT `+pendingColumns+` FROM pending_enrollments
		WHERE device_id = ? AND approved_at = '' ORDER BY id LIMIT 1`, id)
	p, err := scanPending(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PendingEnrollment{}, ErrNotFound
	}
	return p, err
}

// ApprovePending lets one waiting request in. The device gets its token at its
// next poll. A groupID of 0 leaves the group to the rules below.
//
// A row that the request collides with keeps its playlists, its name and its
// group: the admin said "this machine is that screen now", not "start again".
// The token of the old machine goes away, because two boxes must never hold one
// token.
func (d *DB) ApprovePending(pendingID int64, groupID int64) error {
	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	row := tx.QueryRow(`SELECT `+pendingColumns+` FROM pending_enrollments
		WHERE id = ? AND approved_at = ''`, pendingID)
	p, err := scanPending(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotPending
	}
	if err != nil {
		return err
	}

	now := d.now()
	_, have, err := d.enrollTarget(tx, p.DeviceID)
	if err != nil {
		return err
	}

	tokenGroup := int64(0)
	if p.TokenID != 0 {
		var g sql.NullInt64
		err := tx.QueryRow(`SELECT group_id FROM enrollment_tokens WHERE id = ?`, p.TokenID).Scan(&g)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		tokenGroup = intFromNull(g)
	}

	req := manifest.EnrollRequest{
		DeviceID: p.DeviceID, HardwareID: p.HardwareID, Name: p.Name, Version: p.Version,
	}
	// A row that was never paired takes the group of its enrollment token. The
	// group that the admin sends with the approval wins over both.
	if err := d.upsertDevice(tx, req, p.IP, now, tokenGroup, !have); err != nil {
		return err
	}
	if groupID != 0 {
		if _, err := tx.Exec(`UPDATE devices SET group_id = ? WHERE id = ?`, groupID, p.DeviceID); err != nil {
			return err
		}
	}
	if p.CollidesWith != "" {
		// The machine changed. The token of the old machine stops working now, and
		// the new machine takes the stored hardware ID with the approval.
		if _, err := tx.Exec(`UPDATE devices SET token_hash = '', hardware_id = ?,
			needs_confirm = 0, pending_hardware_id = '', pending_hardware_at = '',
			conflict = 0, conflict_hardware_id = ''
			WHERE id = ?`, p.HardwareID, p.DeviceID); err != nil {
			return err
		}
	}

	// The row waits for its device now. The server makes no device token here, so
	// it never holds one in plain form.
	if _, err := tx.Exec(`UPDATE pending_enrollments SET approved_at = ? WHERE id = ?`,
		d.stamp(now), pendingID); err != nil {
		return err
	}
	return tx.Commit()
}

// RejectPending turns one waiting request away. The row of the request goes, and
// a device row that never paired goes with it. A screen that paired before keeps
// its row and its token: the admin said "this request is not one of mine", not
// "delete the screen".
func (d *DB) RejectPending(pendingID int64) error {
	tx, err := d.w.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var deviceID string
	err = tx.QueryRow(`SELECT device_id FROM pending_enrollments WHERE id = ?`, pendingID).Scan(&deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotPending
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM pending_enrollments WHERE id = ?`, pendingID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM devices WHERE id = ? AND ever_paired = 0`, deviceID); err != nil {
		return err
	}
	return tx.Commit()
}

// CountPending gives the number of requests that wait for the admin. The health
// page and the sidebar totals show it.
func (d *DB) CountPending() (int, error) {
	var n int
	err := d.r.QueryRow(`SELECT COUNT(*) FROM pending_enrollments WHERE approved_at = ''`).Scan(&n)
	return n, err
}
