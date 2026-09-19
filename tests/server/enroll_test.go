package server_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// enroll calls POST /api/v1/enroll.
func (f *fleet) enroll(id, token string) (manifest.EnrollResponse, reply) {
	f.t.Helper()
	res := f.call(http.MethodPost, "/api/v1/enroll", manifest.EnrollRequest{
		DeviceID: id, HardwareID: "hw-" + id, Name: id, Token: token, Version: "1.4.2",
	}, nil)
	var out manifest.EnrollResponse
	if res.status == http.StatusOK {
		res.json(f.t, &out)
	}
	return out, res
}

// makeToken makes an enrollment token through the admin API and gives its value.
func (f *fleet) makeToken(mode string, maxUses int, expiresAt string) string {
	f.t.Helper()
	body := map[string]any{"name": "test " + mode, "mode": mode, "max_uses": maxUses}
	if expiresAt != "" {
		body["expires_at"] = expiresAt
	}
	res := f.mustOK(f.adminCall(http.MethodPost, "/api/admin/tokens", body), "make a token")

	var out struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
		TOML  string `json:"toml"`
	}
	res.json(f.t, &out)
	if out.Token == "" || out.TOML == "" {
		f.t.Fatalf("the token answer is %s", res.body)
	}
	return out.Token
}

func TestEnrollWithAnAutoToken(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.makeToken("auto", 0, "")

	out, res := f.enroll("px-auto0001", token)
	f.mustOK(res, "enroll")
	if out.Status != "paired" || out.DeviceToken == "" {
		t.Fatalf("the answer is %+v", out)
	}

	// The token works on the manifest route at once.
	f.mustOK(f.device(http.MethodGet, "/api/v1/manifest", out.DeviceToken, nil), "manifest")
}

func TestEnrollWithAPendingToken(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.makeToken("pending", 0, "")

	out, res := f.enroll("px-pend0001", token)
	f.mustOK(res, "enroll")
	if out.Status != "pending" || out.ClaimSecret == "" || len(out.PairingCode) != 6 {
		t.Fatalf("the answer is %+v", out)
	}
	if out.DeviceToken != "" {
		t.Fatal("a pending device got a token")
	}

	// A poll with the claim secret still says pending.
	poll, res := f.enroll("px-pend0001", out.ClaimSecret)
	f.mustOK(res, "the poll")
	if poll.Status != "pending" || poll.PairingCode != out.PairingCode {
		t.Fatalf("the poll gave %+v", poll)
	}

	// The admin approves by the code on the screen.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/pending/"+out.PairingCode+"/approve",
		map[string]any{}), "approve")

	done, res := f.enroll("px-pend0001", out.ClaimSecret)
	f.mustOK(res, "the poll after the approval")
	if done.Status != "paired" || done.DeviceToken == "" {
		t.Fatalf("the answer is %+v", done)
	}
	f.mustOK(f.device(http.MethodGet, "/api/v1/manifest", done.DeviceToken, nil), "manifest")
}

func TestEnrollByCode(t *testing.T) {
	f := newFleet(t)
	f.login()

	// No token at all: the device shows a code and waits.
	out, res := f.enroll("px-code0001", "")
	f.mustOK(res, "enroll")
	if out.Status != "pending" || out.PairingCode == "" || out.ClaimSecret == "" {
		t.Fatalf("the answer is %+v", out)
	}

	// The device is in the list with its code.
	list := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/devices", nil), "the device list")
	var view struct {
		Devices []struct {
			ID          string `json:"id"`
			State       string `json:"state"`
			PendingCode string `json:"pending_code"`
		} `json:"devices"`
		Totals struct {
			NeedsALook int `json:"needs_a_look"`
		} `json:"totals"`
	}
	list.json(t, &view)
	if len(view.Devices) != 1 || view.Devices[0].State != "pending" ||
		view.Devices[0].PendingCode != out.PairingCode {
		t.Fatalf("the list is %+v", view)
	}
	if view.Totals.NeedsALook != 1 {
		t.Fatalf("the totals say %d need a look", view.Totals.NeedsALook)
	}

	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/px-code0001/approve",
		map[string]any{}), "approve")

	done, res := f.enroll("px-code0001", out.ClaimSecret)
	f.mustOK(res, "the poll after the approval")
	if done.Status != "paired" || done.DeviceToken == "" {
		t.Fatalf("the answer is %+v", done)
	}
}

func TestEnrollRejectByCode(t *testing.T) {
	f := newFleet(t)
	f.login()
	out, _ := f.enroll("px-rej00001", "")

	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/pending/"+out.PairingCode+"/reject", nil), "reject")

	// The row is gone, because the device never paired.
	res := f.adminCall(http.MethodGet, "/api/admin/devices/px-rej00001", nil)
	if res.status != http.StatusNotFound {
		t.Fatalf("the rejected device answered %d: %s", res.status, res.body)
	}
}

