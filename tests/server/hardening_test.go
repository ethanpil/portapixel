package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ethanpil/portapixel/internal/httpguard"
)

func TestAdminNeedsASession(t *testing.T) {
	f := newFleet(t)
	// No login. Every admin route must answer 401.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/devices"},
		{http.MethodGet, "/api/admin/groups"},
		{http.MethodGet, "/api/admin/playlists"},
		{http.MethodGet, "/api/admin/media"},
		{http.MethodGet, "/api/admin/tokens"},
		{http.MethodGet, "/api/admin/releases"},
		{http.MethodGet, "/api/admin/health"},
		{http.MethodGet, "/api/admin/settings"},
		{http.MethodPost, "/api/admin/groups"},
		{http.MethodPost, "/api/admin/tokens"},
		{http.MethodDelete, "/api/admin/devices/px-a"},
	} {
		res := f.call(c.method, c.path, nil, nil)
		if res.status != http.StatusUnauthorized {
			t.Errorf("%s %s with no session answered %d", c.method, c.path, res.status)
		}
		if res.errorText(t) == "" {
			t.Errorf("%s %s answered no error text: %s", c.method, c.path, res.body)
		}
	}
}

func TestLoginLogoutAndSession(t *testing.T) {
	f := newFleet(t)

	// The session route answers before a login, because "am I logged in" must
	// work when the answer is no.
	res := f.mustOK(f.call(http.MethodGet, "/api/admin/session", nil, nil), "the session")
	var view struct {
		LoggedIn   bool   `json:"logged_in"`
		ServerName string `json:"server_name"`
		Version    string `json:"version"`
	}
	res.json(t, &view)
	if view.LoggedIn {
		t.Fatal("the session says logged in before a login")
	}
	if view.ServerName != "Test fleet" || view.Version == "" {
		t.Fatalf("the session view is %+v", view)
	}

	// A wrong password gives 401.
	bad := f.call(http.MethodPost, "/api/admin/login", map[string]string{"password": "wrong"}, nil)
	if bad.status != http.StatusUnauthorized {
		t.Fatalf("a wrong password answered %d", bad.status)
	}

	f.login()
	res = f.mustOK(f.call(http.MethodGet, "/api/admin/session", nil, nil), "the session")
	res.json(t, &view)
	if !view.LoggedIn {
		t.Fatal("the session says logged out after a login")
	}
	f.mustOK(f.adminCall(http.MethodGet, "/api/admin/devices", nil), "the device list")

	f.mustOK(f.call(http.MethodPost, "/api/admin/logout", nil, nil), "logout")
	if res := f.call(http.MethodGet, "/api/admin/devices", nil, nil); res.status != http.StatusUnauthorized {
		t.Fatalf("the device list after the logout answered %d", res.status)
	}
}

func TestLoginRateLimit(t *testing.T) {
	f := newFleet(t)
	var limited bool
	for i := 0; i < 12; i++ {
		res := f.call(http.MethodPost, "/api/admin/login", map[string]string{"password": "wrong"}, nil)
		switch res.status {
		case http.StatusUnauthorized:
		case http.StatusTooManyRequests:
			limited = true
		default:
			t.Fatalf("attempt %d answered %d: %s", i, res.status, res.body)
		}
	}
	if !limited {
		t.Fatal("twelve wrong passwords were never rate limited")
	}
}

func TestAdminNeedsTheCSRFHeader(t *testing.T) {
	f := newFleet(t)
	f.login()

	// The same call without the header must be refused. A form on another site
	// cannot add a header, so this is the guard against request forgery (D46).
	res := f.call(http.MethodPost, "/api/admin/groups", map[string]string{"name": "Lobby"},
		func(r *http.Request) { r.Header.Del(httpguard.HeaderName) })
	if res.status != http.StatusForbidden {
		t.Fatalf("a POST with no %s header answered %d: %s", httpguard.HeaderName, res.status, res.body)
	}
	if res.errorText(t) == "" {
		t.Fatalf("the answer holds no error text: %s", res.body)
	}

	// A GET needs no header, because it changes nothing.
	f.mustOK(f.call(http.MethodGet, "/api/admin/groups", nil, nil), "the group list")

	// With the header the same call works.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/groups", map[string]string{"name": "Lobby"}),
		"the group")
}

