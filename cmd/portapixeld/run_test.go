package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/browser"
	"github.com/ethanpil/portapixel/internal/device/httpd"
	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/device/scheduler"
	"github.com/ethanpil/portapixel/internal/device/syncer"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/updater"
	"github.com/ethanpil/portapixel/internal/version"
	"slices"
)

// A rename of a playlist must correct every place that names it. The schedule
// rules and playback.default_playlist hold the directory name, so without this a
// rename left the rules pointing at a directory that is not there: the rule
// matched nothing, the default playlist was gone, and the screen showed the
// fallback picture with no word about why.
func TestRenamePlaylistRefs(t *testing.T) {
	tests := []struct {
		name        string
		defaultName string
		rules       []string
		wantDefault string
		wantRules   []string
		wantCount   int
	}{
		{
			name:        "the default playlist and two rules",
			defaultName: "lobby",
			rules:       []string{"lobby", "night", "lobby"},
			wantDefault: "front-desk",
			wantRules:   []string{"front-desk", "night", "front-desk"},
			wantCount:   3,
		},
		{
			name:        "only a rule",
			defaultName: "default",
			rules:       []string{"lobby"},
			wantDefault: "default",
			wantRules:   []string{"front-desk"},
			wantCount:   1,
		},
		{
			name:        "nothing names it",
			defaultName: "default",
			rules:       []string{"night"},
			wantDefault: "default",
			wantRules:   []string{"night"},
			wantCount:   0,
		},
		{
			name:        "no rules at all",
			defaultName: "lobby",
			rules:       nil,
			wantDefault: "front-desk",
			wantRules:   nil,
			wantCount:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Playback.DefaultPlaylist = tt.defaultName
			for _, r := range tt.rules {
				cfg.Schedule = append(cfg.Schedule, config.Rule{Playlist: r, Start: "08:00", End: "18:00"})
			}

			got, count := renamePlaylistRefs(cfg, "lobby", "front-desk")
			if count != tt.wantCount {
				t.Errorf("count = %d, want %d", count, tt.wantCount)
			}
			if got.Playback.DefaultPlaylist != tt.wantDefault {
				t.Errorf("default_playlist = %q, want %q", got.Playback.DefaultPlaylist, tt.wantDefault)
			}
			if len(got.Schedule) != len(tt.wantRules) {
				t.Fatalf("rules = %+v, want %v", got.Schedule, tt.wantRules)
			}
			for i, want := range tt.wantRules {
				if got.Schedule[i].Playlist != want {
					t.Errorf("rule %d = %q, want %q", i, got.Schedule[i].Playlist, want)
				}
			}
			// The rules that came in must not change: they share an array with the
			// configuration that the scheduler reads.
			for i, want := range tt.rules {
				if cfg.Schedule[i].Playlist != want {
					t.Errorf("the rename changed rule %d of the configuration that runs", i)
				}
			}
		})
	}
}

