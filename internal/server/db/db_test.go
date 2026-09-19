package db

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// open makes a database in a temporary directory.
func open(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestOpenTwiceKeepsTheSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateGroup("Lobby"); err != nil {
		t.Fatal(err)
	}
	d.Close()

	again, err := Open(path)
	if err != nil {
		t.Fatalf("the second open failed: %v", err)
	}
	defer again.Close()

	groups, err := again.Groups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Name != "Lobby" {
		t.Fatalf("the group did not survive the reopen: %+v", groups)
	}
}

func TestIntegrityCheckSaysOK(t *testing.T) {
	if got, err := open(t).IntegrityCheck(); err != nil || got != "ok" {
		t.Fatalf("integrity check gave %q, %v", got, err)
	}
}

func TestForeignKeysAreOn(t *testing.T) {
	d := open(t)
	// A command for a device that is not there must fail, and the failure must be
	// ErrNotFound and not a plain error: the route answers 404 for it.
	_, err := d.QueueCommand("px-nothing", "reboot", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a command for a device that does not exist gave %v, want ErrNotFound", err)
	}
}

// TestOpenResetsAWorkingMirror covers the process that stopped in the middle of a
// mirror. A row that says "working" for ever makes the mirror route answer 409 and
// the admin has no button that works.
func TestOpenResetsAWorkingMirror(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetMirrorState("1.5.0", MirrorWorking, ""); err != nil {
		t.Fatal(err)
	}
	d.Close()

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()

	rel, err := again.Release("1.5.0")
	if err != nil {
		t.Fatal(err)
	}
	if rel.MirrorState != MirrorFailed {
		t.Fatalf("the mirror state after the restart is %q, want %q", rel.MirrorState, MirrorFailed)
	}
	if rel.MirrorError == "" {
		t.Fatal("the failure has no text for the admin")
	}
}

// pair makes a paired device with the given ID.
func pair(t *testing.T, d *DB, id string) string {
	t.Helper()
	_, token, err := d.CreateEnrollToken("test", "auto", 0, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Enroll(req(id, token), "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "paired" || res.DeviceToken == "" {
		t.Fatalf("enroll gave %+v", res)
	}
	return res.DeviceToken
}

func req(id, token string) manifest.EnrollRequest {
	return manifest.EnrollRequest{
		DeviceID: id, HardwareID: hw(id), Name: id, Token: token, Version: "1.0.0",
	}
}

// hw makes the hardware ID of a test device. A real one is the full SHA-256 of the
// hardware source, so the test values are long as well: R2 compares them.
func hw(id string) string {
	out := []byte("0000000000000000000000000000000000000000000000000000000000000000")
	copy(out, id)
	return string(out)
}

func TestEnrollWithAutoToken(t *testing.T) {
	d := open(t)
	token := pair(t, d, "px-aaaa0001")

	dev, err := d.DeviceByToken(token)
	if err != nil {
		t.Fatalf("the token does not find its device: %v", err)
	}
	if dev.Pending {
		t.Fatal("an auto token left the device pending")
	}
	if dev.PairedAt.IsZero() {
		t.Fatal("paired_at is empty")
	}
	if dev.Name != "px-aaaa0001" || dev.HardwareID != hw("px-aaaa0001") {
		t.Fatalf("the row is %+v", dev)
	}
	if !errors.Is(err, nil) {
		t.Fatal(err)
	}
	if _, err := d.DeviceByToken("not a token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a wrong token gave %v", err)
	}
}