func TestEnrollTokenMaxUses(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.makeToken("auto", 2, "")

	for i := 1; i <= 2; i++ {
		out, res := f.enroll(fmt.Sprintf("px-uses000%d", i), token)
		f.mustOK(res, "enroll")
		if out.Status != "paired" {
			t.Fatalf("use %d gave %+v", i, out)
		}
	}
	_, res := f.enroll("px-uses0003", token)
	if res.status != http.StatusUnauthorized {
		t.Fatalf("the third use answered %d: %s", res.status, res.body)
	}

	// The list shows the count.
	list := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/tokens", nil), "the token list")
	var view struct {
		Tokens []struct {
			Uses    int  `json:"uses"`
			MaxUses int  `json:"max_uses"`
			Revoked bool `json:"revoked"`
		} `json:"tokens"`
	}
	list.json(t, &view)
	if len(view.Tokens) != 1 || view.Tokens[0].Uses < 2 || view.Tokens[0].MaxUses != 2 {
		t.Fatalf("the token list is %+v", view.Tokens)
	}
}

func TestEnrollTokenExpiry(t *testing.T) {
	f := newFleet(t)
	f.login()
	// A time in the past is refused at creation.
	res := f.adminCall(http.MethodPost, "/api/admin/tokens", map[string]any{
		"mode": "auto", "expires_at": "2000-01-01T00:00:00Z",
	})
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("a token that expired in the past answered %d: %s", res.status, res.body)
	}
	if fields := res.fields(t); len(fields) == 0 || fields[0].Field != "expires_at" {
		t.Fatalf("the fields are %+v", fields)
	}

	// A token that expires in an hour works now. The clock of the database then
	// moves past the expiry, and the same token stops working.
	expires := time.Now().Add(time.Hour).UTC()
	token := f.makeToken("auto", 0, expires.Format(time.RFC3339))
	if _, res := f.enroll("px-exp00001", token); res.status != http.StatusOK {
		t.Fatalf("the enroll before the expiry answered %d: %s", res.status, res.body)
	}
	f.db.SetClock(func() time.Time { return expires.Add(time.Minute) })
	if _, res := f.enroll("px-exp00002", token); res.status != http.StatusUnauthorized {
		t.Fatalf("the enroll after the expiry answered %d: %s", res.status, res.body)
	}
}

func TestEnrollTokenRevoke(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.makeToken("auto", 0, "")

	list := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/tokens", nil), "the token list")
	var view struct {
		Tokens []struct {
			ID     int64  `json:"id"`
			Prefix string `json:"prefix"`
		} `json:"tokens"`
	}
	list.json(t, &view)
	if view.Tokens[0].Prefix != token[:8] {
		t.Fatalf("the prefix is %q, want the first characters of the token", view.Tokens[0].Prefix)
	}

	// A device that paired before the revoke keeps its own token.
	out, _ := f.enroll("px-rev00001", token)
	f.mustOK(f.adminCall(http.MethodPost,
		fmt.Sprintf("/api/admin/tokens/%d/revoke", view.Tokens[0].ID), nil), "revoke")

	if _, res := f.enroll("px-rev00002", token); res.status != http.StatusUnauthorized {
		t.Fatalf("the enroll after the revoke answered %d", res.status)
	}
	f.mustOK(f.device(http.MethodGet, "/api/v1/manifest", out.DeviceToken, nil),
		"the manifest of a device that paired before the revoke")
}

func TestEnrollRateLimit(t *testing.T) {
	f := newFleet(t)

	// Five bad attempts a minute from one address, then no more. The limiter
	// counts by address, and every attempt here comes from the loopback.
	var limited bool
	for i := 0; i < 12; i++ {
		_, res := f.enroll(fmt.Sprintf("px-rate000%d", i%10), "a token that nobody gave out")
		switch res.status {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			limited = true
		default:
			t.Fatalf("attempt %d answered %d: %s", i, res.status, res.body)
		}
	}
	if !limited {
		t.Fatal("twelve bad enroll attempts were never rate limited")
	}
}

func TestEnrollBadDeviceID(t *testing.T) {
	f := newFleet(t)
	_, res := f.enroll("px-../../etc", "")
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("a bad device ID answered %d: %s", res.status, res.body)
	}
	fields := res.fields(t)
	if len(fields) != 1 || fields[0].Field != "device_id" {
		t.Fatalf("the fields are %+v", fields)
	}
}

func TestEnrollBody(t *testing.T) {
	f := newFleet(t)
	res := f.call(http.MethodPost, "/api/v1/enroll", nil, func(r *http.Request) {
		r.Body = http.NoBody
	})
	if res.status != http.StatusBadRequest {
		t.Fatalf("an empty body answered %d: %s", res.status, res.body)
	}
	if res.errorText(t) == "" {
		t.Fatalf("the answer holds no error text: %s", res.body)
	}
}