// A new playlist name must be in the file and not only in memory, and the result
// must still pass the validator.
func TestRenamePlaylistRefsSaves(t *testing.T) {
	media, state := t.TempDir(), t.TempDir()
	cfg := config.Default()
	cfg.Playback.DefaultPlaylist = "lobby"
	cfg.Schedule = []config.Rule{{Playlist: "lobby", Start: "08:00", End: "18:00"}}
	if err := config.Save(media, state, cfg); err != nil {
		t.Fatal(err)
	}

	updated, count := renamePlaylistRefs(cfg, "lobby", "front-desk")
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	if err := config.Save(media, state, updated); err != nil {
		t.Fatalf("the corrected configuration does not pass the validator: %v", err)
	}
	data, err := os.ReadFile(config.MediaPath(media))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"lobby"`) {
		t.Errorf("the saved file still names the old playlist:\n%s", data)
	}
	if !strings.Contains(string(data), `"front-desk"`) {
		t.Errorf("the saved file does not name the new playlist:\n%s", data)
	}
}

// A live reload is not a start. A file with one bad value must keep the settings
// that run: config.Load would fall back to the shadow copy or to the factory
// defaults, and a hand edit would then take the device back to the settings of
// last week, or to DHCP.
func TestReloadConfigKeepsTheRunningSettings(t *testing.T) {
	media, state := t.TempDir(), t.TempDir()
	good := config.Default()
	good.Device.Name = "Lobby north"
	good.Network.WifiSSID = "Office"
	good.Network.WifiPSK = "a long enough key"
	if err := config.Save(media, state, good); err != nil {
		t.Fatal(err)
	}

	d := &daemon{
		paths: paths{media: media, state: state},
		log:   opslog.New(filepath.Join(state, opsLogName)),
		cfg:   good,
		hub:   httpd.NewHub(),
	}
	d.sched = scheduler.New(scheduler.Options{Config: d.config, Log: d.log})
	d.sup = browser.New(browser.Options{
		Command: browser.CommandConfig{Override: browser.DisableCommand},
		Log:     d.log,
	})

	// A file that does not parse.
	if err := os.WriteFile(config.MediaPath(media), []byte("[device\nname = "), 0o644); err != nil {
		t.Fatal(err)
	}
	d.reloadConfig(configMTime(media))
	if got := d.config().Device.Name; got != "Lobby north" {
		t.Fatalf("a file that does not parse changed the running name to %q", got)
	}
	if got := d.config().Network.WifiPSK; got != "a long enough key" {
		t.Fatalf("a file that does not parse lost the WiFi key")
	}
	if w := d.configView().Warning; !strings.Contains(w, "portapixel.toml has an error") {
		t.Errorf("warning = %q", w)
	}

	// A file that parses but breaks a rule.
	if err := os.WriteFile(config.MediaPath(media), []byte("[display]\nrotation = 45\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.reloadConfig(configMTime(media).Add(time.Second))
	if got := d.config().Device.Name; got != "Lobby north" {
		t.Fatalf("a bad value changed the running name to %q", got)
	}
	if w := d.configView().Warning; !strings.Contains(w, "last good settings") {
		t.Errorf("warning = %q", w)
	}

	// A file that is good is taken.
	good.Device.Name = "Front desk"
	if err := config.Save(media, state, good); err != nil {
		t.Fatal(err)
	}
	d.reloadConfig(configMTime(media).Add(2 * time.Second))
	if got := d.config().Device.Name; got != "Front desk" {
		t.Fatalf("a good file was not taken: name = %q", got)
	}
	if w := d.configView().Warning; w != "" {
		t.Errorf("a good file left the warning %q", w)
	}
}

// provision must never write the configuration back when config.Load repaired it
// or fell back. That write cost the user their WiFi key and their password for one
// character that the parser did not like.
func TestRefreshConfigIDKeepsAFileThatHasAFault(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		keepsID bool
	}{
		{"a file that does not parse", "[device\nname = ", false},
		{"a file with one bad value", "[device]\nid = \"px-00000000\"\nname = \"Lobby\"\n[display]\nrotation = 45\n", false},
		{"a file that is good", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			media, state := t.TempDir(), t.TempDir()
			log := opslog.New(filepath.Join(state, opsLogName))

			body := tt.file
			if body == "" {
				cfg := config.Default()
				cfg.Device.ID = "px-00000000"
				cfg.Network.WifiSSID = "Office"
				cfg.Network.WifiPSK = "a long enough key"
				body = string(config.Render(cfg))
			}
			if err := os.WriteFile(config.MediaPath(media), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := refreshConfigID(media, state, "px-11111111", log); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(config.MediaPath(media))
			if err != nil {
				t.Fatal(err)
			}
			if tt.keepsID {
				if !strings.Contains(string(after), "px-11111111") {
					t.Errorf("a good file did not take the new id:\n%s", after)
				}
				if !strings.Contains(string(after), "a long enough key") {
					t.Errorf("the WiFi key is gone:\n%s", after)
				}
				return
			}
			if string(after) != body {
				t.Errorf("the file changed:\nbefore %q\nafter  %q", body, after)
			}
			lines := ""
			for _, e := range log.Tail(50) {
				lines += e.Event + " "
			}
			if !strings.Contains(lines, "provision.config.kept") {
				t.Errorf("no provision.config.kept line: %s", lines)
			}
		})
	}
}

// A hand edit of portapixel.toml on a paired device must not take a field that the
// fleet server owns (D48). The other fields of the same edit apply, the file of the
// person is never rewritten, and a warning names the field.
func TestKeepManagedHoldsTheFleetFields(t *testing.T) {
	old := config.Default()
	old.Playback.DefaultPlaylist = "lobby"
	old.Display.OnTime = "07:30"
	old.Display.OffTime = "22:00"
	old.Server.URL = "https://signage.example.com"
	old.Device.Name = "Lobby north"

	next := old
	next.Playback.DefaultPlaylist = "the hand edit"
	next.Display.OnTime = "05:00"
	next.Server.URL = "https://other.example.com"
	next.Device.Name = "Front desk" // local: this one applies
	next.Display.Rotation = 90      // local: this one applies

	d := &daemon{}
	// A standalone device takes everything.
	got, kept := d.keepManaged(old, next)
	if len(kept) != 0 || got.Playback.DefaultPlaylist != "the hand edit" {
		t.Fatalf("a standalone device did not take the edit: kept=%v", kept)
	}

	// A paired device keeps the managed fields and the pairing fields.
	d.sync = syncer.New(syncer.Options{
		Config: func() config.Config { return old },
		State: func() identity.State {
			return identity.State{DeviceToken: "t", ServerURL: old.Server.URL}
		},
	})
	got, kept = d.keepManaged(old, next)
	if got.Playback.DefaultPlaylist != "lobby" {
		t.Errorf("the default playlist became %q", got.Playback.DefaultPlaylist)
	}
	if got.Display.OnTime != "07:30" {
		t.Errorf("the screen on time became %q", got.Display.OnTime)
	}
	if got.Server.URL != "https://signage.example.com" {
		t.Errorf("the server address became %q", got.Server.URL)
	}
	if got.Device.Name != "Front desk" || got.Display.Rotation != 90 {
		t.Errorf("a local field of the same edit was not taken: %+v", got.Device)
	}
	want := []string{"playback.default_playlist", "display.on_time", "server.url"}
	for _, field := range want {
		if !slices.Contains(kept, field) {
			t.Errorf("%s is not in the list that the warning names: %v", field, kept)
		}
	}
}

// The two pairing fields change through /api/pair only. A settings save that sends
// them back as they are must still pass.
func TestPairingFields(t *testing.T) {
	for _, field := range []string{"server.url", "server.token"} {
		if !pairingField(field) {
			t.Errorf("%s must belong to the pairing", field)
		}
	}
	for _, field := range []string{"server.poll_seconds", "device.name", "web.password"} {
		if pairingField(field) {
			t.Errorf("%s must stay with the local admin", field)
		}
	}
}

// The daemon side of the health gate protocol (plan section 15).
//
// One write of the marker was a race that rolled a GOOD release back: the gate
// removed a stale marker after both had started, so a daemon that was quick lost
// its marker and nothing wrote it again. health-gate.sh removes a stale marker in
// start_pre now, and the daemon writes the marker again every few seconds for as
// long as .swap-pending names its release.
func TestHealthMarkerComesBackWhileASwapIsPending(t *testing.T) {
	old := markerReassert
	markerReassert = 50 * time.Millisecond
	defer func() { markerReassert = old }()

	releases, state := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(releases, updater.HealthDir), 0o755); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(releases, updater.PendingFile)
	marker := filepath.Join(releases, updater.HealthDir, version.Version+updater.OKSuffix)
	if err := os.WriteFile(pending, []byte(version.Version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &daemon{
		paths: paths{releases: releases, state: state},
		log:   opslog.New(filepath.Join(state, opsLogName)),
	}
	// A daemon with no browser counts as up, which is what --browser-cmd none does.
	d.sup = browser.New(browser.Options{
		Command: browser.CommandConfig{Override: browser.DisableCommand},
		Log:     d.log,
	})

	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() { d.writeHealthMarker(done); close(stopped) }()
	defer close(done)

	body := waitForMarker(t, marker, "the first health marker")
	// The content names the run that wrote it, so a marker of an earlier boot can
	// be told apart from this one.
	for _, want := range []string{"version=" + version.Version, "pid=", "start="} {
		if !strings.Contains(body, want) {
			t.Errorf("the marker holds %q and must name %s", body, want)
		}
	}

	// Anything at all takes the marker away. It must come back.
	removeMarker(t, marker)
	waitForMarker(t, marker, "the health marker again")

	// The pending marker goes away, which is what the gate does when it passes.
	// The daemon then stops writing: a device in its steady state writes nothing
	// to the flash (D2).
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon still writes the health marker with no pending update")
	}
	removeMarker(t, marker)
	time.Sleep(10 * markerReassert)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the daemon wrote the health marker again after the update passed")
	}
}

// removeMarker takes the health marker away. It tries again for a moment: on
// Windows a file that the daemon writes at this instant cannot be removed.
func removeMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := os.Remove(path); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("cannot remove %s: %v", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForMarker waits for the health marker and gives its content.
func waitForMarker(t *testing.T, path, what string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := os.ReadFile(path); err == nil && len(body) > 0 {
			return string(body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return ""
}
