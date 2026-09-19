package httpd

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethanpil/portapixel/internal/device/syncer"
)

// writeUnder writes one file under a root and makes the directories on the way.
func writeUnder(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fleetFx gives a fixture with the fleet client wired and a session.
func fleetFx(t *testing.T, serverName string) *fx {
	t.Helper()
	f := newFx(t)
	f.fleet = true
	f.serverName = serverName
	f.rebuild()
	f.login()
	return f
}

func TestGetPairGivesTheState(t *testing.T) {
	f := fleetFx(t, "Ridgeline Signage")
	f.pairState = PairState{
		Status:      syncer.StatusPending,
		ServerURL:   "https://signage.example.com",
		ServerName:  "Ridgeline Signage",
		PairingCode: "K7M2QP",
	}

	w := f.do(http.MethodGet, "/api/pair", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/pair gave %d: %s", w.Code, w.Body)
	}
	got := body(t, w)
	for key, want := range map[string]string{
		"status":       syncer.StatusPending,
		"server_url":   "https://signage.example.com",
		"server_name":  "Ridgeline Signage",
		"pairing_code": "K7M2QP",
	} {
		if got[key] != want {
			t.Errorf("%s is %v, want %q", key, got[key], want)
		}
	}
}

func TestPostPairPassesTheAddressAndTheToken(t *testing.T) {
	f := fleetFx(t, "")
	f.pairState = PairState{Status: syncer.StatusPaired, ServerURL: "https://signage.example.com"}

	w := f.do(http.MethodPost, "/api/pair", map[string]string{
		"url": "https://signage.example.com", "token": "an enrollment token",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/pair gave %d: %s", w.Code, w.Body)
	}
	if f.pairURL != "https://signage.example.com" || f.pairToken != "an enrollment token" {
		t.Errorf("the daemon got %q and %q", f.pairURL, f.pairToken)
	}
	if body(t, w)["status"] != syncer.StatusPaired {
		t.Errorf("the answer is %s", w.Body)
	}
}

func TestPostPairAnswersTheKindOfFault(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"a bad address is a fault of the request", syncer.BadURL{Reason: "no"}, http.StatusUnprocessableEntity},
		{"a server that did not answer is a gateway fault", errors.New("no route to host"), http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := fleetFx(t, "")
			f.pairErr = tt.err
			w := f.do(http.MethodPost, "/api/pair", map[string]string{"url": "http://a"})
			if w.Code != tt.want {
				t.Errorf("gave %d, want %d: %s", w.Code, tt.want, w.Body)
			}
			mustJSON(t, w)
		})
	}
}

func TestDeletePairUnpairs(t *testing.T) {
	f := fleetFx(t, "Ridgeline Signage")
	w := f.do(http.MethodDelete, "/api/pair", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /api/pair gave %d: %s", w.Code, w.Body)
	}
	if f.unpaired != 1 {
		t.Errorf("the daemon unpaired %d times", f.unpaired)
	}
}

// TestUnknownAPIPathAnswersJSON covers the rule that every failure under /api/
// gives {"error": "..."}. web/shared/api.js reads that field, and the text page of
// http.ServeMux made a wrong path look like a broken build.
func TestUnknownAPIPathAnswersJSON(t *testing.T) {
	f := newFx(t)
	f.login()
	tests := []struct {
		name, method, path string
		want               int
	}{
		{"a path that no route has", http.MethodGet, "/api/nothing", http.StatusNotFound},
		{"a method that the route does not take", http.MethodPost, "/api/opslog", http.StatusMethodNotAllowed},
		{"a path under a route that takes a name", http.MethodGet, "/api/playlists/a/b/c", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := f.do(tt.method, tt.path, nil)
			if w.Code != tt.want {
				t.Errorf("%s %s gave %d, want %d: %s", tt.method, tt.path, w.Code, tt.want, w.Body)
			}
			mustJSON(t, w)
			if body(t, w)["error"] == nil {
				t.Errorf("the answer holds no error field: %s", w.Body)
			}
		})
	}
}

