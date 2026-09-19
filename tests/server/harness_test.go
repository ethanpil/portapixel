// Package server_test holds the route tests of the fleet server.
//
// The tests drive the real handlers over a real HTTP connection, with a real
// SQLite database in a temporary directory. Nothing here is a stub except the
// GitHub API and the release signing key: everything else is the code that
// ships (plan section 17 item 7).
//
// The route stack comes from internal/server.New, which is the constructor that
// cmd/portapixel-server calls. There is no second copy of the wiring here. The
// guards that the hardening tests check are therefore the guards that the binary
// serves: remove the Host allowlist from that constructor and these tests fail.
package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/server"
	"github.com/ethanpil/portapixel/internal/server/admin"
	"github.com/ethanpil/portapixel/internal/server/api"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
	"github.com/ethanpil/portapixel/internal/server/media"
	"github.com/ethanpil/portapixel/internal/server/releases"
)

// testPassword is the admin password of a test server.
const testPassword = "a good test password"

// fleet is one server under test.
//
// The values that the routes read and write live in state, which fleet holds by
// pointer. So a copy of fleet for a subtest shares that state and copies no lock.
type fleet struct {
	t      *testing.T
	dir    string
	db     *db.DB
	store  *media.Store
	mirror *releases.Mirror
	srv    *httptest.Server
	// client carries the session cookie.
	client *http.Client
	st     *state
}

// state is what the routes of the server read and write through the functions of
// the harness.
type state struct {
	mu    sync.Mutex
	hosts []string
	// settings are what the settings routes read and write.
	settings admin.Settings
	// password stands for the hash in server.toml. The test compares in plain
	// form, because bcrypt is tested in the command package.
	password string
	// release is the approved release that the manifest path reads. The command
	// keeps it in memory for the same reason, so the harness does too.
	release *db.Release
	// proxies decides the address of a caller. A test that needs a trusted proxy
	// replaces it before it makes its first call.
	proxies *httpjson.Proxies
}

// newFleet starts a server.
func newFleet(t *testing.T) *fleet {
	t.Helper()
	dir := t.TempDir()

	database, err := db.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	store, err := media.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	// No proxy is in front of a test server, so the caller address is the peer
	// address. A test that needs one replaces the list before its first call.
	proxies, _ := httpjson.NewProxies(nil)
	f := &fleet{
		t: t, dir: dir, db: database, store: store,
		st: &state{
			password: testPassword,
			settings: admin.Settings{ServerName: "Test fleet", PollSeconds: 60},
			proxies:  proxies,
		},
	}
	f.mirror = releases.NewMirror(dir, "owner/name")
	f.mirror.Report = func(v, state, errText string) {
		database.SetMirrorState(v, state, errText)
		f.fleetChanged()
	}

	log := opslog.New(dir + "/ops.log")

	f.srv = httptest.NewServer(server.New(server.Deps{
		Device: api.Deps{
			DB: database, Media: store, Mirror: f.mirror, Log: log,
			Limiter:        httpguard.NewLimiter(),
			PendingLimiter: httpguard.NewPendingLimiter(),
			ClientIP:       f.clientIP,
			Fleet:          f.readFleet,
		},
		Admin: admin.Deps{
			DB: database, Media: store, Mirror: f.mirror, Log: log,
			Sessions:      f.sessions(),
			Limiter:       httpguard.NewLimiter(),
			ClientIP:      f.clientIP,
			DataDir:       dir,
			StartedAt:     time.Now(),
			Background:    context.Background,
			CheckPassword: f.checkPassword,
			SetPassword:   f.setPassword,
			Settings:      f.readSettings,
			SaveSettings:  f.saveSettings,
			FleetChanged:  f.fleetChanged,
		},
		Hosts: f.allowedHosts,
	}))
	t.Cleanup(f.srv.Close)

	// The allowlist holds the address that the test server answers on, the same
	// way the real allowlist holds the public URL.
	host := strings.TrimPrefix(f.srv.URL, "http://")
	f.st.mu.Lock()
	f.st.hosts = []string{host, "localhost", "127.0.0.1"}
	f.st.mu.Unlock()
	f.fleetChanged()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.client = &http.Client{Jar: jar}
	return f
}

// with gives a view of the fleet that reports to the *testing.T of a subtest.
//
// A helper of the harness calls Fatalf, and Fatalf of the parent T from the
// goroutine of a subtest is not permitted. Every t.Run that uses a helper takes a
// view from here. The state is shared, because it lives behind a pointer.
func (f *fleet) with(t *testing.T) *fleet {
	view := *f
	view.t = t
	return &view
}

func (f *fleet) allowedHosts() []string {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	return append([]string(nil), f.st.hosts...)
}

func (f *fleet) readSettings() admin.Settings {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	return f.st.settings
}

