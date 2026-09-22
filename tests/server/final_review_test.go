package server_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/server/db"
)

// enrollAs sends one enroll request with a hardware ID of its own, so a test can act
// as a second machine that names the device ID of a screen.
func (f *fleet) enrollAs(id, hardware, token string) (manifest.EnrollResponse, reply) {
	f.t.Helper()
	res := f.call(http.MethodPost, "/api/v1/enroll", manifest.EnrollRequest{
		DeviceID: id, HardwareID: hardware, Name: id, Token: token, Version: "1.4.2",
	}, nil)
	var out manifest.EnrollResponse
	if res.status == http.StatusOK {
		res.json(f.t, &out)
	}
	return out, res
}

// TestAWaitingEnrollmentIsNotOpenToAnybodyWhoKnowsTheDeviceID is the takeover that
// the pending table was made to stop.
//
// The fallback screen shows the device ID beside the pairing code, so the ID is not a
// secret. A caller that only named that ID used to take the row over: the server
// wrote a NEW claim secret into it, gave the caller that secret and the code that the
// admin reads, and left the true screen with a secret that matched nothing. The admin
// then approved a card that looked right and the caller collected the device token.
func TestAWaitingEnrollmentIsNotOpenToAnybodyWhoKnowsTheDeviceID(t *testing.T) {
	f := newFleet(t)

	// The screen on the wall asks for approval.
	screen, res := f.enrollAs("px-victim01", hardwareOf("px-victim01"), "")
	f.mustOK(res, "the enroll of the screen")
	if screen.ClaimSecret == "" || screen.PairingCode == "" {
		t.Fatalf("the screen got no claim secret and no code: %s", res.body)
	}

	// A second machine names the same device ID.
	other, res := f.enrollAs("px-victim01", hardwareOf("px-other001"), "")
	f.mustOK(res, "the enroll of the other machine")
	if other.ClaimSecret == screen.ClaimSecret {
		t.Fatal("the two machines hold one claim secret")
	}
	if other.PairingCode == screen.PairingCode {
		t.Fatalf("the other machine took the pairing code of the screen: %q", other.PairingCode)
	}

	// The secret of the screen still works. Before the fix it answered 401 with the
	// code token-revoked, which makes a device drop its pairing and start again.
	back, res := f.enrollAs("px-victim01", hardwareOf("px-victim01"), screen.ClaimSecret)
	f.mustOK(res, "the poll of the screen")
	if back.Status != "pending" || back.PairingCode != screen.PairingCode {
		t.Fatalf("the screen lost its place in the list: %+v", back)
	}

	// The admin approves the request of the SCREEN, by the code that the screen shows.
	f.login()
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/pending/"+screen.PairingCode+"/approve", nil),
		"approve the screen")

	// The other machine gets no token from that approval.
	stolen, res := f.enrollAs("px-victim01", hardwareOf("px-other001"), other.ClaimSecret)
	if res.status == http.StatusOK && stolen.DeviceToken != "" {
		t.Fatal("the other machine collected the device token of the screen")
	}
	paired, res := f.enrollAs("px-victim01", hardwareOf("px-victim01"), screen.ClaimSecret)
	f.mustOK(res, "the screen collects its token")
	if paired.Status != "paired" || paired.DeviceToken == "" {
		t.Fatalf("the screen got no token: %+v", paired)
	}
}

// TestAnEnrollWithNoTokenDoesNotClearTheWrongTokenCount covers the one rate limit of
// the only open write route of the server.
//
// A request with no token shows no secret, so it must not clear the count of wrong
// tokens. It used to: the answer of a request that found an existing row carried
// Created == false, and the handler read that as "this caller proved something".
func TestAnEnrollWithNoTokenDoesNotClearTheWrongTokenCount(t *testing.T) {
	f := newFleet(t)

	// One good request makes the row that the replay finds again.
	if _, res := f.enrollAs("px-limiter1", hardwareOf("px-limiter1"), ""); res.status != http.StatusOK {
		t.Fatalf("the first enroll answered %d: %s", res.status, res.body)
	}

	// Five wrong tokens use up the budget of this address.
	for i := 0; i < 5; i++ {
		if _, res := f.enrollAs("px-limiter1", hardwareOf("px-limiter1"), "not-a-token"); res.status != http.StatusUnauthorized {
			t.Fatalf("attempt %d answered %d, want 401: %s", i+1, res.status, res.body)
		}
	}
	// The replay with no token must not give the budget back.
	f.enrollAs("px-limiter1", hardwareOf("px-limiter1"), "")
	if _, res := f.enrollAs("px-limiter1", hardwareOf("px-limiter1"), "another-wrong-token"); res.status != http.StatusTooManyRequests {
		t.Fatalf("the sixth wrong token answered %d, want 429: %s", res.status, res.body)
	}
}

