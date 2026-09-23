package db

import (
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// enrollReq makes one enroll request of a test screen.
func enrollReq(id, hardware, token string) manifest.EnrollRequest {
	return manifest.EnrollRequest{
		DeviceID: id, HardwareID: hardware, Name: id, Token: token, Version: "1.4.2",
	}
}

// TestThePendingCapCountsOnlyTheRequestsThatWait covers the cap that stops an open
// route from filling a table.
//
// The count used to hold every row. A row that the admin approved stays until its
// device comes back for the token, and no sweep removes it, so 200 screens that never
// came back would have stopped enrollment for the whole fleet for ever, with an empty
// pending list on the page.
func TestThePendingCapCountsOnlyTheRequestsThatWait(t *testing.T) {
	d := open(t)

	// Fill the table with rows that the admin approved and that nobody collected.
	for i := 0; i < maxPending; i++ {
		id := "px-appro" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		res, err := d.Enroll(enrollReq(id, id+"-hw", ""), "10.0.0.5")
		if err != nil {
			t.Fatalf("the enroll of %s failed: %v", id, err)
		}
		p, err := d.PendingByCode(res.PairingCode)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.ApprovePending(p.ID, 0); err != nil {
			t.Fatal(err)
		}
	}

	// A new screen still gets in.
	if _, err := d.Enroll(enrollReq("px-newone1", "px-newone1-hw", ""), "10.0.0.6"); err != nil {
		t.Fatalf("a new screen was refused although every row waits for its own device: %v", err)
	}
}

// TestAnApprovedRequestDoesNotWaitForEver covers the life of a claim secret.
//
// The claim secret of an approved row buys a device token from any machine that holds
// it. Such a row used to stay in the table for the life of the installation, so the
// secret never expired and no admin route could see it or cancel it.
func TestAnApprovedRequestDoesNotWaitForEver(t *testing.T) {
	d := open(t)
	res, err := d.Enroll(enrollReq("px-oldclaim", "px-oldclaim-hw", ""), "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.PendingByCode(res.PairingCode)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApprovePending(p.ID, 0); err != nil {
		t.Fatal(err)
	}

	// The device comes back with its secret long after the approval.
	d.SetClock(func() time.Time { return time.Now().Add(approvedLife + time.Hour) })
	got, err := d.Enroll(enrollReq("px-oldclaim", "px-oldclaim-hw", res.ClaimSecret), "10.0.0.9")
	if err == nil && got.DeviceToken != "" {
		t.Fatal("a claim secret of an approval that is a month old still bought a device token")
	}
}

// TestAStalePendingHardwareChangeGoesAway covers the hardware-swap card.
//
// A card that moved to another box and back used to leave needs_confirm set for ever,
// with the hardware ID of the box that is not there. One click of the admin would then
// have stored that ID, and the real box would look like a change at its next
// heartbeat.
func TestAStalePendingHardwareChangeGoesAway(t *testing.T) {
	d := open(t)
	base := time.Now()
	d.SetClock(func() time.Time { return base })

	id := "px-swap0001"
	token := d.mustPair(t, id, "box-a")

	// The card reports box B: a repair that waits for the admin.
	d.mustBeat(t, id, "box-b")
	dev, err := d.Device(id)
	if err != nil {
		t.Fatal(err)
	}
	if !dev.NeedsConfirm || dev.PrevHardwareID == "" {
		t.Fatalf("the repair was not recorded: %+v", dev)
	}

	// Much later the card is back in box A. The clone window has passed, so this is
	// not two machines taking turns.
	d.SetClock(func() time.Time { return base.Add(2 * cloneWindow) })
	d.mustBeat(t, id, "box-a")
	dev, err = d.Device(id)
	if err != nil {
		t.Fatal(err)
	}
	if dev.NeedsConfirm || dev.PrevHardwareID != "" {
		t.Fatalf("the swap card is still on the screen: needs_confirm=%v prev=%q",
			dev.NeedsConfirm, dev.PrevHardwareID)
	}
	if dev.HardwareID != "box-a" {
		t.Fatalf("the stored hardware ID is %q, want box-a", dev.HardwareID)
	}
	_ = token
}

// TestAConflictInsideTheWindowIsStillAConflict keeps the fix above from hiding the
// clone case: two machines that take turns quickly are two machines.
func TestAConflictInsideTheWindowIsStillAConflict(t *testing.T) {
	d := open(t)
	base := time.Now()
	d.SetClock(func() time.Time { return base })

	id := "px-clone001"
	d.mustPair(t, id, "box-a")
	d.mustBeat(t, id, "box-b")
	d.SetClock(func() time.Time { return base.Add(cloneWindow / 2) })
	d.mustBeat(t, id, "box-a")

	dev, err := d.Device(id)
	if err != nil {
		t.Fatal(err)
	}
	if !dev.Conflict {
		t.Fatalf("two machines that took turns are not a conflict: %+v", dev)
	}
}

// TestANeverPairedRowTakesTheGroupOfItsToken covers the group rule of an approval.
// A row that was ever paired keeps the group that the admin gave it.
func TestANeverPairedRowTakesTheGroupOfItsToken(t *testing.T) {
	d := open(t)
	group, err := d.CreateGroup("Warehouse")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := d.CreateEnrollToken("the first batch", "pending", group, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}

	res, err := d.Enroll(enrollReq("px-group001", "px-group001-hw", token), "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "pending" {
		t.Fatalf("a pending token paired at once: %+v", res)
	}
	p, err := d.PendingByCode(res.PairingCode)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApprovePending(p.ID, 0); err != nil {
		t.Fatal(err)
	}
	dev, err := d.Device("px-group001")
	if err != nil {
		t.Fatal(err)
	}
	if dev.GroupID != group {
		t.Fatalf("the new screen landed in group %d, want %d", dev.GroupID, group)
	}
}

// TestACommandIDIsNeverHandedOutTwice covers the ID that the device remembers.
//
// The device keeps the last 100 command IDs and does not run one that it already ran.
// A plain SQLite rowid comes back after the rows of a deleted screen go away, so a
// screen that paired again used to acknowledge its first new command without running
// it, and the admin UI showed "acked".
func TestACommandIDIsNeverHandedOutTwice(t *testing.T) {
	d := open(t)
	d.mustPair(t, "px-cmdid001", "box-a")

	var first int64
	for i := 0; i < 3; i++ {
		id, err := d.QueueCommand("px-cmdid001", "rescan", nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = id
		}
	}
	if err := d.DeleteDevice("px-cmdid001"); err != nil {
		t.Fatal(err)
	}
	d.mustPair(t, "px-cmdid001", "box-a")

	next, err := d.QueueCommand("px-cmdid001", "reboot", nil)
	if err != nil {
		t.Fatal(err)
	}
	if next <= first+2 {
		t.Fatalf("the command ID came back: the new one is %d and the old ones ended at %d",
			next, first+2)
	}
}

// TestAQueuedCommandDoesNotWaitForEver covers the age of an order.
//
// A screen that is off for a day and comes back must not run a stack of orders from
// yesterday. Nothing expired a command that never went out, and nothing removed an
// answered row at all.
func TestAQueuedCommandDoesNotWaitForEver(t *testing.T) {
	d := open(t)
	base := time.Now()
	d.SetClock(func() time.Time { return base })
	d.mustPair(t, "px-oldcmd01", "box-a")
	if _, err := d.QueueCommand("px-oldcmd01", "reboot", nil); err != nil {
		t.Fatal(err)
	}

	// Two days later the screen comes back.
	d.SetClock(func() time.Time { return base.Add(2 * commandLife) })
	got, err := d.TakeCommands("px-oldcmd01")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a command of two days ago went out: %+v", got)
	}
	list, err := d.Commands("px-oldcmd01", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].State != CommandExpired {
		t.Fatalf("the command is %+v, want the state %q", list, CommandExpired)
	}
}

