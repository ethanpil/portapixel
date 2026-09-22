package httpd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/browser"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/device/syncer"
	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// fx is the test device: a real library over a temporary media root, a real
// session store and limiter, and fake functions for everything else.
type fx struct {
	t     *testing.T
	h     http.Handler
	media string
	state string
	hub   *Hub

	cfg        config.Config
	saved      *config.Config
	applied    Applied
	commands   []string
	rootPW     string
	beats      []browser.Heartbeat
	codecs     manifest.CodecReport
	urlSkip    bool
	urlIndex   int
	readyHits  int
	cookie     *http.Cookie
	commandErr error
	// noSecret builds the handler with no player secret. A daemon that never
	// made one must answer no player call at all.
	noSecret bool

	// The fleet client. fleet is nil for a build that carries none, and then the
	// pairing routes answer 501. serverName is the name of the server that
	// manages the device; an empty name means standalone (D48).
	fleet      bool
	serverName string
	pairState  PairState
	pairURL    string
	pairToken  string
	pairErr    error
	unpaired   int
}

func newFx(t *testing.T) *fx {
	t.Helper()
	f := &fx{t: t, media: t.TempDir(), state: t.TempDir(), hub: NewHub()}
	f.cfg = config.Default()
	f.cfg.Web.Password = "letmein"
	f.cfg.Network.WifiSSID = "Office"
	f.cfg.Network.WifiPSK = "a secret"

	f.rebuild()
	return f
}

// rebuild makes the handler again from the flags of the fixture.
func (f *fx) rebuild() {
	t := f.t
	t.Helper()
	log := opslog.New(filepath.Join(f.state, "ops.log"))
	lib := library.New(library.Options{MediaRoot: f.media, StateDir: f.state, Log: log})
	lib.Rescan()

	secret := testSecret
	if f.noSecret {
		secret = ""
	}
	// The fleet client of the fixture. A nil set of functions is a build with no
	// fleet client, and then every pairing route answers 501.
	var pairState func() PairState
	var pair func(context.Context, string, string) (PairState, error)
	var unpair func() error
	var managed func() (string, bool)
	if f.fleet {
		pairState = func() PairState { return f.pairState }
		pair = func(_ context.Context, url, token string) (PairState, error) {
			f.pairURL, f.pairToken = url, token
			if f.pairErr != nil {
				return PairState{}, f.pairErr
			}
			return f.pairState, nil
		}
		unpair = func() error { f.unpaired++; return nil }
		managed = func() (string, bool) { return f.serverName, f.serverName != "" }
	}
	f.h = New(Deps{
		MediaRoot:    f.media,
		Log:          log,
		Library:      lib,
		Sessions:     httpguard.NewSessions(),
		Limiter:      httpguard.NewLimiter(),
		Hub:          f.hub,
		PlayerSecret: secret,
		Hosts:        func() []string { return []string{"localhost", "127.0.0.1", "lobby.local"} },
		Password:     func() string { return f.cfg.Web.Password },
		Status: func(loopback, trusted bool) manifest.Status {
			st := manifest.Status{DeviceID: "px-1a2b3c4d", Name: f.cfg.Device.Name}
			if loopback {
				st.PairingCode = "H7K2QX"
			}
			if trusted {
				// The fixture stands in for health.redact: the whole report goes only
				// to a caller that may see it.
				st.ServerURL = "https://fleet.example.com"
			}
			return st
		},
		Config: func() ConfigView { return ConfigView{Config: f.cfg.Masked()} },
		SaveConfig: func(incoming config.Config) (Applied, error) {
			merged := config.MergeMasked(f.cfg, incoming)
			if errs := merged.Validate(); len(errs) > 0 {
				return Applied{}, errs
			}
			// The same field guard that the daemon uses (D48). It is here, and not
			// in the handler, because the daemon holds the configuration that runs
			// and the handler holds no rule.
			if f.serverName != "" {
				for _, c := range config.ChangeClass(f.cfg, merged) {
					if syncer.ManagedField(c.Field) {
						return Applied{}, ErrManaged{Server: f.serverName}
					}
				}
			}
			f.cfg = merged
			saved := merged
			f.saved = &saved
			return f.applied, nil
		},
		ActivePlaylist: func() string { return "default" },
		PlayerManifest: func() library.PlayerManifest {
			return library.PlayerManifest{Fallback: true, Tier: "high"}
		},
		Heartbeat: func(hb browser.Heartbeat) { f.beats = append(f.beats, hb) },
		URLItem: func(index int) (bool, error) {
			f.urlIndex = index
			return f.urlSkip, nil
		},
		PlayerReady: func() { f.readyHits++ },
		Command: func(name string) error {
			f.commands = append(f.commands, name)
			return f.commandErr
		},
		// The daemon also looks for a release bundle here. A test needs the scan
		// only (D52).
		Rescan:          lib.Rescan,
		AdminURL:        func() string { return "http://lobby.local/" },
		SetRootPassword: func(pw string) error { f.rootPW = pw; return nil },
		SetCodecs:       func(r manifest.CodecReport) { f.codecs = r },
		PairState:       pairState,
		Pair:            pair,
		Unpair:          unpair,
		Managed:         managed,
	})
}

// request holds the parts of a test request that a test wants to change.
type request struct {
	host       string
	remote     string
	noCSRF     bool
	secret     string
	headers    map[string]string
	rawBody    io.Reader
	noCookie   bool
	rangeBytes string
	// contentLength sets r.ContentLength, which the upload route reads to refuse
	// a file that cannot fit.
	contentLength int64
}

