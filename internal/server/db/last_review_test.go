package db

import (
	"errors"
	"testing"
	"time"
)

// TestARejectKeepsTheRowOfAnApprovedRequest covers two requests for one device ID.
//
// Two machines asked to be px-twins01. The admin approved the first, which made the
// device row, and then rejected the second. The reject deleted that row, because it
// was never paired. The first machine then collected a device token that no row held,
// and its first poll told it to drop the pairing.
func TestARejectKeepsTheRowOfAnApprovedRequest(t *testing.T) {
	d := open(t)

	first, err := d.Enroll(enrollReq("px-twins01", "hw-first", ""), "10.0.0.5", true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Enroll(enrollReq("px-twins01", "hw-second", ""), "10.0.0.6", true)
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

	got, err := d.Enroll(enrollReq("px-twins01", "hw-first", first.ClaimSecret), "10.0.0.5", true)
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

	res, err := d.Enroll(enrollReq("px-alone001", "hw-alone", ""), "10.0.0.5", true)
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

// TestARequestWithNoPlaceInTheCountMakesNoRow covers the limit of R5 in the rules.
//
// Only the rules know if a request makes a new pending row. With no place in the
// count, a request that would make one writes nothing. A request that makes no new
// row still works: a pairing with an auto token and the poll of a request that
// waits.
func TestARequestWithNoPlaceInTheCountMakesNoRow(t *testing.T) {
	d := open(t)
	_, auto, err := d.CreateEnrollToken("auto", "auto", 0, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, slow, err := d.CreateEnrollToken("slow", "pending", 0, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.Enroll(enrollReq("px-full0001", "hw-full1", slow), "10.0.0.5", false); !errors.Is(err, ErrQueueLimited) {
		t.Fatalf("a pending-mode token with no place answered %v, want ErrQueueLimited", err)
	}
	if _, err := d.Enroll(enrollReq("px-full0002", "hw-full2", ""), "10.0.0.5", false); !errors.Is(err, ErrQueueLimited) {
		t.Fatalf("a request with no token and no place answered %v, want ErrQueueLimited", err)
	}
	if n, err := d.CountPending(); err != nil || n != 0 {
		t.Fatalf("the refused requests left %d rows (%v)", n, err)
	}

	// An auto token pairs a new device ID and makes no pending row.
	paired, err := d.Enroll(enrollReq("px-full0004", "hw-full4", auto), "10.0.0.5", false)
	if err != nil || paired.Status != "paired" {
		t.Fatalf("an auto token with no place answered %+v, %v", paired, err)
	}

	// A request that waits already keeps its poll.
	waiting, err := d.Enroll(enrollReq("px-full0003", "hw-full3", ""), "10.0.0.5", true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := d.Enroll(enrollReq("px-full0003", "hw-full3", waiting.ClaimSecret), "10.0.0.5", false)
	if err != nil || again.Status != "pending" {
		t.Fatalf("the poll of a waiting request answered %+v, %v", again, err)
	}
}