func (f *fleet) saveSettings(s admin.Settings) error {
	f.st.mu.Lock()
	f.st.settings = s
	f.st.mu.Unlock()
	f.fleetChanged()
	return nil
}

// readFleet gives the values that the manifest needs.
func (f *fleet) readFleet() api.Fleet {
	s := f.readSettings()
	f.st.mu.Lock()
	release := f.st.release
	f.st.mu.Unlock()
	return api.Fleet{ServerName: s.ServerName, DefaultPoll: s.PollSeconds, Release: release}
}

// fleetChanged reads the approved release again. The command does the same, so a
// poll of a device costs no query for it.
func (f *fleet) fleetChanged() {
	var release *db.Release
	if rel, err := f.db.ApprovedRelease(); err == nil {
		release = &rel
	}
	f.st.mu.Lock()
	f.st.release = release
	f.st.mu.Unlock()
}

func (f *fleet) checkPassword(p string) bool {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	return p != "" && p == f.st.password
}

func (f *fleet) setPassword(p string) error {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	f.st.password = p
	return nil
}

// password gives the password that the server takes now.
func (f *fleet) password() string {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	return f.st.password
}

// clientIP gives the address of a caller, through the proxy list of the harness.
func (f *fleet) clientIP(r *http.Request) string {
	f.st.mu.Lock()
	proxies := f.st.proxies
	f.st.mu.Unlock()
	return proxies.ClientIP(r)
}

// clientIsHTTPS answers the Secure question of the session cookie.
func (f *fleet) clientIsHTTPS(r *http.Request) bool {
	f.st.mu.Lock()
	proxies := f.st.proxies
	f.st.mu.Unlock()
	return proxies.ClientIsHTTPS(r) ||
		strings.HasPrefix(f.readSettings().PublicURL, "https://")
}

// trustProxies replaces the proxy list of the harness.
func (f *fleet) trustProxies(entries []string) {
	proxies, warnings := httpjson.NewProxies(entries)
	if len(warnings) != 0 {
		f.t.Fatalf("the proxy list gave warnings: %v", warnings)
	}
	f.st.mu.Lock()
	f.st.proxies = proxies
	f.st.mu.Unlock()
}

// reply is one answer of the API.
type reply struct {
	status int
	body   []byte
	header http.Header
}

// json reads the body into v.
func (r reply) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("the answer is not JSON: %v\n%s", err, r.body)
	}
}

// errorText gives the "error" field of the answer.
func (r reply) errorText(t *testing.T) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	json.Unmarshal(r.body, &body)
	return body.Error
}

// fields gives the "fields" list of a 422 answer.
func (r reply) fields(t *testing.T) []db.FieldError {
	t.Helper()
	var body struct {
		Error  string          `json:"error"`
		Fields []db.FieldError `json:"fields"`
	}
	r.json(t, &body)
	if body.Error == "" {
		t.Fatalf("a 422 answer with no error text: %s", r.body)
	}
	return body.Fields
}

// call makes one request. It sets the CSRF header on every method that changes
// something, which is what the browser does.
func (f *fleet) call(method, path string, body any, edit func(*http.Request)) reply {
	f.t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, reader)
	if err != nil {
		f.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set(httpguard.HeaderName, httpguard.HeaderValue)
	}
	if edit != nil {
		edit(req)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return reply{status: resp.StatusCode, body: data, header: resp.Header}
}

// adminCall calls the admin API. Every route needs a session, so login runs first.
func (f *fleet) adminCall(method, path string, body any) reply {
	f.t.Helper()
	return f.call(method, path, body, nil)
}

// login makes the session that the admin routes need.
func (f *fleet) login() {
	f.t.Helper()
	res := f.call(http.MethodPost, "/api/admin/login",
		map[string]string{"password": f.password()}, nil)
	if res.status != http.StatusOK {
		f.t.Fatalf("the login answered %d: %s", res.status, res.body)
	}
}

// device calls the device API with a bearer token.
func (f *fleet) device(method, path, token string, body any) reply {
	f.t.Helper()
	return f.call(method, path, body, func(r *http.Request) {
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
	})
}

// upload sends a file as the raw body, the way web/shared/api.js does.
func (f *fleet) upload(path, name string, body []byte) reply {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set(httpguard.HeaderName, httpguard.HeaderValue)
	req.Header.Set(admin.FilenameHeader, url.QueryEscape(name))
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: data, header: resp.Header}
}

// sessions makes the session store of the admin UI, the way the command does.
func (f *fleet) sessions() *httpguard.Sessions {
	sessions := httpguard.NewSessions()
	sessions.Secure = f.clientIsHTTPS
	return sessions
}

// mustOK fails the test when the answer is not 200.
func (f *fleet) mustOK(res reply, what string) reply {
	f.t.Helper()
	if res.status != http.StatusOK {
		f.t.Fatalf("%s answered %d: %s", what, res.status, res.body)
	}
	return res
}