// do sends a request through the whole middleware chain.
func (f *fx) do(method, target string, body any, opts ...func(*request)) *httptest.ResponseRecorder {
	f.t.Helper()
	req := request{host: "127.0.0.1", remote: "127.0.0.1:52000"}
	for _, o := range opts {
		o(&req)
	}

	var reader io.Reader
	switch {
	case req.rawBody != nil:
		reader = req.rawBody
	case body != nil:
		data, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}

	r := httptest.NewRequest(method, target, reader)
	if req.contentLength != 0 {
		r.ContentLength = req.contentLength
	}
	r.Host = req.host
	r.RemoteAddr = req.remote
	if method != http.MethodGet && method != http.MethodHead && !req.noCSRF {
		r.Header.Set(httpguard.HeaderName, httpguard.HeaderValue)
	}
	if req.secret != "" {
		r.Header.Set(PlayerHeader, req.secret)
	}
	if req.rangeBytes != "" {
		r.Header.Set("Range", req.rangeBytes)
	}
	for k, v := range req.headers {
		r.Header.Set(k, v)
	}
	if f.cookie != nil && !req.noCookie {
		r.AddCookie(f.cookie)
	}

	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// login makes a session and keeps the cookie for the later requests.
func (f *fx) login() {
	f.t.Helper()
	w := f.do(http.MethodPost, "/api/login", map[string]string{"password": f.cfg.Web.Password})
	if w.Code != http.StatusOK {
		f.t.Fatalf("login gave %d: %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == httpguard.CookieName {
			f.cookie = c
		}
	}
	if f.cookie == nil {
		f.t.Fatal("login set no session cookie")
	}
}

// body reads a JSON answer into a map.
func body(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("the answer is not JSON (%d): %s", w.Code, w.Body)
	}
	return out
}

// mustJSON checks that an answer is JSON and never HTML. Every error of this API
// is JSON, so a UI never has to read an error page.
func mustJSON(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type = %q, want JSON. Body: %s", ct, w.Body)
	}
}

// ------------------------------------------------------------------ hardening

func TestHostAllowlist(t *testing.T) {
	f := newFx(t)
	tests := []struct {
		host string
		want int
	}{
		{"127.0.0.1", http.StatusOK},
		{"127.0.0.1:8099", http.StatusOK},
		{"localhost", http.StatusOK},
		{"lobby.local", http.StatusOK},
		{"evil.example.com", http.StatusMisdirectedRequest},
		{"attacker.local", http.StatusMisdirectedRequest},
		{"", http.StatusMisdirectedRequest},
	}
	for _, tt := range tests {
		w := f.do(http.MethodGet, "/api/status", nil, func(r *request) { r.host = tt.host })
		if w.Code != tt.want {
			t.Errorf("Host %q gave %d, want %d", tt.host, w.Code, tt.want)
		}
		if tt.want != http.StatusOK {
			mustJSON(t, w)
		}
	}
}

func TestCSRFHeader(t *testing.T) {
	f := newFx(t)
	f.login()

	// A POST with no header must fail, whatever else is correct.
	w := f.do(http.MethodPost, "/api/rescan", nil, func(r *request) { r.noCSRF = true })
	if w.Code != http.StatusForbidden {
		t.Errorf("a POST with no header gave %d, want 403", w.Code)
	}
	mustJSON(t, w)

	// The same call with the header works.
	if w := f.do(http.MethodPost, "/api/rescan", nil); w.Code != http.StatusOK {
		t.Errorf("a POST with the header gave %d: %s", w.Code, w.Body)
	}
	// A GET needs no header.
	if w := f.do(http.MethodGet, "/api/status", nil, func(r *request) { r.noCSRF = true }); w.Code != http.StatusOK {
		t.Errorf("a GET with no header gave %d", w.Code)
	}
}

func TestPlayerEndpointsNeedLoopbackAndSecret(t *testing.T) {
	f := newFx(t)
	tests := []struct {
		name   string
		remote string
		secret string
		want   int
	}{
		{"loopback with the secret", "127.0.0.1:52000", testSecret, http.StatusOK},
		{"loopback with no secret", "127.0.0.1:52000", "", http.StatusForbidden},
		{"loopback with a wrong secret", "127.0.0.1:52000", "0000", http.StatusForbidden},
		{"the network with the secret", "192.168.1.30:52000", testSecret, http.StatusForbidden},
		{"the network with no secret", "192.168.1.30:52000", "", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := f.do(http.MethodGet, "/api/player/manifest", nil, func(r *request) {
				r.remote = tt.remote
				r.secret = tt.secret
			})
			if w.Code != tt.want {
				t.Errorf("got %d, want %d: %s", w.Code, tt.want, w.Body)
			}
			mustJSON(t, w)
		})
	}

	// The SSE stream carries the secret in the query, because EventSource cannot
	// set a header.
	w := f.do(http.MethodGet, "/api/player/manifest?k="+testSecret, nil)
	if w.Code != http.StatusOK {
		t.Errorf("the secret in the query gave %d", w.Code)
	}
}

func TestPairingCodeOnlyForTheDevice(t *testing.T) {
	f := newFx(t)

	local := body(t, f.do(http.MethodGet, "/api/status", nil))
	if local["pairing_code"] != "H7K2QX" {
		t.Errorf("the device itself did not see the pairing code: %v", local)
	}

	lan := body(t, f.do(http.MethodGet, "/api/status", nil, func(r *request) {
		r.remote = "192.168.1.30:52000"
	}))
	if _, there := lan["pairing_code"]; there {
		t.Errorf("the pairing code went to the network: %v", lan)
	}
}

// ------------------------------------------------------------------ sessions

func TestLoginAndSession(t *testing.T) {
	f := newFx(t)

	// Without a session the admin API is closed.
	w := f.do(http.MethodGet, "/api/config", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated GET gave %d", w.Code)
	}
	if got := body(t, f.do(http.MethodGet, "/api/session", nil))["authenticated"]; got != false {
		t.Errorf("session says %v", got)
	}

	// A wrong password is 401 and nothing else.
	w = f.do(http.MethodPost, "/api/login", map[string]string{"password": "guess"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong password gave %d", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Errorf("a failed login set a cookie")
	}

	f.login()
	if got := body(t, f.do(http.MethodGet, "/api/session", nil))["authenticated"]; got != true {
		t.Errorf("session says %v after the login", got)
	}
	if w := f.do(http.MethodGet, "/api/config", nil); w.Code != http.StatusOK {
		t.Errorf("GET /api/config gave %d after the login", w.Code)
	}

	// Logging out closes the session again.
	if w := f.do(http.MethodPost, "/api/logout", nil); w.Code != http.StatusOK {
		t.Fatalf("logout gave %d", w.Code)
	}
	if w := f.do(http.MethodGet, "/api/config", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("the session lived after the logout: %d", w.Code)
	}
}

func TestLoginLimiter(t *testing.T) {
	f := newFx(t)
	for i := 0; i < 5; i++ {
		if w := f.do(http.MethodPost, "/api/login", map[string]string{"password": "guess"}); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d gave %d", i, w.Code)
		}
	}
	w := f.do(http.MethodPost, "/api/login", map[string]string{"password": "guess"})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("the sixth attempt gave %d, want 429", w.Code)
	}
	// Even the correct password waits: that is the point of the limiter.
	w = f.do(http.MethodPost, "/api/login", map[string]string{"password": f.cfg.Web.Password})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("the correct password gave %d, want 429", w.Code)
	}
}