// TestThePendingLimiterStopsTheWriteAndNotOnlyTheAnswer covers the cap on the route
// that an unauthenticated caller can repeat with a new device ID each time.
//
// The check used to run after db.Enroll had committed, so every request wrote its row
// and the limiter only held the answer back. The table filled up all the same.
func TestThePendingLimiterStopsTheWriteAndNotOnlyTheAnswer(t *testing.T) {
	f := newFleet(t)

	made := 0
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("px-flood%03d", i)
		_, res := f.enrollAs(id, hardwareOf(id), "")
		if res.status == http.StatusOK {
			made++
		}
	}
	if made == 10 {
		t.Fatal("ten new screens from one address all passed the limiter")
	}

	f.login()
	res := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/devices", nil), "the device list")
	var list struct {
		Devices []struct {
			Pending bool `json:"pending"`
		} `json:"devices"`
	}
	res.json(t, &list)
	rows := 0
	for _, d := range list.Devices {
		if d.Pending {
			rows++
		}
	}
	if rows > made {
		t.Fatalf("the table holds %d waiting rows and only %d requests were answered", rows, made)
	}
}

// TestABadDayNameIsRefusedAndNotWidenedToEveryDay covers the three routes that take a
// day list.
//
// An empty day list means "every day" on both ends of the wire. The routes used to
// throw an unknown word away, so a rule that the admin asked for on one day played on
// all seven and shadowed every rule below it, with a 200 and no word about why.
func TestABadDayNameIsRefusedAndNotWidenedToEveryDay(t *testing.T) {
	f := newFleet(t)
	f.login()
	group := f.makeGroup("Warehouse")
	playlist := f.makePlaylist("Safety loop",
		map[string]any{"url": "https://example.com/board", "duration": 30})
	device := "px-days0001"
	f.pairDevice(device)

	cases := []struct {
		name   string
		method string
		path   string
		body   map[string]any
		field  string
	}{
		{"a schedule rule", http.MethodPost, "/api/admin/assignments",
			map[string]any{"group_id": group, "playlist_id": playlist,
				"days": []string{"funday"}, "start": "08:00", "end": "18:00"}, "days"},
		{"the screen rule of a group", http.MethodPut, fmt.Sprintf("/api/admin/groups/%d", group),
			map[string]any{"name": "Warehouse", "default_playlist_id": 0,
				"screen_on": "07:00", "screen_off": "22:00",
				"screen_days": []string{"Monday"}}, "screen_days"},
		{"the screen rule of a screen", http.MethodPost, "/api/admin/devices/" + device + "/overrides",
			map[string]any{"default_playlist_id": 0, "screen_on": "07:00", "screen_off": "22:00",
				"screen_days": []string{"mon", "funday"}}, "screen_days"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			view := f.with(t)
			res := view.adminCall(c.method, c.path, c.body)
			if res.status != http.StatusUnprocessableEntity {
				t.Fatalf("%s %s answered %d, want 422: %s", c.method, c.path, res.status, res.body)
			}
			fields := res.fields(t)
			if len(fields) == 0 || fields[0].Field != c.field {
				t.Fatalf("the field errors are %+v, want one on %q", fields, c.field)
			}
		})
	}

	// A good list still works and keeps exactly the days that it named.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/assignments", map[string]any{
		"group_id": group, "playlist_id": playlist,
		"days": []string{"mon"}, "start": "08:00", "end": "18:00",
	}), "a rule with a good day")
}

// TestAGroupWithTimeRulesIsNotDeletedSilently covers the cascade of
// assignments.group_id. One click used to take away every time rule of the group with
// no answer that said so.
func TestAGroupWithTimeRulesIsNotDeletedSilently(t *testing.T) {
	f := newFleet(t)
	f.login()
	group := f.makeGroup("Warehouse")
	playlist := f.makePlaylist("Safety loop",
		map[string]any{"url": "https://example.com/board", "duration": 30})
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/assignments", map[string]any{
		"group_id": group, "playlist_id": playlist, "days": []string{"mon"},
		"start": "08:00", "end": "18:00",
	}), "a rule of the group")

	res := f.adminCall(http.MethodDelete, fmt.Sprintf("/api/admin/groups/%d", group), nil)
	if res.status != http.StatusConflict {
		t.Fatalf("the delete answered %d, want 409: %s", res.status, res.body)
	}
	// The rule is still there.
	res = f.mustOK(f.adminCall(http.MethodGet,
		fmt.Sprintf("/api/admin/assignments?group_id=%d", group), nil), "the rules of the group")
	var rules struct {
		Assignments []db.Assignment `json:"assignments"`
	}
	res.json(t, &rules)
	if len(rules.Assignments) != 1 {
		t.Fatalf("the group holds %d rules after the refused delete, want 1", len(rules.Assignments))
	}
}

