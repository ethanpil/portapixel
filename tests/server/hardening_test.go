package server_test

import (
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/server/db"
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
	res := f.call(http.MethodPost, "/api/admin/login", map[string]string{"password": f.password()}, nil)
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
	// This server answers plain HTTP on the loopback, so the cookie must not take
	// Secure: a Secure cookie would never come back.
	if strings.Contains(cookie, "Secure") {
		t.Errorf("the cookie of a plain HTTP server holds Secure: %q", cookie)
	}
}

// TestSessionCookieIsSecureOverHTTPS covers the answer of a TLS-terminating proxy
// that the admin named in trusted_proxies, and the answer of one that nobody named.
//
// A public URL of https does NOT make the cookie Secure by itself. The flag must
// answer the request and not the configuration: a server that is also reachable over
// plain HTTP would otherwise send a cookie that the browser throws away, and the
// admin would be in a login loop with a 200 on every login.
func TestSessionCookieIsSecureOverHTTPS(t *testing.T) {
	f := newFleet(t)
	f.login()
	f.mustOK(f.adminCall(http.MethodPut, "/api/admin/settings", map[string]any{
		"server_name": "Test fleet", "default_poll_seconds": 60,
		"public_url": "https://signage.example.com",
	}), "save the settings")

	res := f.mustOK(f.call(http.MethodPost, "/api/admin/login",
		map[string]string{"password": f.password()}, nil), "login")
	if cookie := res.header.Get("Set-Cookie"); strings.Contains(cookie, "Secure") {
		t.Errorf("a public URL of https made the cookie Secure over plain HTTP: %q", cookie)
	}

	// A proxy that we trust says https in a header. One that we do not trust says
	// the same thing and is ignored.
	plain := newFleet(t)
	plain.trustProxies([]string{"127.0.0.1", "::1"})
	res = plain.mustOK(plain.call(http.MethodPost, "/api/admin/login",
		map[string]string{"password": plain.password()}, func(r *http.Request) {
			r.Header.Set("X-Forwarded-Proto", "https")
		}), "login behind a proxy")
	if cookie := res.header.Get("Set-Cookie"); !strings.Contains(cookie, "Secure") {
		t.Errorf("the cookie behind a trusted proxy does not hold Secure: %q", cookie)
	}

	untrusted := newFleet(t)
	res = untrusted.mustOK(untrusted.call(http.MethodPost, "/api/admin/login",
		map[string]string{"password": untrusted.password()}, func(r *http.Request) {
			r.Header.Set("X-Forwarded-Proto", "https")
		}), "login with a header that nobody may set")
	if cookie := res.header.Get("Set-Cookie"); strings.Contains(cookie, "Secure") {
		t.Errorf("a header of an untrusted peer made the cookie Secure: %q", cookie)
	}
}

// TestPasswordChangeNeedsTheCurrentPassword covers the one credential of the
// server. A session cookie alone must not be enough to change it.
func TestPasswordChangeNeedsTheCurrentPassword(t *testing.T) {
	f := newFleet(t)
	f.login()

	// A wrong current password answers 403 and names the field. It is not a 401:
	// web/shared/api.js signs the admin out at a 401, and a wrong value in one field
	// of a form is not a session that ended.
	bad := f.adminCall(http.MethodPost, "/api/admin/password", map[string]string{
		"current": "not the password", "password": "a new good password",
	})
	if bad.status != http.StatusForbidden {
		t.Fatalf("a wrong current password answered %d: %s", bad.status, bad.body)
	}
	fields := bad.fields(t)
	if len(fields) != 1 || fields[0].Field != "current" {
		t.Fatalf("the fields are %+v", fields)
	}
	if f.password() != testPassword {
		t.Fatal("a request with the wrong current password changed the password")
	}

	// A body with no current password at all is refused as well.
	if res := f.adminCall(http.MethodPost, "/api/admin/password",
		map[string]string{"password": "a new good password"}); res.status != http.StatusForbidden {
		t.Fatalf("a request with no current password answered %d", res.status)
	}
}

// TestPasswordChangeDropsTheOtherSessions covers the reason that somebody changes a
// password: a laptop that went missing. A session that stayed open would make the
// change worth nothing.
func TestPasswordChangeDropsTheOtherSessions(t *testing.T) {
	f := newFleet(t)
	f.login()

	// A second browser with its own cookie jar.
	other := newBrowser(t, f)
	other.login()
	other.mustOK(other.adminCall(http.MethodGet, "/api/admin/devices", nil), "the second browser")

	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/password", map[string]string{
		"current": testPassword, "password": "a new good password",
	}), "set the password")

	// The browser that made the change still works.
	f.mustOK(f.adminCall(http.MethodGet, "/api/admin/devices", nil), "the first browser")
	// The other one is out.
	if res := other.adminCall(http.MethodGet, "/api/admin/devices", nil); res.status != http.StatusUnauthorized {
		t.Fatalf("the other session answered %d after the password change", res.status)
	}
}