// ------------------------------------------------------------------ config

func TestConfigGetIsMasked(t *testing.T) {
	f := newFx(t)
	f.login()

	got := body(t, f.do(http.MethodGet, "/api/config", nil))
	cfg := got["config"].(map[string]any)
	network := cfg["network"].(map[string]any)
	web := cfg["web"].(map[string]any)
	if network["wifi_psk"] != config.Mask || web["password"] != config.Mask {
		t.Fatalf("the secrets are not masked: %v", cfg)
	}
}

func TestConfigPutRoundTripWithMaskedSecrets(t *testing.T) {
	f := newFx(t)
	f.login()
	f.applied = Applied{Applied: "live", Changes: []config.Change{{Field: "device.name", Class: config.Live}}}

	// Read, change one field, and send it back with the masks in it.
	view := body(t, f.do(http.MethodGet, "/api/config", nil))
	cfg := view["config"].(map[string]any)
	cfg["device"].(map[string]any)["name"] = "Lobby Screen"

	w := f.do(http.MethodPut, "/api/config", cfg)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT gave %d: %s", w.Code, w.Body)
	}
	answer := body(t, w)
	if answer["applied"] != "live" {
		t.Errorf("applied = %v", answer["applied"])
	}
	if f.saved == nil {
		t.Fatal("the daemon saved nothing")
	}
	if f.saved.Device.Name != "Lobby Screen" {
		t.Errorf("name = %q", f.saved.Device.Name)
	}
	// The masks must not have replaced the true secrets.
	if f.saved.Network.WifiPSK != "a secret" || f.saved.Web.Password != "letmein" {
		t.Errorf("a save with masks lost a secret: psk=%q password=%q",
			f.saved.Network.WifiPSK, f.saved.Web.Password)
	}

	// An empty secret clears it: that is how a person removes a WiFi key.
	cfg["network"].(map[string]any)["wifi_psk"] = ""
	cfg["network"].(map[string]any)["wifi_ssid"] = ""
	if w := f.do(http.MethodPut, "/api/config", cfg); w.Code != http.StatusOK {
		t.Fatalf("PUT gave %d: %s", w.Code, w.Body)
	}
	if f.saved.Network.WifiPSK != "" {
		t.Errorf("the WiFi key did not clear: %q", f.saved.Network.WifiPSK)
	}
}

func TestConfigPutFieldErrors(t *testing.T) {
	f := newFx(t)
	f.login()

	view := body(t, f.do(http.MethodGet, "/api/config", nil))
	cfg := view["config"].(map[string]any)
	cfg["display"].(map[string]any)["rotation"] = 45
	cfg["audio"].(map[string]any)["volume"] = 300

	w := f.do(http.MethodPut, "/api/config", cfg)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a bad configuration gave %d, want 422: %s", w.Code, w.Body)
	}
	answer := body(t, w)
	fields, ok := answer["fields"].([]any)
	if !ok || len(fields) != 2 {
		t.Fatalf("fields = %v", answer["fields"])
	}
	names := map[string]bool{}
	for _, item := range fields {
		entry := item.(map[string]any)
		names[entry["field"].(string)] = true
		if entry["message"] == "" {
			t.Errorf("a field error with no message: %v", entry)
		}
	}
	if !names["display.rotation"] || !names["audio.volume"] {
		t.Errorf("the field names are wrong: %v", names)
	}
}

