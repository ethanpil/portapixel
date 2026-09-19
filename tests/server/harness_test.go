// Package server_test holds the route tests of the fleet server.
//
// The tests drive the real handlers over a real HTTP connection, with a real
// SQLite database in a temporary directory. Nothing here is a stub except the
// GitHub API and the release signing key: everything else is the code that
// ships (plan section 17 item 7).
package server_test

import (
	"bytes"
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
	"github.com/ethanpil/portapixel/internal/server/admin"
	"github.com/ethanpil/portapixel/internal/server/api"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/media"
	"github.com/ethanpil/portapixel/internal/server/releases"
)

// testPassword is the admin password of a test server.
const testPassword = "a good test password"

// fleet is one server under test.
type fleet struct {
	t      *testing.T
	dir    string
	db     *db.DB
	store  *media.Store
	mirror *releases.Mirror
	srv    *httptest.Server
	// client carries the session cookie.
	client *http.Client

	mu    sync.Mutex
	hosts []string
	// settings are what the settings routes read and write.
	settings admin.Settings
	// passwordHash stands for the hash in server.toml. The test compares in
	// plain form, because bcrypt is tested in the command package.
	password string
}

// newFleet starts a server.
//
// The route stack is the stack of cmd/portapixel-server: the device API with no
// browser guards, and the admin API behind the Host allowlist and the CSRF
// header. The command wires the same thing; see the report for why the wiring is
// in two places.
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

	f := &fleet{
		t: t, dir: dir, db: database, store: store,
		password: testPassword,
		settings: admin.Settings{ServerName: "Test fleet", PollSeconds: 60},
	}
	f.mirror = releases.NewMirror(dir, "owner/name")
	f.mirror.Report = func(v, state, errText string) {
		database.SetMirrorState(v, state, errText)
	}

	log := opslog.New(dir + "/ops.log")

	deviceMux := http.NewServeMux()
	api.Deps{
		DB: database, Media: store, Mirror: f.mirror, Log: log,
		Limiter:     httpguard.NewLimiter(),
		ServerName:  func() string { return f.readSettings().ServerName },
		DefaultPoll: func() int { return f.readSettings().PollSeconds },
	}.Routes(deviceMux)

	uiMux := http.NewServeMux()
	admin.Deps{
		DB: database, Media: store, Mirror: f.mirror, Log: log,
		Sessions:      httpguard.NewSessions(),
		Limiter:       httpguard.NewLimiter(),
		DataDir:       dir,
		StartedAt:     time.Now(),
		Hosts:         f.allowedHosts,
		CheckPassword: f.checkPassword,
		SetPassword:   f.setPassword,
		Settings:      f.readSettings,
		SaveSettings:  f.saveSettings,
	}.Routes(uiMux)

	var ui http.Handler = uiMux
	ui = httpguard.RequireHeader(ui)
	ui = httpguard.HostAllowlist(f.allowedHosts)(ui)

	root := http.NewServeMux()
	root.Handle("/api/v1/", deviceMux)
	root.Handle("/", ui)

	f.srv = httptest.NewServer(root)
	t.Cleanup(f.srv.Close)

	// The allowlist holds the address that the test server answers on, the same
	// way the real allowlist holds the public URL.
	host := strings.TrimPrefix(f.srv.URL, "http://")
	f.mu.Lock()
	f.hosts = []string{host, "localhost", "127.0.0.1"}
	f.mu.Unlock()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.client = &http.Client{Jar: jar}
	return f
}

func (f *fleet) allowedHosts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.hosts...)
}

func (f *fleet) readSettings() admin.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings
}

func (f *fleet) saveSettings(s admin.Settings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings = s
	return nil
}

func (f *fleet) checkPassword(p string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return p != "" && p == f.password
}

func (f *fleet) setPassword(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.password = p
	return nil
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

// admin calls the admin API. Every route needs a session, so login runs first.
func (f *fleet) adminCall(method, path string, body any) reply {
	f.t.Helper()
	return f.call(method, path, body, nil)
}

// login makes the session that the admin routes need.
func (f *fleet) login() {
	f.t.Helper()
	res := f.call(http.MethodPost, "/api/admin/login",
		map[string]string{"password": f.password}, nil)
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

// mustOK fails the test when the answer is not 200.
func (f *fleet) mustOK(res reply, what string) reply {
	f.t.Helper()
	if res.status != http.StatusOK {
		f.t.Fatalf("%s answered %d: %s", what, res.status, res.body)
	}
	return res
}