// TestClientAddressBehindAProxy covers the rate limiters and the last_ip column
// behind a reverse proxy, which deploy/README recommends.
func TestClientAddressBehindAProxy(t *testing.T) {
	f := newFleet(t)
	f.login()

	// An untrusted peer cannot choose the address that the server counts.
	spoofed := f.call(http.MethodPost, "/api/v1/enroll", map[string]any{
		"device_id": "px-spoof001", "hardware_id": "hw-spoof",
	}, func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
	})
	f.mustOK(spoofed, "an enroll with a header that nobody may set")
	waiting := f.pendingByDevice(t, "px-spoof001")
	if waiting.IP == "203.0.113.9" {
		t.Fatal("the server believed X-Forwarded-For from a peer that it does not trust")
	}

	// With the peer in the list, the right-most entry that is not a proxy wins.
	f.trustProxies([]string{"127.0.0.1", "::1", "10.0.0.0/8"})
	chained := f.call(http.MethodPost, "/api/v1/enroll", map[string]any{
		"device_id": "px-chain001", "hardware_id": "hw-chain",
	}, func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.7")
	})
	f.mustOK(chained, "an enroll behind a chain of proxies")
	if waiting = f.pendingByDevice(t, "px-chain001"); waiting.IP != "203.0.113.9" {
		t.Fatalf("the address of the screen is %q, want the client of the chain", waiting.IP)
	}
}

// newBrowser gives a second client of the same server, with its own cookie jar.
func newBrowser(t *testing.T, f *fleet) *fleet {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	other := *f
	other.client = &http.Client{Jar: jar}
	return &other
}

// pendingByDevice gives the waiting request of one device ID.
func (f *fleet) pendingByDevice(t *testing.T, id string) db.PendingEnrollment {
	t.Helper()
	p, err := f.db.PendingByDevice(id)
	if err != nil {
		t.Fatalf("no request of %s waits: %v", id, err)
	}
	return p
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

	// Every other failure of the two APIs. A path that no route holds is the one
	// that used to answer the text page of http.ServeMux, which api.js cannot parse.
	deviceUnauthorized := f.device(http.MethodGet, "/api/v1/manifest", "no such token", nil)
	adminNotFound := f.adminCall(http.MethodGet, "/api/admin/devices/px-nothing", nil)
	adminNoRoute := f.adminCall(http.MethodGet, "/api/admin/nope", nil)
	deviceNoRoute := f.device(http.MethodGet, "/api/v1/nope", "", nil)
	badMethod := f.adminCall(http.MethodDelete, "/api/admin/settings", nil)

	if deviceUnauthorized.status != http.StatusUnauthorized {
		t.Fatalf("the manifest with no good token answered %d", deviceUnauthorized.status)
	}
	if adminNoRoute.status != http.StatusNotFound || deviceNoRoute.status != http.StatusNotFound {
		t.Fatalf("a path that no route holds answered %d and %d",
			adminNoRoute.status, deviceNoRoute.status)
	}
	for name, res := range map[string]reply{
		"the device 401":      deviceUnauthorized,
		"the admin 404":       adminNotFound,
		"the admin no-route":  adminNoRoute,
		"the device no-route": deviceNoRoute,
		"the wrong method":    badMethod,
	} {
		if res.errorText(t) == "" {
			t.Errorf("%s holds no error text: %s", name, res.body)
		}
		if kind := res.header.Get("Content-Type"); kind != "application/json" {
			t.Errorf("the content type of %s is %q", name, kind)
		}
	}
}

// TestJSONRoutesNeedTheJSONContentType covers the cross-site request that needs no
// preflight. A form on another site can send text/plain, multipart/form-data or
// application/x-www-form-urlencoded, and none of them is application/json.
func TestJSONRoutesNeedTheJSONContentType(t *testing.T) {
	f := newFleet(t)
	f.login()

	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/enroll"},
		{http.MethodPost, "/api/admin/login"},
		{http.MethodPost, "/api/admin/groups"},
	} {
		res := f.call(c.method, c.path, map[string]any{"device_id": "px-ct000001", "name": "x"},
			func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") })
		if res.status != http.StatusUnsupportedMediaType {
			t.Errorf("%s %s with text/plain answered %d: %s", c.method, c.path, res.status, res.body)
		}
		if res.errorText(t) == "" {
			t.Errorf("%s %s holds no error text", c.method, c.path)
		}
	}
}

// TestLicences serves the third-party licence list (D33). The device serves the
// same file, so the two ends never show two different lists.
func TestLicences(t *testing.T) {
	f := newFleet(t)
	res := f.mustOK(f.call(http.MethodGet, "/licenses", nil, nil), "the licence list")
	if len(res.body) == 0 {
		t.Fatal("the licence list has no bytes")
	}
	if kind := res.header.Get("Content-Type"); !strings.HasPrefix(kind, "text/plain") {
		t.Fatalf("the licence list is %q", kind)
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
	// Nothing needs a restart. The Host allowlist comes from a function that runs
	// on each request, so a new public URL is live on the next one.
	if out.RestartNeeded {
		t.Fatal("a change of the public URL asked for a restart that it does not need")
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

	// The password route needs the password that is in use.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/password", map[string]string{
		"current": testPassword, "password": "another good password",
	}), "set the password")
	if f.password() != "another good password" {
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