func TestAdminHostAllowlist(t *testing.T) {
	f := newFleet(t)
	f.login()

	// A name that nobody configured must be refused, even with a good session.
	// That is the guard against DNS rebinding (D46).
	res := f.call(http.MethodGet, "/api/admin/devices", nil, func(r *http.Request) {
		r.Host = "evil.example.com"
	})
	if res.status != http.StatusMisdirectedRequest {
		t.Fatalf("a Host header of a name that nobody configured answered %d: %s", res.status, res.body)
	}
	if res.errorText(t) == "" {
		t.Fatalf("the answer holds no error text: %s", res.body)
	}

	// A name that is in the allowlist works.
	f.mustOK(f.call(http.MethodGet, "/api/admin/devices", nil, func(r *http.Request) {
		r.Host = "localhost"
	}), "the device list with a Host of localhost")
}

func TestDeviceAPITakesAnyHostAndNoCSRFHeader(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.pairDevice("px-host0001")

	// A device sends whatever Host the admin typed into its configuration, and it
	// carries a token and no cookie. So the two browser guards do not apply to
	// /api/v1, and a device with an address that the server does not know must
	// still work.
	res := f.call(http.MethodGet, "/api/v1/manifest", nil, func(r *http.Request) {
		r.Host = "signage.internal.example"
		r.Header.Set("Authorization", "Bearer "+token)
	})
	if res.status != http.StatusOK {
		t.Fatalf("the manifest with another Host answered %d: %s", res.status, res.body)
	}

	res = f.call(http.MethodPost, "/api/v1/heartbeat", map[string]any{
		"device_id": "px-host0001", "hardware_id": "hw-px-host0001",
	}, func(r *http.Request) {
		r.Header.Del(httpguard.HeaderName)
		r.Header.Set("Authorization", "Bearer "+token)
	})
	if res.status != http.StatusOK {
		t.Fatalf("the heartbeat with no %s header answered %d: %s", httpguard.HeaderName, res.status, res.body)
	}
}

func TestSessionCookieIsHardened(t *testing.T) {
	f := newFleet(t)
	res := f.call(http.MethodPost, "/api/admin/login", map[string]string{"password": f.password}, nil)
	f.mustOK(res, "login")

	cookie := res.header.Get("Set-Cookie")
	if cookie == "" {
		t.Fatal("the login set no cookie")
	}
	for _, want := range []string{httpguard.CookieName, "HttpOnly", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(cookie, want) {
			t.Errorf("the cookie %q does not hold %q", cookie, want)
		}
	}
}

func TestErrorShapesAreTheSameOnBothAPIs(t *testing.T) {
	f := newFleet(t)
	f.login()

	// web/shared/api.js reads {"error": ...} from every failure and
	// {"error": ..., "fields": [...]} from a 422. The device API and the admin API
	// must answer with the same shapes, or the one client would need two paths.
	deviceFields := f.call(http.MethodPost, "/api/v1/enroll", map[string]any{
		"device_id": "NOT A DEVICE ID",
	}, nil)
	if deviceFields.status != http.StatusUnprocessableEntity {
		t.Fatalf("the device API answered %d", deviceFields.status)
	}
	if len(deviceFields.fields(t)) == 0 {
		t.Fatalf("the device 422 has no fields: %s", deviceFields.body)
	}

	adminFields := f.adminCall(http.MethodPost, "/api/admin/assignments", map[string]any{})
	if adminFields.status != http.StatusUnprocessableEntity {
		t.Fatalf("the admin API answered %d: %s", adminFields.status, adminFields.body)
	}
	if len(adminFields.fields(t)) == 0 {
		t.Fatalf("the admin 422 has no fields: %s", adminFields.body)
	}

	// A 404 of each API.
	deviceNotFound := f.device(http.MethodGet, "/api/v1/manifest", "no such token", nil)
	adminNotFound := f.adminCall(http.MethodGet, "/api/admin/devices/px-nothing", nil)
	if deviceNotFound.errorText(t) == "" || adminNotFound.errorText(t) == "" {
		t.Fatalf("an answer holds no error text: %s / %s", deviceNotFound.body, adminNotFound.body)
	}
	for _, res := range []reply{deviceNotFound, adminNotFound} {
		if kind := res.header.Get("Content-Type"); kind != "application/json" {
			t.Errorf("the content type of an error is %q", kind)
		}
	}
}

