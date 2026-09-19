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
	"github.com/ethanpil/portapixel/internal/device/scheduler"
	"github.com/ethanpil/portapixel/internal/opslog"
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