// TestAGroupWithNoNameAnswers422OnTheNameField keeps the answer off the text of an
// error. The route used to compare the message of the error with a sentence, so one
// better sentence would have turned this into a 500.
func TestAGroupWithNoNameAnswers422OnTheNameField(t *testing.T) {
	f := newFleet(t)
	f.login()
	group := f.makeGroup("Warehouse")

	for _, c := range []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{"create", http.MethodPost, "/api/admin/groups", map[string]any{"name": "   "}},
		{"update", http.MethodPut, fmt.Sprintf("/api/admin/groups/%d", group),
			map[string]any{"name": "", "default_playlist_id": 0,
				"screen_on": "", "screen_off": "", "screen_days": []string{}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			view := f.with(t)
			res := view.adminCall(c.method, c.path, c.body)
			if res.status != http.StatusUnprocessableEntity {
				t.Fatalf("%s answered %d, want 422: %s", c.name, res.status, res.body)
			}
			fields := res.fields(t)
			if len(fields) == 0 || fields[0].Field != "name" {
				t.Fatalf("the field errors are %+v, want one on name", fields)
			}
		})
	}
}

// TestPublicURLTakesOnlyAnAddressThatADeviceAccepts covers the one field where the
// server hands an address to a device: the token page pastes it into the [server]
// block of portapixel.toml, and a card that gets a bad one refuses its own
// configuration at first boot.
func TestPublicURLTakesOnlyAnAddressThatADeviceAccepts(t *testing.T) {
	f := newFleet(t)
	f.login()

	bad := []string{
		"https://signage.example.com/?x=1",
		"https://signage.example.com/signage#fleet",
		"https://admin:secret@signage.example.com",
		"https://",
		"ftp://signage.example.com",
	}
	for _, value := range bad {
		res := f.adminCall(http.MethodPut, "/api/admin/settings", map[string]any{
			"server_name": "Test fleet", "default_poll_seconds": 60, "public_url": value,
		})
		if res.status != http.StatusUnprocessableEntity {
			t.Fatalf("public_url %q answered %d, want 422: %s", value, res.status, res.body)
		}
		if fields := res.fields(t); len(fields) == 0 || fields[0].Field != "public_url" {
			t.Fatalf("public_url %q gave the fields %+v", value, fields)
		}
	}

	// A good value comes back with no slash at its end, so the trailing slash is cut
	// once here and not on every card.
	res := f.mustOK(f.adminCall(http.MethodPut, "/api/admin/settings", map[string]any{
		"server_name": "Test fleet", "default_poll_seconds": 60,
		"public_url": "https://signage.example.com/signage/",
	}), "a good public URL")
	var out struct {
		Settings struct {
			PublicURL string `json:"public_url"`
		} `json:"settings"`
	}
	res.json(t, &out)
	if out.Settings.PublicURL != "https://signage.example.com/signage" {
		t.Fatalf("the stored public URL is %q", out.Settings.PublicURL)
	}
}

// TestEveryAnswerCarriesTheBrowserRules covers the headers of the one middleware of
// internal/server. A handler cannot leave them out, so one test covers every route.
func TestEveryAnswerCarriesTheBrowserRules(t *testing.T) {
	f := newFleet(t)
	f.login()

	for _, path := range []string{"/", "/api/admin/session", "/api/admin/devices", "/licenses"} {
		res := f.call(http.MethodGet, path, nil, nil)
		if got := res.header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s carries X-Content-Type-Options %q", path, got)
		}
		if got := res.header.Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s carries Referrer-Policy %q", path, got)
		}
		if got := res.header.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s carries X-Frame-Options %q", path, got)
		}
		csp := res.header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s carries the policy %q", path, csp)
		}
		// A plain HTTP answer must not ask the browser to refuse plain HTTP for a year.
		if got := res.header.Get("Strict-Transport-Security"); got != "" {
			t.Errorf("%s carries Strict-Transport-Security %q over plain HTTP", path, got)
		}
	}
}

