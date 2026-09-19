package db

import (
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
	// A command for a device that is not there must fail.
	if _, err := d.QueueCommand("px-nothing", "reboot", nil); err == nil {
		t.Fatal("a command for a device that does not exist was accepted")
	}
}

// pair makes a paired device with the given ID.
func pair(t *testing.T, d *DB, id string) string {
	t.Helper()
	_, token, err := d.CreateEnrollToken("test", "auto", 0, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Enroll(manifest.EnrollRequest{
		DeviceID: id, HardwareID: "hw-" + id, Name: id, Token: token, Version: "1.0.0",
	}, "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "paired" || res.DeviceToken == "" {
		t.Fatalf("enroll gave %+v", res)
	}
	return res.DeviceToken
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
	if _, err := d.DeviceByToken("not a token"); err != ErrNotFound {
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
			if c.revoke {
				if err := d.RevokeEnrollToken(id); err != nil {
					t.Fatal(err)
				}
			}
			// The first device always goes through, which proves that the token
			// worked before the limit closed it.
			if _, err := d.Enroll(req("px-first", token), "10.0.0.1"); err != nil && !c.wantErr {
				t.Fatalf("the first enroll failed: %v", err)
			}
			if !c.expires.IsZero() {
				// Move the clock past the expiry.
				d.SetClock(func() time.Time { return c.expires.Add(time.Minute) })
			}
			_, err = d.Enroll(req("px-second", token), "10.0.0.2")
			if c.wantErr && err != ErrBadToken {
				t.Fatalf("the second enroll gave %v, want ErrBadToken", err)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("the second enroll gave %v, want no error", err)
			}
		})
	}
}

func req(id, token string) manifest.EnrollRequest {
	return manifest.EnrollRequest{DeviceID: id, HardwareID: "hw-" + id, Name: id, Token: token, Version: "1.0.0"}
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

	// A poll with the claim secret still says pending, and the code does not move.
	poll, err := d.Enroll(req("px-bbbb0002", res.ClaimSecret), "10.0.0.7")
	if err != nil {
		t.Fatal(err)
	}
	if poll.Status != "pending" || poll.PairingCode != res.PairingCode {
		t.Fatalf("the poll gave %+v, want the same code as %+v", poll, res)
	}

	if err := d.Approve("px-bbbb0002", 0); err != nil {
		t.Fatal(err)
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
	dev, err := d.DeviceByCode(res.PairingCode)
	if err != nil {
		t.Fatalf("the code does not find its device: %v", err)
	}
	if dev.ID != "px-cccc0003" {
		t.Fatalf("the code found %s", dev.ID)
	}

	// A reject of a device that never paired removes the row.
	if err := d.Reject(dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Device(dev.ID); err != ErrNotFound {
		t.Fatalf("the rejected device is still there: %v", err)
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
	if _, err := d.Enroll(req("px-ffff0006", token), "10.0.0.10"); err != ErrBadToken {
		t.Fatalf("the enroll gave %v, want ErrBadToken", err)
	}
}

func TestBadDeviceID(t *testing.T) {
	d := open(t)
	for _, id := range []string{"", "px-UPPER", "px-../etc", "px with space"} {
		if _, err := d.Enroll(req(id, ""), "10.0.0.1"); err != ErrBadDeviceID {
			t.Fatalf("the device ID %q gave %v, want ErrBadDeviceID", id, err)
		}
	}
}

func TestHeartbeatHardwareChange(t *testing.T) {
	d := open(t)
	pair(t, d, "px-1111aaaa")

	// The device was quiet for a day, then it came back on other hardware. That
	// is a repair (D21).
	d.SetClock(func() time.Time { return time.Now().Add(24 * time.Hour) })
	hb := manifest.Heartbeat{DeviceID: "px-1111aaaa", HardwareID: "hw-new", Version: "1.0.0"}
	if err := d.Heartbeat("px-1111aaaa", hb, "10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	dev, err := d.Device("px-1111aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if !dev.NeedsConfirm {
		t.Fatal("the hardware change did not ask for confirmation")
	}
	if dev.HardwareID != "hw-new" || dev.PrevHardwareID != "hw-px-1111aaaa" {
		t.Fatalf("the hardware IDs are %q and %q", dev.HardwareID, dev.PrevHardwareID)
	}
	if dev.Conflict {
		t.Fatal("a repair was read as a clone")
	}

	if err := d.ConfirmHardware(dev.ID); err != nil {
		t.Fatal(err)
	}
	if dev, _ = d.Device(dev.ID); dev.NeedsConfirm {
		t.Fatal("the confirmation did not clear the flag")
	}
}

func TestHeartbeatCloneConflict(t *testing.T) {
	d := open(t)
	token := pair(t, d, "px-2222bbbb")

	// Two boxes that run from one card answer inside the clone window.
	hb := manifest.Heartbeat{DeviceID: "px-2222bbbb", HardwareID: "hw-clone", Version: "1.0.0"}
	if err := d.Heartbeat("px-2222bbbb", hb, "10.0.0.6"); err != nil {
		t.Fatal(err)
	}
	dev, err := d.Device("px-2222bbbb")
	if err != nil {
		t.Fatal(err)
	}
	if !dev.Conflict {
		t.Fatal("two hardware IDs inside the window did not make a conflict")
	}
	if dev.ConflictHardwareID != "hw-clone" {
		t.Fatalf("the conflict names %q", dev.ConflictHardwareID)
	}
	if dev.HardwareID != "hw-px-2222bbbb" {
		t.Fatal("the conflict changed the hardware ID of the row")
	}
	if dev.NeedsConfirm {
		t.Fatal("a clone was read as a repair")
	}

	// The resolve revokes the token. Both boxes must pair again.
	if err := d.ResolveConflict(dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DeviceByToken(token); err != ErrNotFound {
		t.Fatalf("the token still works after the conflict was resolved: %v", err)
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
	if len(list) != 1 || list[0].State != "delivered" {
		t.Fatalf("the state is %+v", list)
	}

	hb := manifest.Heartbeat{DeviceID: "px-3333cccc", HardwareID: "hw-px-3333cccc", Acks: []int64{id}}
	if err := d.Heartbeat("px-3333cccc", hb, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if list, _ = d.Commands("px-3333cccc", 10); list[0].State != "acked" {
		t.Fatalf("the state after the ack is %+v", list)
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
	hb := manifest.Heartbeat{DeviceID: "px-5555eeee", HardwareID: "hw-px-5555eeee", Acks: []int64{id}}
	if err := d.Heartbeat("px-5555eeee", hb, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	list, err := d.Commands("px-4444dddd", 10)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].State == "acked" {
		t.Fatal("one device acknowledged the command of another device")
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