func TestConfigPutWithABadBody(t *testing.T) {
	f := newFx(t)
	f.login()
	w := f.do(http.MethodPut, "/api/config", nil, func(r *request) {
		r.rawBody = strings.NewReader("{not json")
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a bad body gave %d", w.Code)
	}
	mustJSON(t, w)
}

// ------------------------------------------------------------------ playlists

func TestPlaylistCRUDOverTheAPI(t *testing.T) {
	f := newFx(t)
	f.login()

	// Create.
	w := f.do(http.MethodPost, "/api/playlists", map[string]string{"title": "Lobby Loop"})
	if w.Code != http.StatusOK {
		t.Fatalf("create gave %d: %s", w.Code, w.Body)
	}
	name := body(t, w)["name"].(string)
	if name != "lobby-loop" {
		t.Fatalf("name = %q", name)
	}
	// The second create is a conflict.
	if w := f.do(http.MethodPost, "/api/playlists", map[string]string{"title": "Lobby Loop"}); w.Code != http.StatusConflict {
		t.Errorf("the second create gave %d, want 409", w.Code)
	}

	// Upload a file as the raw body.
	w = f.do(http.MethodPost, "/api/media/"+name, nil, func(r *request) {
		r.rawBody = strings.NewReader("jpeg data here")
		r.headers = map[string]string{"X-Filename": "Front%20Desk.jpg"}
	})
	if w.Code != http.StatusOK {
		t.Fatalf("upload gave %d: %s", w.Code, w.Body)
	}
	upload := body(t, w)
	file := upload["name"].(string)
	if file != "Front-Desk.jpg" || upload["size"].(float64) != 14 {
		t.Fatalf("upload = %v", upload)
	}

	// Save the playlist that names the file.
	w = f.do(http.MethodPut, "/api/playlists/"+name, map[string]any{
		"title": "Lobby Loop",
		"items": []map[string]any{{"file": file, "duration": 12}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("save gave %d: %s", w.Code, w.Body)
	}

	// Read the list back.
	list := body(t, f.do(http.MethodGet, "/api/playlists", nil))
	playlists := list["playlists"].([]any)
	if len(playlists) != 1 {
		t.Fatalf("playlists = %v", playlists)
	}
	first := playlists[0].(map[string]any)
	if first["name"] != name || first["title"] != "Lobby Loop" {
		t.Errorf("playlist = %v", first)
	}
	items := first["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	item := items[0].(map[string]any)
	if item["kind"] != "image" || item["src"] != "/media/lobby-loop/Front-Desk.jpg" || item["missing"] != false {
		t.Errorf("item = %v", item)
	}
	if list["active"] != "default" {
		t.Errorf("active = %v", list["active"])
	}

	// A playlist that breaks a rule is 422 with field errors.
	w = f.do(http.MethodPut, "/api/playlists/"+name, map[string]any{
		"items": []map[string]any{{"file": "../../etc/shadow"}},
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a bad playlist gave %d: %s", w.Code, w.Body)
	}
	if fields := body(t, w)["fields"].([]any); len(fields) == 0 {
		t.Errorf("no field errors: %s", w.Body)
	}

	// Rename.
	w = f.do(http.MethodPost, "/api/playlists/"+name+"/rename", map[string]string{"title": "Front Desk"})
	if w.Code != http.StatusOK {
		t.Fatalf("rename gave %d: %s", w.Code, w.Body)
	}
	next := body(t, w)["name"].(string)
	if next != "front-desk" {
		t.Fatalf("new name = %q", next)
	}

	// Delete the file and then the playlist.
	if w := f.do(http.MethodDelete, "/api/media/"+next+"/"+file, nil); w.Code != http.StatusOK {
		t.Fatalf("delete media gave %d: %s", w.Code, w.Body)
	}
	if w := f.do(http.MethodDelete, "/api/playlists/"+next, nil); w.Code != http.StatusOK {
		t.Fatalf("delete playlist gave %d: %s", w.Code, w.Body)
	}
	if w := f.do(http.MethodDelete, "/api/playlists/"+next, nil); w.Code != http.StatusNotFound {
		t.Errorf("the second delete gave %d, want 404", w.Code)
	}
}

func TestUploadNeedsAFileName(t *testing.T) {
	f := newFx(t)
	f.login()
	if _, err := f.do(http.MethodPost, "/api/playlists", map[string]string{"title": "x"}), error(nil); err != nil {
		t.Fatal(err)
	}
	w := f.do(http.MethodPost, "/api/media/x", nil, func(r *request) {
		r.rawBody = strings.NewReader("data")
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("an upload with no name gave %d", w.Code)
	}
}

// ------------------------------------------------------------------ commands

func TestCommands(t *testing.T) {
	f := newFx(t)
	f.login()
	for _, name := range []string{"reboot", "restart-browser", "screen-on", "screen-off"} {
		if w := f.do(http.MethodPost, "/api/commands/"+name, nil); w.Code != http.StatusOK {
			t.Errorf("%s gave %d: %s", name, w.Code, w.Body)
		}
	}
	if len(f.commands) != 4 {
		t.Errorf("commands = %v", f.commands)
	}
}

func TestOpslog(t *testing.T) {
	f := newFx(t)
	f.login() // this writes a web.login line
	got := body(t, f.do(http.MethodGet, "/api/opslog?n=10", nil))
	entries := got["entries"].([]any)
	if len(entries) == 0 {
		t.Fatalf("the log is empty: %s", "no entries")
	}
	first := entries[0].(map[string]any)
	if first["event"] == nil || first["time"] == nil {
		t.Errorf("entry = %v", first)
	}
}

func TestRootPassword(t *testing.T) {
	f := newFx(t)
	f.login()

	w := f.do(http.MethodPost, "/api/system/root-password", map[string]string{"password": "short"})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a short password gave %d", w.Code)
	}
	w = f.do(http.MethodPost, "/api/system/root-password", map[string]string{"password": "a longer password"})
	if w.Code != http.StatusOK {
		t.Fatalf("a good password gave %d: %s", w.Code, w.Body)
	}
	if f.rootPW != "a longer password" {
		t.Errorf("the daemon got %q", f.rootPW)
	}
}

func TestRoutesOfTheLaterMilestones(t *testing.T) {
	f := newFx(t)
	f.login()
	tests := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/pair"},
		{http.MethodPost, "/api/pair"},
		{http.MethodDelete, "/api/pair"},
		{http.MethodPost, "/api/update/check"},
		{http.MethodPost, "/api/update/apply"},
		{http.MethodGet, "/api/disks"},
		{http.MethodPost, "/api/install-to-disk"},
		{http.MethodGet, "/api/install-to-disk/events"},
	}
	for _, tt := range tests {
		w := f.do(tt.method, tt.path, nil)
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s %s gave %d, want 501", tt.method, tt.path, w.Code)
		}
		mustJSON(t, w)
		if body(t, w)["error"] != NotImplemented {
			t.Errorf("%s %s said %s", tt.method, tt.path, w.Body)
		}
	}
}

// ------------------------------------------------------------------ player

func TestPlayerProtocol(t *testing.T) {
	f := newFx(t)
	withSecret := func(r *request) { r.secret = testSecret }

	// The manifest.
	got := body(t, f.do(http.MethodGet, "/api/player/manifest", nil, withSecret))
	if got["fallback"] != true || got["tier"] != "high" {
		t.Errorf("manifest = %v", got)
	}

	// A heartbeat.
	w := f.do(http.MethodPost, "/api/player/heartbeat", map[string]any{
		"playlist": "default", "index": 1, "name": "promo.mp4", "kind": "video",
		"state": "playing", "frames": 18211, "position": 12.4,
	}, withSecret)
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat gave %d: %s", w.Code, w.Body)
	}
	if len(f.beats) != 1 || f.beats[0].Frames != 18211 || f.beats[0].Name != "promo.mp4" {
		t.Fatalf("the daemon got %+v", f.beats)
	}

	// A URL item that the daemon takes.
	w = f.do(http.MethodPost, "/api/player/url-item", map[string]int{"index": 2}, withSecret)
	if w.Code != http.StatusOK {
		t.Fatalf("url-item gave %d", w.Code)
	}
	if f.urlIndex != 2 {
		t.Errorf("index = %d", f.urlIndex)
	}
	if answer := body(t, w); answer["skip"] != nil {
		t.Errorf("answer = %v, want no skip", answer)
	}

	// A URL item that does not answer.
	f.urlSkip = true
	w = f.do(http.MethodPost, "/api/player/url-item", map[string]int{"index": 3}, withSecret)
	if answer := body(t, w); answer["skip"] != true {
		t.Errorf("answer = %v, want skip", answer)
	}

	// The answer to a grace request.
	if w := f.do(http.MethodPost, "/api/player/ready", nil, withSecret); w.Code != http.StatusOK {
		t.Fatalf("ready gave %d", w.Code)
	}
	if f.readyHits != 1 {
		t.Errorf("ready hits = %d", f.readyHits)
	}
}

func TestPlayerEvents(t *testing.T) {
	f := newFx(t)

	// httptest.NewRecorder cannot stream, so this test uses a real server. The
	// client is the loopback address, which the player endpoints need.
	server := httptest.NewServer(f.h)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/player/events?k="+testSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the stream gave %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}

	// Wait for the subscription and then send an event.
	deadline := time.Now().Add(5 * time.Second)
	for f.hub.Listeners() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	f.hub.Send(EventPlaylist, map[string]string{"playlist": "evening"})

	buf := make([]byte, 512)
	n, err := res.Body.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	text := string(buf[:n])
	if !strings.Contains(text, ": connected") {
		t.Errorf("the stream did not open with a comment: %q", text)
	}
	if !strings.Contains(text, "event: playlist") {
		// The first read may hold the comment only. Read once more.
		n, err = res.Body.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		text = string(buf[:n])
	}
	if !strings.Contains(text, "event: playlist") || !strings.Contains(text, `"playlist":"evening"`) {
		t.Errorf("the event did not arrive: %q", text)
	}
}

func TestQRCode(t *testing.T) {
	f := newFx(t)
	w := f.do(http.MethodGet, "/api/player/qr.svg", nil, func(r *request) { r.secret = testSecret })
	if w.Code != http.StatusOK {
		t.Fatalf("qr.svg gave %d: %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("content type = %q", ct)
	}
	svg := w.Body.String()
	if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") {
		t.Errorf("the answer is not an SVG: %.80s", svg)
	}
	if !strings.Contains(svg, "viewBox") || !strings.Contains(svg, "<path") {
		t.Errorf("the SVG holds no image: %.200s", svg)
	}
}

// ------------------------------------------------------------------ static files

func TestMediaFiles(t *testing.T) {
	f := newFx(t)
	if err := os.MkdirAll(filepath.Join(f.media, "default"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "0123456789"
	if err := os.WriteFile(filepath.Join(f.media, "default", "a.jpg"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.media, "portapixel.toml"), []byte("[device]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The whole file.
	w := f.do(http.MethodGet, "/media/default/a.jpg", nil)
	if w.Code != http.StatusOK || w.Body.String() != content {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}

	// A Range request, which a video player and the fleet client both use.
	w = f.do(http.MethodGet, "/media/default/a.jpg", nil, func(r *request) { r.rangeBytes = "bytes=2-5" })
	if w.Code != http.StatusPartialContent {
		t.Fatalf("a Range request gave %d", w.Code)
	}
	if w.Body.String() != "2345" {
		t.Errorf("the range holds %q", w.Body)
	}

	// portapixel.toml holds the admin password, the WiFi key and the fleet token.
	// /media/ must never serve it.
	w = f.do(http.MethodGet, "/media/portapixel.toml", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("/media/portapixel.toml gave %d: %s", w.Code, w.Body)
	}
	if err := os.WriteFile(filepath.Join(f.media, "default", "playlist.toml"), []byte("[[item]]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodGet, "/media/default/playlist.toml", nil); w.Code != http.StatusNotFound {
		t.Errorf("/media/default/playlist.toml gave %d", w.Code)
	}
	// A release bundle in _update is for the updater, not for the network.
	if err := os.MkdirAll(filepath.Join(f.media, "_update"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.media, "_update", "portapixeld-amd64"), []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodGet, "/media/_update/portapixeld-amd64", nil); w.Code != http.StatusNotFound {
		t.Errorf("/media/_update/ is served: %d", w.Code)
	}
	// A fleet object is content, so it is served.
	if err := os.MkdirAll(filepath.Join(f.media, "_fleet", "media"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.media, "_fleet", "media", "aabbccdd-clip.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodGet, "/media/_fleet/media/aabbccdd-clip.mp4", nil); w.Code != http.StatusOK {
		t.Errorf("a fleet object is not served: %d", w.Code)
	}

	// No listings and no way out of the media root.
	for _, path := range []string{"/media/", "/media/default/", "/media/../etc/shadow", `/media/..\..\secret`} {
		w := f.do(http.MethodGet, path, nil)
		if w.Code == http.StatusOK {
			t.Errorf("%s gave 200: %s", path, w.Body)
		}
	}
	// A missing file is a JSON 404.
	w = f.do(http.MethodGet, "/media/default/gone.jpg", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("a missing file gave %d", w.Code)
	}
	mustJSON(t, w)
}

func TestStaticAssets(t *testing.T) {
	f := newFx(t)
	// The shared stylesheet is in the binary and is served with its true media
	// type: a browser refuses a stylesheet that arrives as text/plain.
	w := f.do(http.MethodGet, "/shared/pp.css", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("pp.css gave %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("content type = %q", ct)
	}
	w = f.do(http.MethodGet, "/shared/api.js", nil)
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("api.js content type = %q", ct)
	}
	// No listing of the asset directory.
	if w := f.do(http.MethodGet, "/shared/", nil); w.Code == http.StatusOK {
		t.Errorf("/shared/ gave a listing")
	}
}

func TestCleanRelative(t *testing.T) {
	good := []string{"a.jpg", "default/a.jpg", "_fleet/media/aa-b.mp4"}
	bad := []string{"", "/etc/shadow", "../x", "default/../../x", "a\x00b"}
	// A backslash and a colon depend on the system. On Linux, which is what a
	// device runs, both are ordinary characters in a file name: a file that
	// somebody sideloaded can carry them, and the library scan calls such a file
	// healthy. A 400 from /media/ would mean that the file can never play and that
	// nothing says why. os.OpenRoot in serveMedia is what holds a request inside
	// the media root, not a test on characters. On Windows the same two characters
	// are a path separator and a drive letter, so there they are refused.
	bySystem := []string{`default\x`, "C:/x"}
	if runtime.GOOS == "windows" {
		bad = append(bad, bySystem...)
	} else {
		good = append(good, bySystem...)
	}
	for _, in := range good {
		if _, ok := cleanRelative(in); !ok {
			t.Errorf("cleanRelative(%q) refused a good path", in)
		}
	}
	for _, in := range bad {
		if _, ok := cleanRelative(in); ok {
			t.Errorf("cleanRelative(%q) accepted a bad path", in)
		}
	}
}

// The other half of the rule above, on the system that a device runs. A file name
// with a colon and a backslash in it PLAYS, and it still cannot name anything
// outside the media root.
func TestMediaServesAnOddNameAndStaysInsideTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a colon and a backslash are not legal in a Windows file name")
	}
	f := newFx(t)
	if err := os.MkdirAll(filepath.Join(f.media, "lobby"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The name that a person sideloaded, for example from a camera or a download.
	odd := `clip:2026-01-02\a.jpg`
	if err := os.WriteFile(filepath.Join(f.media, "lobby", odd), []byte("picture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodGet, "/media/lobby/"+url.PathEscape(odd), nil); w.Code != http.StatusOK {
		t.Errorf("a file whose name holds a colon gave %d: %s", w.Code, w.Body)
	}

	// The same characters must not become a way out of the root. A link with such a
	// name to a file outside the media root is not content. os.OpenRoot refuses it.
	secret := filepath.Join(f.state, "secret.jpg")
	if err := os.WriteFile(secret, []byte("the root password hash"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := `out:of\root.jpg`
	if err := os.Symlink(secret, filepath.Join(f.media, "lobby", out)); err != nil {
		t.Skipf("this machine cannot make a symbolic link: %v", err)
	}
	if w := f.do(http.MethodGet, "/media/lobby/"+url.PathEscape(out), nil); w.Code == http.StatusOK {
		t.Errorf("a link out of the media root was served: %s", w.Body)
	}
	// And the path steps are still refused, whatever characters come with them.
	for _, path := range []string{`/media/..\..\secret`, "/media/lobby/../../etc/shadow"} {
		if w := f.do(http.MethodGet, path, nil); w.Code == http.StatusOK {
			t.Errorf("%s gave 200: %s", path, w.Body)
		}
	}
}

// A link on the media partition must never reach a file outside it. Every laptop
// can write to that partition, and the daemon runs as root: /etc/shadow and the
// state directory with the device token are one link away.
func TestMediaDoesNotFollowALinkOutOfTheRoot(t *testing.T) {
	f := newFx(t)
	secret := filepath.Join(f.state, "secret.jpg")
	if err := os.WriteFile(secret, []byte("the root password hash"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.media, "lobby"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.media, "lobby", "notes.jpg")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("this machine cannot make a symbolic link: %v", err)
	}
	w := f.do(http.MethodGet, "/media/lobby/notes.jpg", nil)
	if w.Code == http.StatusOK {
		t.Fatalf("a link out of the media root was served: %s", w.Body)
	}

	// A link that stays inside the root is content and is served.
	real := filepath.Join(f.media, "lobby", "real.jpg")
	if err := os.WriteFile(real, []byte("picture"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = real
	inside := filepath.Join(f.media, "lobby", "alias.jpg")
	if err := os.Symlink("real.jpg", inside); err != nil {
		t.Skipf("this machine cannot make a symbolic link: %v", err)
	}
	if w := f.do(http.MethodGet, "/media/lobby/alias.jpg", nil); w.Code != http.StatusOK {
		t.Errorf("a link inside the media root gave %d", w.Code)
	}
}

// /media/ serves pictures and videos and nothing else. A denylist of names was
// wrong: an atomic write stages portapixel.toml under a name of its own, and a
// power cut leaves that file on the partition with the admin password, the WiFi
// key and the fleet token in it.
func TestMediaServesOnlyPicturesAndVideos(t *testing.T) {
	f := newFx(t)
	if err := os.MkdirAll(filepath.Join(f.media, "lobby"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"portapixel.toml":           "[web]\npassword = \"letmein\"\n",
		"portapixel.toml.tmp123456": "[web]\npassword = \"letmein\"\n",
		".hidden.jpg":               "picture",
		"lobby/playlist.toml":       "[[item]]\n",
		"lobby/notes.txt":           "text",
		"lobby/good.jpg":            "picture",
		"lobby/clip.mp4":            "video",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(f.media, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	served := map[string]bool{"lobby/good.jpg": true, "lobby/clip.mp4": true}
	for name := range files {
		w := f.do(http.MethodGet, "/media/"+name, nil)
		if served[name] {
			if w.Code != http.StatusOK {
				t.Errorf("/media/%s gave %d, want 200", name, w.Code)
			}
			continue
		}
		if w.Code == http.StatusOK {
			t.Errorf("/media/%s was served: %s", name, w.Body)
		}
	}
}

// An upload that cannot fit must be refused before it is written. One upload that
// fills PPMEDIA is a device that can save no configuration and no playlist.
func TestUploadRefusesAFileThatDoesNotFit(t *testing.T) {
	f := newFx(t)
	f.login()
	if w := f.do(http.MethodPost, "/api/playlists", map[string]string{"title": "Lobby"}); w.Code != http.StatusOK {
		t.Fatalf("create gave %d", w.Code)
	}
	w := f.do(http.MethodPost, "/api/media/lobby", nil, func(r *request) {
		r.rawBody = strings.NewReader("x")
		r.headers = map[string]string{"X-Filename": "huge.mp4", "Content-Length": "1152921504606846976"}
		r.contentLength = 1 << 60
	})
	if w.Code != http.StatusInsufficientStorage {
		t.Fatalf("an upload of 1 EB gave %d: %s", w.Code, w.Body)
	}
	mustJSON(t, w)
}

// A PUT that is refused must change nothing. encoding/json writes into the
// elements of a slice that is already there, so a body that held a schedule
// changed the rules of the running configuration under the scheduler goroutine,
// and it did that even when the body was refused with 422.
func TestPutConfigDoesNotTouchTheRunningConfig(t *testing.T) {
	f := newFx(t)
	f.login()
	f.cfg.Schedule = []config.Rule{{Playlist: "lobby", Days: []string{"mon", "tue"}, Start: "08:00", End: "18:00"}}
	before, err := json.Marshal(f.cfg)
	if err != nil {
		t.Fatal(err)
	}

	w := f.do(http.MethodPut, "/api/config", map[string]any{
		"schedule": []map[string]any{{"playlist": "other", "days": []string{}, "start": "99:99", "end": "18:00"}},
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a bad schedule gave %d: %s", w.Code, w.Body)
	}
	after, err := json.Marshal(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("the refused PUT changed the running configuration:\nbefore %s\nafter  %s", before, after)
	}
}

// A command that the browser could not take must not answer "done". The person
// looks at the screen and waits for something that will never happen.
func TestCommandReportsABusyBrowser(t *testing.T) {
	f := newFx(t)
	f.login()
	f.commandErr = browser.ErrBusy
	w := f.do(http.MethodPost, "/api/commands/restart-browser", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a busy browser gave %d: %s", w.Code, w.Body)
	}
	mustJSON(t, w)

	f.commandErr = nil
	if w := f.do(http.MethodPost, "/api/commands/restart-browser", nil); w.Code != http.StatusOK {
		t.Fatalf("a command that worked gave %d", w.Code)
	}
}

// A daemon with no player secret must answer no player call at all. Two empty
// values are equal to a plain constant-time compare, so a secret that never got
// made would have opened every player endpoint.
func TestPlayerSecretIsNeverEmpty(t *testing.T) {
	if len(NewSecret()) != SecretBytes*2 {
		t.Fatalf("the player secret is %d characters, want %d", len(NewSecret()), SecretBytes*2)
	}
	if NewSecret() == NewSecret() {
		t.Fatal("two secrets are the same")
	}

	f := newFx(t)
	f.noSecret = true
	f.rebuild()
	w := f.do(http.MethodGet, "/api/player/manifest", nil, func(r *request) { r.secret = "" })
	if w.Code != http.StatusForbidden {
		t.Fatalf("a call with no secret against a daemon with no secret gave %d", w.Code)
	}
}

// The stream of the player never ends by itself, and Shutdown does not cancel a
// request context, so every stop of the daemon cost the whole grace time.
func TestHubCloseEndsTheStreams(t *testing.T) {
	h := NewHub()
	done := make(chan struct{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/player/events", nil)
	go func() {
		h.serve(w, r)
		close(done)
	}()
	for i := 0; i < 500 && h.Listeners() == 0; i++ {
		time.Sleep(time.Millisecond)
	}
	if h.Listeners() != 1 {
		t.Fatal("the stream did not open")
	}

	h.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the stream")
	}
	h.Close() // twice must be safe: a stop can come from two paths
}

// ------------------------------------------------------- the v0.2 routes

// GET /api/media/{playlist} must list every media file of the directory and say
// which ones the playlist names. A person who copied a folder of pictures onto the
// stick from a laptop has files that no playlist names yet.
func TestGetMedia(t *testing.T) {
	f := newFx(t)
	dir := filepath.Join(f.media, "default")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("playlist.toml", "[[item]]\nfile = \"in.jpg\"\nduration = 10\n")
	write("in.jpg", "a")
	write("out.png", "bb")
	write("notes.txt", "not media")
	write(".upload-123", "a staging file")
	f.rebuild()
	f.login()

	got := body(t, f.do(http.MethodGet, "/api/media/default", nil))
	files, ok := got["files"].([]any)
	if !ok {
		t.Fatalf("files = %v", got)
	}
	state := map[string]bool{}
	for _, raw := range files {
		file := raw.(map[string]any)
		state[file["name"].(string)] = file["in_playlist"].(bool)
	}
	if len(state) != 2 {
		t.Fatalf("files = %v, want in.jpg and out.png only", state)
	}
	if !state["in.jpg"] {
		t.Error("in.jpg is not marked as part of the playlist")
	}
	if state["out.png"] {
		t.Error("out.png is marked as part of the playlist")
	}

	// A playlist that is not there gives 404 JSON, never an HTML page.
	w := f.do(http.MethodGet, "/api/media/missing", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("a playlist that is not there gave %d", w.Code)
	}
	mustJSON(t, w)
}

// An empty playlist must give [] and never null. A UI that has to test for both is
// a UI with a bug waiting in it.
func TestEmptyPlaylistGivesAnEmptyList(t *testing.T) {
	f := newFx(t)
	dir := filepath.Join(f.media, "empty")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "playlist.toml"), []byte("[playlist]\nname = \"Empty\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.rebuild()
	f.login()

	if !strings.Contains(f.do(http.MethodGet, "/api/playlists", nil).Body.String(), `"items":[]`) {
		t.Errorf("the playlist list holds no empty item array: %s", f.do(http.MethodGet, "/api/playlists", nil).Body)
	}
	if strings.Contains(f.do(http.MethodGet, "/api/playlists", nil).Body.String(), `"items":null`) {
		t.Error("the playlist list holds null in place of an empty item array")
	}
}

// The player sends its codec report with the FIRST heartbeat (D12). The daemon must
// take it and must not need it on every beat.
func TestHeartbeatCarriesTheCodecReport(t *testing.T) {
	f := newFx(t)
	withSecret := func(r *request) { r.secret = testSecret }

	first := map[string]any{
		"playlist": "default", "index": 0, "state": "playing", "frames": 1,
		"codecs": map[string]any{
			"h264": map[string]any{"1080": map[string]any{"supported": true, "smooth": true, "powerEfficient": true}},
		},
	}
	if w := f.do(http.MethodPost, "/api/player/heartbeat", first, withSecret); w.Code != http.StatusOK {
		t.Fatalf("the first heartbeat gave %d: %s", w.Code, w.Body)
	}
	if f.codecs == nil || !f.codecs["h264"]["1080"].Supported {
		t.Fatalf("the daemon got %+v", f.codecs)
	}
	if len(f.beats) != 1 || f.beats[0].Frames != 1 {
		t.Errorf("beats = %+v", f.beats)
	}

	// A later heartbeat with no report must not clear what the daemon has.
	f.codecs = nil
	second := map[string]any{"playlist": "default", "index": 1, "state": "playing", "frames": 2}
	if w := f.do(http.MethodPost, "/api/player/heartbeat", second, withSecret); w.Code != http.StatusOK {
		t.Fatalf("the second heartbeat gave %d", w.Code)
	}
	if f.codecs != nil {
		t.Errorf("a heartbeat with no report called SetCodecs with %+v", f.codecs)
	}
}

// GET /licenses serves the list from the binary when there is no copy on the
// device, and the copy on the device wins when there is one (D33).
func TestLicenses(t *testing.T) {
	f := newFx(t)
	w := f.do(http.MethodGet, "/licenses", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/licenses gave %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Third-party licences") {
		t.Errorf("the body is not the licence list: %.80s", w.Body.String())
	}
	if kind := w.Header().Get("Content-Type"); !strings.HasPrefix(kind, "text/plain") {
		t.Errorf("content type = %q; a browser must show the file and not download it", kind)
	}
}

// The install progress stream must replay its last event. The admin UI opens the
// stream after the POST answered, so a "done" event that went out first would never
// reach the page and the progress bar would stand still for ever.
func TestInstallEventsReplayTheLastEvent(t *testing.T) {
	hub := NewReplayHub()
	hub.Send("done", map[string]any{"ok": true})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/install-to-disk/events", nil)
	ctx, cancel := context.WithCancel(r.Context())
	cancel() // the stream ends at once; the replay happens before the loop
	hub.serve(w, r.WithContext(ctx))

	if !strings.Contains(w.Body.String(), "event: done") {
		t.Errorf("the stream did not replay the last event: %q", w.Body.String())
	}
}

// Reset takes the last event away, so a SECOND run of the work does not open with
// the answer of the first one.
//
// The done handler of the admin UI closes the stream and prints "The disk is ready.
// Power the machine off and take the stick out". Replayed at the start of install
// number two, that sentence tells a person to pull the stick while the device is
// writing a partition table.
func TestInstallEventsDoNotReplayTheRunBefore(t *testing.T) {
	hub := NewReplayHub()
	hub.Send("done", map[string]any{"ok": true})
	hub.Reset()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/install-to-disk/events", nil)
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	hub.serve(w, r.WithContext(ctx))

	if strings.Contains(w.Body.String(), "event: done") {
		t.Errorf("the stream replayed the done event of the run before: %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), ": connected") {
		t.Errorf("the stream did not open: %q", w.Body.String())
	}

	// The next real event still reaches a subscriber and is replayed.
	hub.Send("progress", map[string]any{"percent": 3})
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/install-to-disk/events", nil)
	ctx, cancel = context.WithCancel(r.Context())
	cancel()
	hub.serve(w, r.WithContext(ctx))
	if !strings.Contains(w.Body.String(), "event: progress") {
		t.Errorf("the stream did not replay the new event: %q", w.Body.String())
	}
}

// A 404 that a handler wrote must reach the caller as it is. A wrapper around the
// whole router replaced every one of them with "this device has no route with this
// path". A person who looked for a missing playlist was then told that the API is
// broken.
func TestAHandlerKeepsItsOwn404(t *testing.T) {
	f := newFx(t)
	f.login()

	w := f.do(http.MethodGet, "/api/media/there-is-no-such-playlist", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "no playlist with this name") {
		t.Errorf("body = %s, want the sentence of the handler", w.Body)
	}

	// A path that no route has still gives the sentence of the router.
	w = f.do(http.MethodGet, "/api/there-is-no-such-route", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("code = %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "no route with this path") {
		t.Errorf("body = %s", w.Body)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("content type = %q", got)
	}
}

// A method that a route does not take gives 405 and JSON.
func TestAMethodThatARouteDoesNotTake(t *testing.T) {
	f := newFx(t)
	f.login()

	w := f.do(http.MethodDelete, "/api/status", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "does not take this method") {
		t.Errorf("body = %s", w.Body)
	}
}

// The 403 of a route that the fleet server owns names the fields that it owns, so
// the admin UI needs no copy of that list.
func TestTheManagedRefusalNamesTheFields(t *testing.T) {
	f := newFx(t)
	f.fleet = true
	f.serverName = "Ridgeline Signage"
	f.rebuild()
	f.login()

	w := f.do(http.MethodPost, "/api/playlists", map[string]any{"title": "New"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("code = %d: %s", w.Code, w.Body)
	}
	var body struct {
		Error  string `json:"error"`
		Fields []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "managed by Ridgeline Signage" {
		t.Errorf("error = %q", body.Error)
	}
	want := syncer.ManagedFields()
	if len(body.Fields) != len(want) {
		t.Fatalf("fields = %+v, want %v", body.Fields, want)
	}
	for i, f := range body.Fields {
		if f.Field != want[i] {
			t.Errorf("field %d = %q, want %q", i, f.Field, want[i])
		}
	}
}

// A media upload on a paired device must read the JSON 403 and not a connection
// that closed in its face. Go drains only a small unread body by itself.
func TestALargeUploadOnAPairedDeviceReadsTheRefusal(t *testing.T) {
	f := newFx(t)
	f.fleet = true
	f.serverName = "Ridgeline Signage"
	f.rebuild()
	f.login()

	big := bytes.Repeat([]byte("x"), 5<<20)
	w := f.do(http.MethodPost, "/api/media/default?filename=a.jpg", nil, func(r *request) {
		r.rawBody = bytes.NewReader(big)
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("code = %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "managed by") {
		t.Errorf("body = %s", w.Body)
	}
}

// GET /api/pair carries the fields that the fleet server owns while the device is
// paired. The admin UI disables exactly these.
func TestPairStateCarriesTheManagedFields(t *testing.T) {
	f := newFx(t)
	f.fleet = true
	f.serverName = "Ridgeline Signage"
	f.pairState = PairState{Status: syncer.StatusPaired, ManagedFields: syncer.ManagedFields()}
	f.rebuild()
	f.login()

	w := f.do(http.MethodGet, "/api/pair", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body)
	}
	var body PairState
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.ManagedFields) != len(syncer.ManagedFields()) {
		t.Errorf("managed_fields = %v", body.ManagedFields)
	}
}

// POST /api/pair on a device that is paired answers 409, so the UI can send the
// person to the Unpair button.
func TestPairOnAPairedDeviceAnswers409(t *testing.T) {
	f := newFx(t)
	f.fleet = true
	f.pairErr = syncer.ErrAlreadyPaired{Server: "Ridgeline Signage"}
	f.rebuild()
	f.login()

	w := f.do(http.MethodPost, "/api/pair", map[string]any{"url": "https://other.example.com"})
	if w.Code != http.StatusConflict {
		t.Fatalf("code = %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "unpair it first") {
		t.Errorf("body = %s", w.Body)
	}
}