// TestAStoredObjectIsInert is the answer to an uploaded SVG.
//
// An SVG can carry a script, and internal/server/media gives it the type
// image/svg+xml. An admin who opened such an object on this origin would run that
// script with the admin session, which is the whole fleet. The policy of the answer
// puts it in a sandbox with no script, and the disposition makes it a download. An
// image in an <img> tag still draws: a policy of a response does not reach an image
// load.
func TestAStoredObjectIsInert(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.pairDevice("px-inert001")

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8">` +
		`<script>fetch("/api/admin/devices")</script></svg>`)
	sha := f.uploadMedia("logo.svg", svg)
	png := f.uploadMedia("welcome.png", imageBytes(t, 20, 20))

	type check struct {
		name   string
		path   string
		token  string
		header func(*http.Request)
		want   int
		attach bool
	}
	cases := []check{
		{name: "the svg object", path: "/api/v1/media/" + sha, token: token,
			want: http.StatusOK, attach: true},
		{name: "a range of the svg object", path: "/api/v1/media/" + sha, token: token,
			header: func(r *http.Request) { r.Header.Set("Range", "bytes=0-3") },
			want:   http.StatusPartialContent, attach: true},
		{name: "the svg object again", path: "/api/v1/media/" + sha, token: token,
			header: func(r *http.Request) { r.Header.Set("If-None-Match", `"`+sha+`"`) },
			want:   http.StatusNotModified, attach: true},
		{name: "the picture", path: "/api/v1/media/" + png, token: token,
			want: http.StatusOK, attach: false},
		{name: "the thumbnail", path: "/api/admin/media/" + png + "/thumb",
			want: http.StatusOK, attach: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			view := f.with(t)
			res := view.call(http.MethodGet, c.path, nil, func(r *http.Request) {
				if c.token != "" {
					r.Header.Set("Authorization", "Bearer "+c.token)
				}
				if c.header != nil {
					c.header(r)
				}
			})
			if res.status != c.want {
				t.Fatalf("%s answered %d, want %d: %s", c.path, res.status, c.want, res.body)
			}
			csp := res.header.Get("Content-Security-Policy")
			if !strings.HasPrefix(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
				t.Errorf("%s carries the policy %q", c.path, csp)
			}
			if got := res.header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("%s carries X-Content-Type-Options %q", c.path, got)
			}
			if got := res.header.Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
				t.Errorf("%s carries Cross-Origin-Resource-Policy %q", c.path, got)
			}
			attached := strings.HasPrefix(res.header.Get("Content-Disposition"), "attachment")
			if attached != c.attach {
				t.Errorf("%s carries the disposition %q, want attachment: %v",
					c.path, res.header.Get("Content-Disposition"), c.attach)
			}
		})
	}
}

// TestAnObjectTakesItsTypeFromTheRow keeps the type off the first bytes of the file.
// The name in the URL is a hash with no extension, so http.ServeContent would guess,
// and a file that somebody uploaded as a picture but that holds HTML would then be
// served as HTML.
func TestAnObjectTakesItsTypeFromTheRow(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.pairDevice("px-sniff001")

	sha := f.uploadMedia("poster.png", []byte("<html><body>not a picture</body></html>"))
	res := f.mustOK(f.device(http.MethodGet, "/api/v1/media/"+sha, token, nil), "the object")
	if got := res.header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("the object is served as %q; the row says image/png", got)
	}
}

// TestAnExpiredCommandBecomesExpiredWithNoPollOfItsDevice covers the state that the
// admin API names. The expiry used to run only inside the poll of the same device, so
// a screen that never came back left every order at "delivered" for ever.
func TestAnExpiredCommandBecomesExpiredWithNoPollOfItsDevice(t *testing.T) {
	f := newFleet(t)
	f.login()
	device := "px-expire01"
	token := f.pairDevice(device)

	res := f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/"+device+"/commands",
		map[string]any{"type": "rescan"}), "queue a command")
	var queued struct {
		ID int64 `json:"id"`
	}
	res.json(t, &queued)

	// The device takes it once and never answers.
	m := f.getManifest(token)
	if len(m.Commands) != 1 || m.Commands[0].ID != queued.ID {
		t.Fatalf("the manifest carries %+v", m.Commands)
	}

	// Two days later, with no poll of that device at all.
	f.db.SetClock(func() time.Time { return time.Now().Add(48 * time.Hour) })
	if err := f.db.SweepCommands(); err != nil {
		t.Fatal(err)
	}
	f.db.SetClock(time.Now)

	res = f.mustOK(f.adminCall(http.MethodGet, "/api/admin/devices/"+device+"/commands", nil),
		"the commands of the device")
	var list struct {
		Commands []db.Command `json:"commands"`
	}
	res.json(t, &list)
	if len(list.Commands) != 1 || list.Commands[0].State != db.CommandExpired {
		t.Fatalf("the command is %+v, want the state %q", list.Commands, db.CommandExpired)
	}
}