func TestEnrollTokenLimits(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		maxUses int
		expires time.Time
		revoke  bool
		wantErr bool
	}{
		{name: "one use left", mode: "auto", maxUses: 2},
		{name: "used up", mode: "auto", maxUses: 1, wantErr: true},
		{name: "expired", mode: "auto", expires: time.Now().Add(time.Hour), wantErr: true},
		{name: "revoked", mode: "auto", revoke: true, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := open(t)
			id, token, err := d.CreateEnrollToken("t", c.mode, 0, c.expires, c.maxUses)
			if err != nil {
				t.Fatal(err)
			}
			// The first device always goes through. That proves that the token
			// worked before the limit closed it, so the case measures the limit and
			// not a token that never worked.
			if _, err := d.Enroll(req("px-first", token), "10.0.0.1"); err != nil {
				t.Fatalf("the first enroll failed: %v", err)
			}
			if c.revoke {
				if err := d.RevokeEnrollToken(id); err != nil {
					t.Fatal(err)
				}
			}
			if !c.expires.IsZero() {
				// Move the clock past the expiry.
				d.SetClock(func() time.Time { return c.expires.Add(time.Minute) })
			}
			_, err = d.Enroll(req("px-second", token), "10.0.0.2")
			if c.wantErr && !errors.Is(err, ErrBadToken) {
				t.Fatalf("the second enroll gave %v, want ErrBadToken", err)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("the second enroll gave %v, want no error", err)
			}
		})
	}
}