// TestSweepCommandsTakesTheOldRowsAway covers the growth of the table. Nothing
// removed an answered or expired row before.
func TestSweepCommandsTakesTheOldRowsAway(t *testing.T) {
	d := open(t)
	base := time.Now()
	d.SetClock(func() time.Time { return base })
	d.mustPair(t, "px-purge001", "box-a")
	if _, err := d.QueueCommand("px-purge001", "rescan", nil); err != nil {
		t.Fatal(err)
	}

	d.SetClock(func() time.Time { return base.Add(commandKeep + 24*time.Hour) })
	if err := d.SweepCommands(); err != nil {
		t.Fatal(err)
	}
	list, err := d.Commands("px-purge001", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("the table still holds %d rows that are a month old", len(list))
	}
}

// TestTheManifestNeverGoesBelowThePollFloor keeps the two ends in step. The device
// repairs anything under MinPollSeconds to its own default, so a smaller number would
// be a number that the screen ignores while the admin page printed it.
func TestTheManifestNeverGoesBelowThePollFloor(t *testing.T) {
	d := open(t)
	d.mustPair(t, "px-floor001", "box-a")
	// A hand-edited row. No route writes this column today.
	if _, err := d.w.Exec(`UPDATE devices SET poll_seconds = 3 WHERE id = ?`, "px-floor001"); err != nil {
		t.Fatal(err)
	}
	dev, err := d.Device("px-floor001")
	if err != nil {
		t.Fatal(err)
	}
	m, err := d.Manifest(dev, ManifestOptions{ServerName: "Test", DefaultPoll: 60})
	if err != nil {
		t.Fatal(err)
	}
	if m.PollSeconds != MinPollSeconds {
		t.Fatalf("the manifest says %d seconds, want the floor of %d", m.PollSeconds, MinPollSeconds)
	}
}

// TestDevicesGroupIsIndexed keeps the poll of a device off a full table scan. The
// group of a device is read on every poll, and the column is a foreign key that five
// statements filter on.
func TestDevicesGroupIsIndexed(t *testing.T) {
	d := open(t)
	for _, want := range []string{"devices_group", "devices_playlist", "pending_token"} {
		var n int
		if err := d.r.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, want).
			Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("the schema holds no index %s", want)
		}
	}
}

// mustPair pairs one test screen with an auto enrollment token and gives its token.
func (d *DB) mustPair(t *testing.T, id, hardware string) string {
	t.Helper()
	_, token, err := d.CreateEnrollToken("auto "+id, "auto", 0, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	res, err2 := d.Enroll(enrollReq(id, hardware, token), "10.0.0.5")
	if err2 != nil {
		t.Fatal(err2)
	}
	if res.DeviceToken == "" {
		t.Fatalf("%s got no device token: %+v", id, res)
	}
	return res.DeviceToken
}

// mustBeat sends one heartbeat with a hardware ID.
func (d *DB) mustBeat(t *testing.T, id, hardware string) {
	t.Helper()
	hb := manifest.Heartbeat{DeviceID: id, HardwareID: hardware, Name: id, Version: "1.4.2"}
	if _, err := d.Heartbeat(id, hb, "10.0.0.5"); err != nil {
		t.Fatal(err)
	}
}