func TestSettingsAndPassword(t *testing.T) {
	f := newFleet(t)
	f.login()

	res := f.mustOK(f.adminCall(http.MethodPut, "/api/admin/settings", map[string]any{
		"server_name": "Ridgeline Signage", "default_poll_seconds": 120,
		"public_url": "https://signage.example.com",
	}), "save the settings")
	var out struct {
		RestartNeeded bool `json:"restart_needed"`
		Settings      struct {
			ServerName        string `json:"server_name"`
			PollSeconds       int    `json:"default_poll_seconds"`
			QuietAfterSeconds int    `json:"quiet_after_seconds"`
		} `json:"settings"`
	}
	res.json(t, &out)
	if out.Settings.ServerName != "Ridgeline Signage" || out.Settings.PollSeconds != 120 {
		t.Fatalf("the settings are %+v", out.Settings)
	}
	if !out.RestartNeeded {
		t.Fatal("a change of the public URL did not ask for a restart")
	}

	// The device sees the new name and the new interval.
	token := f.pairDevice("px-set00001")
	m := f.getManifest(token)
	if m.ServerName != "Ridgeline Signage" || m.PollSeconds != 120 {
		t.Fatalf("the manifest is %+v", m)
	}

	// A bad value gives 422 and names the field.
	bad := f.adminCall(http.MethodPut, "/api/admin/settings", map[string]any{
		"server_name": "", "default_poll_seconds": 1, "public_url": "signage.example.com",
	})
	if bad.status != http.StatusUnprocessableEntity {
		t.Fatalf("bad settings answered %d: %s", bad.status, bad.body)
	}
	if len(bad.fields(t)) != 3 {
		t.Fatalf("the fields are %+v", bad.fields(t))
	}

	// The password route changes the password.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/password",
		map[string]string{"password": "another good password"}), "set the password")
	if f.password != "another good password" {
		t.Fatal("the password did not change")
	}
}

func TestHealthPage(t *testing.T) {
	f := newFleet(t)
	f.login()
	f.pairDevice("px-health01")
	f.uploadMedia("a.png", imageBytes(t, 20, 20))

	res := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/health", nil), "the health page")
	var view struct {
		ServerVersion string `json:"server_version"`
		Arch          string `json:"arch"`
		DataDir       string `json:"data_dir"`
		MediaWritable bool   `json:"media_writable"`
		MediaFiles    int    `json:"media_files"`
		MediaBytes    int64  `json:"media_bytes"`
		DatabaseBytes int64  `json:"database_bytes"`
		HasReleaseKey bool   `json:"has_release_key"`
		MirrorState   string `json:"mirror_state"`
		Integrity     string `json:"integrity"`
		Contacts      struct {
			Total     int            `json:"total"`
			LastHour  int            `json:"last_hour"`
			NeverSeen int            `json:"never_seen"`
			Versions  map[string]int `json:"versions"`
		} `json:"contacts"`
	}
	res.json(t, &view)

	if view.ServerVersion == "" || view.Arch == "" || view.DataDir == "" {
		t.Fatalf("the health page is %+v", view)
	}
	if !view.MediaWritable {
		t.Fatal("the media store reports that it is not writable")
	}
	if view.MediaFiles != 1 || view.MediaBytes == 0 || view.DatabaseBytes == 0 {
		t.Fatalf("the counts are %+v", view)
	}
	if view.Contacts.Total != 1 || view.Contacts.LastHour != 1 {
		t.Fatalf("the contacts are %+v", view.Contacts)
	}
	if view.Contacts.Versions["1.4.2"] != 1 {
		t.Fatalf("the version histogram is %+v", view.Contacts.Versions)
	}
	// Nobody ran the integrity check yet, so it is empty.
	if view.Integrity != "" {
		t.Fatalf("the integrity result is %q before anybody ran the check", view.Integrity)
	}

	// The check runs on demand.
	res = f.mustOK(f.adminCall(http.MethodPost, "/api/admin/health/integrity", nil), "the integrity check")
	var check struct {
		Integrity string `json:"integrity"`
	}
	res.json(t, &check)
	if check.Integrity != "ok" {
		t.Fatalf("the integrity check says %q", check.Integrity)
	}
	// The health page then shows the stored answer.
	res = f.mustOK(f.adminCall(http.MethodGet, "/api/admin/health", nil), "the health page")
	res.json(t, &view)
	if view.Integrity != "ok" {
		t.Fatalf("the health page says %q", view.Integrity)
	}
}
