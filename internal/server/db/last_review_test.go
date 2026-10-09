package db

import "testing"

// TestARejectKeepsTheRowOfAnApprovedRequest covers two requests for one device ID.
//
// Two machines asked to be px-twins01. The admin approved the first, which made the
// device row, and then rejected the second. The reject deleted that row, because it
// was never paired. The first machine then collected a device token that no row held,
// and its first poll told it to drop the pairing.
func TestARejectKeepsTheRowOfAnApprovedRequest(t *testing.T) {
	d := open(t)

	first, err := d.Enroll(enrollReq("px-twins01", "hw-first", ""), "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Enroll(enrollReq("px-twins01", "hw-second", ""), "10.0.0.6")
	if err != nil {
		t.Fatal(err)
	}
	approve, err := d.PendingByCode(first.PairingCode)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApprovePending(approve.ID, 0); err != nil {
		t.Fatal(err)
	}
	reject, err := d.PendingByCode(second.PairingCode)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RejectPending(reject.ID); err != nil {
		t.Fatal(err)
	}

	got, err := d.Enroll(enrollReq("px-twins01", "hw-first", first.ClaimSecret), "10.0.0.5")
	if err != nil {
		t.Fatalf("the approved machine could not collect its token: %v", err)
	}
	if got.Status != "paired" || got.DeviceToken == "" {
		t.Fatalf("the approved machine got %+v", got)
	}
	if _, err := d.DeviceByToken(got.DeviceToken); err != nil {
		t.Fatalf("the token that the device got is not in the table: %v", err)
	}
}

// TestARejectOfTheLastRequestRemovesTheNewRow keeps the old half of the rule: a
// device row that never paired goes with the last request that named it.
func TestARejectOfTheLastRequestRemovesTheNewRow(t *testing.T) {
	d := open(t)

	res, err := d.Enroll(enrollReq("px-alone001", "hw-alone", ""), "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.PendingByCode(res.PairingCode)
	if err != nil {
		t.Fatal(err)
	}
	// The approval makes the device row. The row has no token yet.
	if err := d.ApprovePending(p.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Device("px-alone001"); err != nil {
		t.Fatalf("the approval made no device row: %v", err)
	}
	if err := d.RejectPending(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Device("px-alone001"); err != ErrNotFound {
		t.Fatalf("the device row of a rejected request stayed: %v", err)
	}
}