func TestEnrollPendingModeThenApprove(t *testing.T) {
	d := open(t)
	_, token, err := d.CreateEnrollToken("t", "pending", 0, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Enroll(req("px-bbbb0002", token), "10.0.0.7")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "pending" || res.ClaimSecret == "" || len(res.PairingCode) != codeLength {
		t.Fatalf("enroll gave %+v", res)
	}
	if res.DeviceToken != "" {
		t.Fatal("a pending device got a token")
	}
	if !res.Created {
		t.Fatal("the first pending request does not report that it made a row")
	}
	// A request that waits makes no devices row. That is rule R1: the devices
	// table changes at approval only.
	if _, err := d.Device("px-bbbb0002"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a request that waits made a devices row: %v", err)
	}

	// A poll with the claim secret still says pending, and the code does not move.
	poll, err := d.Enroll(req("px-bbbb0002", res.ClaimSecret), "10.0.0.7")
	if err != nil {
		t.Fatal(err)
	}
	if poll.Status != "pending" || poll.PairingCode != res.PairingCode {
		t.Fatalf("the poll gave %+v, want the same code as %+v", poll, res)
	}
	if poll.Created {
		t.Fatal("a poll of a request that waits counted as a new row")
	}

	waiting, err := d.PendingByCode(res.PairingCode)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApprovePending(waiting.ID, 0); err != nil {
		t.Fatal(err)
	}
	// A second approval is a second click of the button, not a broken database.
	if err := d.ApprovePending(waiting.ID, 0); !errors.Is(err, ErrNotPending) {
		t.Fatalf("the second approval gave %v, want ErrNotPending", err)
	}

	done, err := d.Enroll(req("px-bbbb0002", res.ClaimSecret), "10.0.0.7")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "paired" || done.DeviceToken == "" {
		t.Fatalf("the poll after the approval gave %+v", done)
	}
	if _, err := d.DeviceByToken(done.DeviceToken); err != nil {
		t.Fatalf("the new token does not find its device: %v", err)
	}
}

// TestClaimSecretIsStoredHashed is the rule R4. A claim secret is exchangeable for
// a device token, so anybody who reads the database file, a backup or the
// write-ahead log must not find it.
func TestClaimSecretIsStoredHashed(t *testing.T) {
	d := open(t)
	res, err := d.Enroll(req("px-hash0001", ""), "10.0.0.9")
	if err != nil {
		t.Fatal(err)
	}
	if res.ClaimSecret == "" {
		t.Fatal("the request got no claim secret")
	}

	var stored string
	if err := d.r.QueryRow(`SELECT claim_hash FROM pending_enrollments WHERE device_id = ?`,
		"px-hash0001").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == res.ClaimSecret {
		t.Fatal("the table holds the claim secret in plain form")
	}
	if stored != hashSecret(res.ClaimSecret) {
		t.Fatalf("the table holds %q, want the SHA-256 of the secret", stored)
	}
	// A secret that does not match finds nothing, whatever device ID it names.
	if _, err := d.Enroll(req("px-hash0001", "a secret that nobody gave out"), "10.0.0.9"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("a wrong claim secret gave %v, want ErrBadToken", err)
	}
}

// TestPendingRequestsExpire is the second half of R4. An open route that makes rows
// must lose them again, or the table only grows.
func TestPendingRequestsExpire(t *testing.T) {
	d := open(t)
	if _, err := d.Enroll(req("px-old00001", ""), "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.CountPending(); n != 1 {
		t.Fatalf("the list holds %d requests", n)
	}

	d.SetClock(func() time.Time { return time.Now().Add(pendingLife + time.Minute) })
	if err := d.SweepPending(); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.CountPending(); n != 0 {
		t.Fatalf("the list still holds %d requests after the sweep", n)
	}
}

// TestPendingListHasACap is R5. Without it an unauthenticated caller fills the
// table by looping over device IDs.
func TestPendingListHasACap(t *testing.T) {
	d := open(t)
	for i := 0; i < maxPending; i++ {
		id := "px-" + hashOfIndex(i)[:8]
		if _, err := d.Enroll(req(id, ""), "10.0.0.1"); err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
	}
	if _, err := d.Enroll(req("px-onemore", ""), "10.0.0.1"); !errors.Is(err, ErrTooManyPending) {
		t.Fatalf("the request over the cap gave %v, want ErrTooManyPending", err)
	}
}

func TestPairingCodeAlphabet(t *testing.T) {
	// The code must never hold 0, O, 1 or I: a person reads it off a screen.
	for i := 0; i < 200; i++ {
		code := newPairingCode()
		if len(code) != codeLength {
			t.Fatalf("the code %q has the wrong length", code)
		}
		for _, c := range code {
			switch c {
			case '0', 'O', '1', 'I':
				t.Fatalf("the code %q holds the character %q", code, c)
			}
		}
	}
}

func TestEnrollByCode(t *testing.T) {
	d := open(t)
	res, err := d.Enroll(req("px-cccc0003", ""), "10.0.0.8")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "pending" || res.PairingCode == "" {
		t.Fatalf("enroll gave %+v", res)
	}
	waiting, err := d.PendingByCode(res.PairingCode)
	if err != nil {
		t.Fatalf("the code does not find its request: %v", err)
	}
	if waiting.DeviceID != "px-cccc0003" {
		t.Fatalf("the code found %s", waiting.DeviceID)
	}

	// A reject removes the request and the row of a device that never paired.
	if err := d.RejectPending(waiting.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PendingByCode(res.PairingCode); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the rejected request is still there: %v", err)
	}
	if _, err := d.Device("px-cccc0003"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the rejected device is still there: %v", err)
	}
}

// TestEnrollTokenTakesTheGroupOfANewRow covers item (d) of the review. A row that
// was never paired takes the group of the enrollment token that it enrolls with. A
// row that was ever paired keeps the group that the admin gave it.
func TestEnrollTokenTakesTheGroupOfANewRow(t *testing.T) {
	d := open(t)
	warehouse, err := d.CreateGroup("Warehouse")
	if err != nil {
		t.Fatal(err)
	}
	lobby, err := d.CreateGroup("Lobby")
	if err != nil {
		t.Fatal(err)
	}

	_, token, err := d.CreateEnrollToken("batch", "auto", warehouse, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Enroll(req("px-grp00001", token), "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	dev, err := d.Device("px-grp00001")
	if err != nil {
		t.Fatal(err)
	}
	if dev.GroupID != warehouse {
		t.Fatalf("the new row landed in group %d, want the group of the token %d", dev.GroupID, warehouse)
	}

	// The admin moves it, and the card enrolls again with the same token. The
	// choice of the admin stays.
	if err := d.MoveDevice("px-grp00001", lobby); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Enroll(req("px-grp00001", token), "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if dev, err = d.Device("px-grp00001"); err != nil {
		t.Fatal(err)
	}
	if dev.GroupID != lobby {
		t.Fatalf("the re-enrolment moved the row to group %d and undid the admin", dev.GroupID)
	}

	// A pending request takes the group of its token at the approval.
	_, pendingToken, err := d.CreateEnrollToken("batch 2", "pending", warehouse, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Enroll(req("px-grp00002", pendingToken), "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := d.PendingByCode(res.PairingCode)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApprovePending(waiting.ID, 0); err != nil {
		t.Fatal(err)
	}
	if dev, err = d.Device("px-grp00002"); err != nil {
		t.Fatal(err)
	}
	if dev.GroupID != warehouse {
		t.Fatalf("the approved row landed in group %d, want %d", dev.GroupID, warehouse)
	}
}

func TestEnrollAgainWithTheDeviceToken(t *testing.T) {
	d := open(t)
	token := pair(t, d, "px-dddd0004")

	res, err := d.Enroll(req("px-dddd0004", token), "10.0.0.9")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "paired" || res.DeviceToken != token {
		t.Fatalf("the second enroll gave %+v, want the same token", res)
	}
}

func TestATokenOfAnotherDeviceIsRefused(t *testing.T) {
	d := open(t)
	token := pair(t, d, "px-eeee0005")

	// A device that presents the token of another row must be refused. This is
	// the "device token scoped to its own row" rule.
	if _, err := d.Enroll(req("px-ffff0006", token), "10.0.0.10"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("the enroll gave %v, want ErrBadToken", err)
	}
}

// TestEnrollmentTokenCannotTakeOverAPairedRow is rule R1 and R3. An enrollment
// token is a fleet-wide value that many cards hold, so its holder must not be able
// to name the device ID of a screen that works and take that screen over.
func TestEnrollmentTokenCannotTakeOverAPairedRow(t *testing.T) {
	d := open(t)
	victim := pair(t, d, "px-victim01")

	// A machine with another hardware ID asks to be px-victim01, with a valid
	// fleet token.
	_, token, err := d.CreateEnrollToken("batch", "auto", 0, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	attack := manifest.EnrollRequest{
		DeviceID: "px-victim01", HardwareID: hw("another-machine"),
		Name: "mine now", Token: token, Version: "9.9.9",
	}
	res, err := d.Enroll(attack, "10.0.0.66")
	if err != nil {
		t.Fatalf("the request gave %v; it must land in the pending list", err)
	}
	if res.Status != "pending" || res.DeviceToken != "" {
		t.Fatalf("the request gave %+v; it must get no token", res)
	}

	// The screen on the wall keeps its token, its hardware ID and its name.
	dev, err := d.DeviceByToken(victim)
	if err != nil {
		t.Fatalf("the screen lost its token: %v", err)
	}
	if dev.HardwareID != hw("px-victim01") {
		t.Fatalf("the hardware ID of the screen is now %q", dev.HardwareID)
	}
	if dev.Name != "px-victim01" {
		t.Fatalf("the name of the screen is now %q", dev.Name)
	}

	// The admin sees the warning.
	waiting, err := d.PendingByCode(res.PairingCode)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.CollidesWith != "px-victim01" {
		t.Fatalf("the request does not warn about the collision: %+v", waiting)
	}

	// The same rule holds with no token at all.
	byCode, err := d.Enroll(manifest.EnrollRequest{
		DeviceID: "px-victim01", HardwareID: hw("a third machine"),
	}, "10.0.0.67")
	if err != nil {
		t.Fatal(err)
	}
	if byCode.DeviceToken != "" {
		t.Fatalf("the code flow gave a token for a row that is paired: %+v", byCode)
	}
	if _, err := d.DeviceByToken(victim); err != nil {
		t.Fatalf("the screen lost its token to the code flow: %v", err)
	}

	// The approval is the one way in, and it revokes the token of the old machine.
	if err := d.ApprovePending(waiting.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DeviceByToken(victim); !errors.Is(err, ErrNotFound) {
		t.Fatal("the token of the machine that was replaced still works")
	}
}

// TestReflashedCardPairsAgain is rule R2. A card that lost its ext4 state comes
// back with the same hardware ID, and that is the one case where an enrollment
// token may give a paired row a new token.
func TestReflashedCardPairsAgain(t *testing.T) {
	d := open(t)
	old := pair(t, d, "px-reflash1")

	_, token, err := d.CreateEnrollToken("batch", "auto", 0, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Enroll(req("px-reflash1", token), "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "paired" || res.DeviceToken == "" {
		t.Fatalf("the reflashed card gave %+v", res)
	}
	if res.DeviceToken == old {
		t.Fatal("the reflashed card got the token that it had lost")
	}
	// The old token is gone: one row holds one token.
	if _, err := d.DeviceByToken(old); !errors.Is(err, ErrNotFound) {
		t.Fatal("the old token still works")
	}

	// A pending-mode token gives no such shortcut, even with the same hardware.
	_, pendingToken, err := d.CreateEnrollToken("batch 2", "pending", 0, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	slow, err := d.Enroll(req("px-reflash1", pendingToken), "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if slow.DeviceToken != "" {
		t.Fatalf("a pending token paired a row at once: %+v", slow)
	}
}

func TestBadDeviceID(t *testing.T) {
	d := open(t)
	for _, id := range []string{"", "px-UPPER", "px-../etc", "px with space"} {
		if _, err := d.Enroll(req(id, ""), "10.0.0.1"); !errors.Is(err, ErrBadDeviceID) {
			t.Fatalf("the device ID %q gave %v, want ErrBadDeviceID", id, err)
		}
	}
}

// clock is a clock that a test moves by hand. The identity rules of D21 are about
// the order of requests, so a test of them cannot use the real time.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }
func (c *clock) add(d time.Duration) {
	c.at = c.at.Add(d)
}

// TestHardwareRepairWithTheRealCallOrder is the case that the old rule could not
// reach.
//
// A device polls its manifest and then sends its heartbeat. The poll moves
// last_seen, so a rule that compared the hardware ID against last_seen read every
// repair as a clone, and the one-click confirmation of D21 was unreachable.
func TestHardwareRepairWithTheRealCallOrder(t *testing.T) {
	d := open(t)
	c := &clock{at: time.Now()}
	d.SetClock(c.now)
	pair(t, d, "px-repair01")

	// The card moves into another box. The device polls first, as a real one does.
	c.add(2 * time.Hour)
	if err := d.TouchSeen("px-repair01", "10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	hb := manifest.Heartbeat{DeviceID: "px-repair01", HardwareID: hw("a new box"), Version: "1.0.0"}
	if err := d.Heartbeat("px-repair01", hb, "10.0.0.5"); err != nil {
		t.Fatal(err)
	}

	dev, err := d.Device("px-repair01")
	if err != nil {
		t.Fatal(err)
	}
	if !dev.NeedsConfirm {
		t.Fatal("the hardware change did not ask for confirmation")
	}
	if dev.Conflict {
		t.Fatal("a repair was read as a clone")
	}
	// The card still plays while the admin has not answered.
	if dev.HardwareID != hw("a new box") || dev.PrevHardwareID != hw("px-repair01") {
		t.Fatalf("the hardware IDs of the view are %q and %q", dev.HardwareID, dev.PrevHardwareID)
	}

	// The stored value does not move until the admin agrees.
	var stored string
	if err := d.r.QueryRow(`SELECT hardware_id FROM devices WHERE id = ?`, "px-repair01").
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != hw("px-repair01") {
		t.Fatalf("the stored hardware ID moved to %q before the admin confirmed", stored)
	}

	if err := d.ConfirmHardware("px-repair01"); err != nil {
		t.Fatal(err)
	}
	if dev, err = d.Device("px-repair01"); err != nil {
		t.Fatal(err)
	}
	if dev.NeedsConfirm || dev.HardwareID != hw("a new box") {
		t.Fatalf("the confirmation gave %+v", dev)
	}
}

// TestCloneConflictNeedsTheTwoToTakeTurns is the other half of D21. One machine
// that changed is a repair. Two machines that answer in turn on one token is a
// clone, and only that case may raise the conflict.
func TestCloneConflictNeedsTheTwoToTakeTurns(t *testing.T) {
	d := open(t)
	c := &clock{at: time.Now()}
	d.SetClock(c.now)
	pair(t, d, "px-clone001")

	// Box A is the one that we know. Box B answers with the same token.
	boxB := manifest.Heartbeat{DeviceID: "px-clone001", HardwareID: hw("box-b"), Version: "1.0.0"}
	c.add(time.Minute)
	if err := d.Heartbeat("px-clone001", boxB, "10.0.0.6"); err != nil {
		t.Fatal(err)
	}
	// Box A answers again a minute later. Now the two take turns.
	boxA := manifest.Heartbeat{DeviceID: "px-clone001", HardwareID: hw("px-clone001"), Version: "1.0.0"}
	c.add(time.Minute)
	if err := d.Heartbeat("px-clone001", boxA, "10.0.0.7"); err != nil {
		t.Fatal(err)
	}

	dev, err := d.Device("px-clone001")
	if err != nil {
		t.Fatal(err)
	}
	if !dev.Conflict {
		t.Fatal("two machines that take turns on one token did not make a conflict")
	}
	if dev.ConflictHardwareID != hw("box-b") {
		t.Fatalf("the conflict names %q", dev.ConflictHardwareID)
	}
}

// TestOneHardwareChangeIsNotAClone is the regression of the old rule: a card that
// moved into another box, with a poll before the heartbeat, must never be a
// conflict.
func TestOneHardwareChangeIsNotAClone(t *testing.T) {
	d := open(t)
	c := &clock{at: time.Now()}
	d.SetClock(c.now)
	pair(t, d, "px-once0001")

	// Two heartbeats of the new box, a minute apart, with a poll before each one.
	for i := 0; i < 2; i++ {
		c.add(time.Minute)
		if err := d.TouchSeen("px-once0001", "10.0.0.5"); err != nil {
			t.Fatal(err)
		}
		hb := manifest.Heartbeat{DeviceID: "px-once0001", HardwareID: hw("a new box"), Version: "1.0.0"}
		if err := d.Heartbeat("px-once0001", hb, "10.0.0.5"); err != nil {
			t.Fatal(err)
		}
	}
	dev, err := d.Device("px-once0001")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Conflict {
		t.Fatal("one machine that changed was read as a clone")
	}
	if !dev.NeedsConfirm {
		t.Fatal("the change did not ask for confirmation")
	}
}

func TestCommandDeliveryAndAck(t *testing.T) {
	d := open(t)
	pair(t, d, "px-3333cccc")

	id, err := d.QueueCommand("px-3333cccc", "reboot", map[string]string{"why": "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueCommand("px-3333cccc", "make-coffee", nil); err == nil {
		t.Fatal("an unknown command type was accepted")
	}

	got, err := d.TakeCommands("px-3333cccc")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != id || got[0].Args["why"] != "test" {
		t.Fatalf("the delivery gave %+v", got)
	}
	// A second poll must give nothing: one poll is one delivery.
	if again, _ := d.TakeCommands("px-3333cccc"); len(again) != 0 {
		t.Fatalf("the command was delivered twice: %+v", again)
	}

	list, err := d.Commands("px-3333cccc", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != CommandDelivered {
		t.Fatalf("the state is %+v", list)
	}

	hb := manifest.Heartbeat{DeviceID: "px-3333cccc", HardwareID: hw("px-3333cccc"), Acks: []int64{id}}
	if err := d.Heartbeat("px-3333cccc", hb, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if list, _ = d.Commands("px-3333cccc", 10); list[0].State != CommandAcked {
		t.Fatalf("the state after the ack is %+v", list)
	}
}

// TestTakeCommandsMarksOnlyWhatWentOut is the bug of the old code: the select ran
// on the read pool and the update then marked every undelivered command of the
// device. A command that an admin queued in between was marked as delivered and
// never went out.
func TestTakeCommandsMarksOnlyWhatWentOut(t *testing.T) {
	d := open(t)
	pair(t, d, "px-take0001")

	first, err := d.QueueCommand("px-take0001", "rescan", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.TakeCommands("px-take0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != first {
		t.Fatalf("the delivery gave %+v", got)
	}

	// A command that arrives after the delivery is still queued.
	second, err := d.QueueCommand("px-take0001", "reboot", nil)
	if err != nil {
		t.Fatal(err)
	}
	list, err := d.Commands("px-take0001", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.ID == second && c.State != CommandQueued {
			t.Fatalf("the second command is %q before it went out", c.State)
		}
	}
	got, err = d.TakeCommands("px-take0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != second {
		t.Fatalf("the second delivery gave %+v, want the second command", got)
	}
}

// TestCommandGoesOutAgainAndThenExpires covers the device that took a command and
// never acknowledged it. One more try is worth it; a try every ten minutes for ever
// is a screen that reboots for ever.
func TestCommandGoesOutAgainAndThenExpires(t *testing.T) {
	d := open(t)
	c := &clock{at: time.Now()}
	d.SetClock(c.now)
	pair(t, d, "px-retry001")

	id, err := d.QueueCommand("px-retry001", "reboot", nil)
	if err != nil {
		t.Fatal(err)
	}
	for try := 1; try <= maxDeliveries; try++ {
		got, err := d.TakeCommands("px-retry001")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != id {
			t.Fatalf("delivery %d gave %+v", try, got)
		}
		// Straight after a delivery the command does not go out again.
		if again, _ := d.TakeCommands("px-retry001"); len(again) != 0 {
			t.Fatalf("delivery %d went out twice at once: %+v", try, again)
		}
		c.add(redeliverAfter + time.Minute)
	}

	// The tries are used up. The command is expired and it never goes out again.
	if again, _ := d.TakeCommands("px-retry001"); len(again) != 0 {
		t.Fatalf("the command went out after %d tries: %+v", maxDeliveries, again)
	}
	list, err := d.Commands("px-retry001", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != CommandExpired {
		t.Fatalf("the state is %+v, want expired", list)
	}
	if list[0].Deliveries != maxDeliveries {
		t.Fatalf("the command went out %d times", list[0].Deliveries)
	}
}

func TestADeviceCannotAckAnotherDeviceCommand(t *testing.T) {
	d := open(t)
	pair(t, d, "px-4444dddd")
	pair(t, d, "px-5555eeee")

	id, err := d.QueueCommand("px-4444dddd", "reboot", nil)
	if err != nil {
		t.Fatal(err)
	}
	hb := manifest.Heartbeat{DeviceID: "px-5555eeee", HardwareID: hw("px-5555eeee"), Acks: []int64{id}}
	if err := d.Heartbeat("px-5555eeee", hb, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	list, err := d.Commands("px-4444dddd", 10)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].State == CommandAcked {
		t.Fatal("one device acknowledged the command of another device")
	}
}

// TestHeartbeatAcksAreCapped stops a heartbeat from carrying an unbounded list.
func TestHeartbeatAcksAreCapped(t *testing.T) {
	d := open(t)
	pair(t, d, "px-acks0001")

	acks := make([]int64, maxAcks+1)
	for i := range acks {
		acks[i] = int64(i + 1)
	}
	hb := manifest.Heartbeat{DeviceID: "px-acks0001", HardwareID: hw("px-acks0001"), Acks: acks}
	var errs Errors
	if err := d.Heartbeat("px-acks0001", hb, "10.0.0.1"); !errors.As(err, &errs) {
		t.Fatalf("a heartbeat with %d acks gave %v, want a field error", len(acks), err)
	}
}

// TestDuplicateGroupNameIsASentinel proves that the duplicate answer comes from the
// result code of SQLite and not from the text of its message.
func TestDuplicateGroupNameIsASentinel(t *testing.T) {
	d := open(t)
	if _, err := d.CreateGroup("Lobby"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateGroup("Lobby"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("the second group gave %v, want ErrDuplicate", err)
	}
}

func TestDeviceState(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		dev  Device
		want string
	}{
		{"pending", Device{Pending: true}, StatePending},
		{"conflict", Device{Conflict: true}, StateConflict},
		{"hardware change", Device{NeedsConfirm: true}, StateNeedsConfirm},
		{"never seen", Device{}, StateOffline},
		{"just now", Device{LastSeen: now.Add(-10 * time.Second)}, StateOnline},
		{"two intervals", Device{LastSeen: now.Add(-100 * time.Second)}, StateOnline},
		{"three intervals", Device{LastSeen: now.Add(-200 * time.Second)}, StateQuiet},
		{"two days", Device{LastSeen: now.Add(-48 * time.Hour)}, StateOffline},
		{"own interval", Device{PollSeconds: 600, LastSeen: now.Add(-200 * time.Second)}, StateOnline},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := State(c.dev, 60, now); got != c.want {
				t.Fatalf("the state is %q, want %q", got, c.want)
			}
		})
	}
}
