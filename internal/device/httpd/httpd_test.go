package httpd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/browser"
	"github.com/ethanpil/portapixel/internal/device/library"
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

	cfg       config.Config
	saved     *config.Config
	applied   Applied
	commands  []string
	rootPW    string
	beats     []browser.Heartbeat
	urlSkip   bool
	urlIndex  int
	readyHits int
	cookie    *http.Cookie
}

func newFx(t *testing.T) *fx {
	t.Helper()
	f := &fx{t: t, media: t.TempDir(), state: t.TempDir(), hub: NewHub()}
	f.cfg = config.Default()
	f.cfg.Web.Password = "letmein"
	f.cfg.Network.WifiSSID = "Office"
	f.cfg.Network.WifiPSK = "a secret"

	log := opslog.New(filepath.Join(f.state, "ops.log"))
	lib := library.New(library.Options{MediaRoot: f.media, StateDir: f.state, Log: log})
	lib.Rescan()

	f.h = New(Deps{
		MediaRoot:    f.media,
		Log:          log,
		Library:      lib,
		Sessions:     httpguard.NewSessions(),
		Limiter:      httpguard.NewLimiter(),
		Hub:          f.hub,
		PlayerSecret: testSecret,
		Hosts:        func() []string { return []string{"localhost", "127.0.0.1", "lobby.local"} },
		Password:     func() string { return f.cfg.Web.Password },
		Status: func(loopback bool) manifest.Status {
			st := manifest.Status{DeviceID: "px-1a2b3c4d", Name: f.cfg.Device.Name}
			if loopback {
				st.PairingCode = "H7K2QX"
			}
			return st
		},
		Config: func() ConfigView { return ConfigView{Config: f.cfg.Masked()} },
		SaveConfig: func(incoming config.Config) (Applied, error) {
			merged := config.MergeMasked(f.cfg, incoming)
			if errs := merged.Validate(); len(errs) > 0 {
				return Applied{}, errs
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
			return nil
		},
		AdminURL:        func() string { return "http://lobby.local/" },
		SetRootPassword: func(pw string) error { f.rootPW = pw; return nil },
	})
	return f
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
		{http.MethodPost, "/api/pair"},
		{http.MethodDelete, "/api/pair"},
		{http.MethodPost, "/api/update/check"},
		{http.MethodPost, "/api/update/apply"},
		{http.MethodGet, "/api/disks"},
		{http.MethodPost, "/api/install-to-disk"},
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
	bad := []string{"", "/etc/shadow", "../x", "default/../../x", `default\x`, "C:/x", "a\x00b"}
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