// TestFleetLockTable walks every route that changes something and says which ones
// the fleet server owns while the device is paired (D48, plan section 13).
//
// The table is the contract. A new route that changes content must be added here
// and to the guard in the same commit, or the local admin and the server both write
// the playlists of one screen.
func TestFleetLockTable(t *testing.T) {
	makeFile := func(f *fx) {
		writeUnder(t, f.media, "default/playlist.toml", "[[item]]\nfile = \"a.jpg\"\n")
		writeUnder(t, f.media, "default/a.jpg", "x")
	}

	tests := []struct {
		name         string
		method, path string
		body         any
		locked       bool
	}{
		// The fleet server owns the content and the playlists.
		{"create a playlist", http.MethodPost, "/api/playlists", map[string]string{"title": "New"}, true},
		{"save a playlist", http.MethodPut, "/api/playlists/default", map[string]any{"title": "D"}, true},
		{"rename a playlist", http.MethodPost, "/api/playlists/default/rename", map[string]string{"title": "D"}, true},
		{"delete a playlist", http.MethodDelete, "/api/playlists/default", nil, true},
		{"upload media", http.MethodPost, "/api/media/default", nil, true},
		{"delete media", http.MethodDelete, "/api/media/default/a.jpg", nil, true},

		// Reading is never locked: the local admin must see what plays.
		{"list playlists", http.MethodGet, "/api/playlists", nil, false},
		{"list the files of a playlist", http.MethodGet, "/api/media/default", nil, false},
		{"read the configuration", http.MethodGet, "/api/config", nil, false},
		{"read the ops log", http.MethodGet, "/api/opslog", nil, false},
		{"rescan", http.MethodPost, "/api/rescan", nil, false},

		// The manual commands stay with the local admin (plan section 13).
		{"screen on", http.MethodPost, "/api/commands/screen-on", nil, false},
		{"restart the browser", http.MethodPost, "/api/commands/restart-browser", nil, false},

		// The local Apply button still works while paired: [updates] auto decides
		// if an update waits for the fleet command or not (D28).
		{"check for an update", http.MethodPost, "/api/update/check", nil, false},

		// Unpairing must work while paired. It is the way out.
		{"unpair", http.MethodDelete, "/api/pair", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Paired: a locked route answers 403 with the name of the server.
			paired := fleetFx(t, "Ridgeline Signage")
			makeFile(paired)
			paired.do(http.MethodPost, "/api/rescan", nil)
			w := paired.do(tt.method, tt.path, tt.body)
			switch {
			case tt.locked && w.Code != http.StatusForbidden:
				t.Fatalf("%s %s gave %d while paired, want 403: %s", tt.method, tt.path, w.Code, w.Body)
			case tt.locked:
				mustJSON(t, w)
				if got := body(t, w)["error"]; got != "managed by Ridgeline Signage" {
					t.Errorf("the message is %v", got)
				}
			case w.Code == http.StatusForbidden:
				t.Fatalf("%s %s gave 403 while paired and must not: %s", tt.method, tt.path, w.Body)
			}

			// Standalone: no route is locked.
			alone := fleetFx(t, "")
			makeFile(alone)
			alone.do(http.MethodPost, "/api/rescan", nil)
			if w := alone.do(tt.method, tt.path, tt.body); w.Code == http.StatusForbidden {
				t.Fatalf("%s %s gave 403 on a standalone device: %s", tt.method, tt.path, w.Body)
			}
		})
	}
}

// TestFleetLockOfTheConfigurationFields proves the other half of D48: the fleet
// server owns some fields of PUT /api/config and the local admin owns the rest.
func TestFleetLockOfTheConfigurationFields(t *testing.T) {
	tests := []struct {
		name   string
		body   map[string]any
		locked bool
	}{
		{"the default playlist", map[string]any{"playback": map[string]any{"default_playlist": "other"}}, true},
		{"the transition", map[string]any{"playback": map[string]any{"transition": "cut"}}, true},
		{"the screen on time", map[string]any{"display": map[string]any{"on_time": "07:30", "off_time": "22:00"}}, true},
		{"automatic updates", map[string]any{"updates": map[string]any{"auto": true}}, true},
		{"the schedule", map[string]any{"schedule": []map[string]any{{"playlist": "default"}}}, true},

		{"the rotation", map[string]any{"display": map[string]any{"rotation": 90}}, false},
		{"the audio output", map[string]any{"audio": map[string]any{"output": "hdmi"}}, false},
		{"the device name", map[string]any{"device": map[string]any{"name": "Lobby"}}, false},
		{"the time zone", map[string]any{"device": map[string]any{"timezone": "Europe/Berlin"}}, false},
		{"the output mode", map[string]any{"display": map[string]any{"video_mode": "1920x1080@60"}}, false},
		{"the web password", map[string]any{"web": map[string]any{"password": "a new password"}}, false},
		{"ssh", map[string]any{"ssh": map[string]any{"enabled": false}}, false},
		{"logging", map[string]any{"logging": map[string]any{"persist": true}}, false},
		{"the network", map[string]any{"network": map[string]any{"wifi_ssid": "Office 2"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := fleetFx(t, "Ridgeline Signage")
			w := f.do(http.MethodPut, "/api/config", tt.body)
			switch {
			case tt.locked && w.Code != http.StatusForbidden:
				t.Errorf("gave %d while paired, want 403: %s", w.Code, w.Body)
			case !tt.locked && w.Code != http.StatusOK:
				t.Errorf("gave %d while paired, want 200: %s", w.Code, w.Body)
			}
		})
	}
}
