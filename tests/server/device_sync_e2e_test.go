package server_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/device/syncer"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/store"
)

// TestDeviceSyncsFromTheRealServer drives the real fleet client against the real
// device routes of the server: enroll with a token, sync a playlist of two objects,
// find the files under _fleet, see the heartbeat server-side, and see a queued
// command delivered and acknowledged.
//
// The other tests of this package drive the routes with a hand-built request. This
// one drives them with the code of the device, so a change of the wire format on
// one end fails here and not in the field.
func TestDeviceSyncsFromTheRealServer(t *testing.T) {
	// newFleet of the harness builds the route stack of the binary,
	// internal/server.New, so this test drives the handler that ships.
	server := newFleet(t)

	// ---- the content of the server ----
	first := addObject(t, server, "welcome.jpg", "the picture of the lobby")
	second := addObject(t, server, "promo.mp4", strings.Repeat("video bytes ", 64))

	playlistID, err := server.db.SavePlaylist(db.Playlist{
		Title:      "Lobby loop",
		Transition: "crossfade",
		Items: []db.PlaylistItem{
			// The name carries the extension, which is what says image or video.
			{SHA256: first, Name: "welcome.jpg", Duration: 15},
			{SHA256: second, Name: "promo.mp4", MaxDuration: 60},
			{URL: "https://dash.example.com/board", Name: "dash", Duration: 30},
		},
	})
	if err != nil {
		t.Fatalf("save the playlist: %v", err)
	}

	// ---- the device ----
	device := newSyncDevice(t, server)

	// A fleet enrollment token in auto mode: one token on any number of cards
	// (D25).
	_, token, err := server.db.CreateEnrollToken("every card", "auto", 0, time.Time{}, 0)
	if err != nil {
		t.Fatalf("make the enrollment token: %v", err)
	}
	state, err := device.sync.Pair(context.Background(), server.srv.URL, token, false)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if state.Status != syncer.StatusPaired {
		t.Fatalf("the pairing status is %q", state.Status)
	}

	// The server holds a row for this device now, and the playlist goes to it.
	row, err := server.db.Device(device.id.DeviceID)
	if err != nil {
		t.Fatalf("the server has no row for the device: %v", err)
	}
	if err := server.db.SetDeviceOverrides(row.ID, playlistID, "", "", ""); err != nil {
		t.Fatalf("assign the playlist: %v", err)
	}
	commandID, err := server.db.QueueCommand(row.ID, "rescan", nil)
	if err != nil {
		t.Fatalf("queue the command: %v", err)
	}

	// ---- one poll ----
	if next := device.sync.Once(context.Background()); next <= 0 {
		t.Fatalf("the wait after the poll is %v", next)
	}
	if got := device.sync.Report().SyncError; got != "" {
		t.Fatalf("the sync failed: %s", got)
	}

	// The objects are in the store of the device, named by their hash (D24).
	for sha, content := range map[string]string{
		first:  "the picture of the lobby",
		second: strings.Repeat("video bytes ", 64),
	} {
		name, err := store.ObjectName(sha, "any")
		if err != nil {
			t.Fatal(err)
		}
		matches, err := filepath.Glob(filepath.Join(device.media, "_fleet", "media", name[:9]+"*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("the store holds %v for %s (%v)", matches, sha[:8], err)
		}
		data, err := os.ReadFile(matches[0])
		if err != nil || string(data) != content {
			t.Errorf("%s holds %d bytes (%v)", filepath.Base(matches[0]), len(data), err)
		}
	}

	// The fleet playlist is on the card and the library serves it.
	text, err := os.ReadFile(filepath.Join(device.media, "_fleet", "lobby-loop", "playlist.toml"))
	if err != nil {
		t.Fatalf("read the fleet playlist: %v", err)
	}
	if !strings.Contains(string(text), `file = "../media/`+first[:8]) {
		t.Errorf("the fleet playlist is:\n%s", text)
	}
	if !strings.Contains(string(text), `url = "https://dash.example.com/board"`) {
		t.Errorf("the URL item is missing:\n%s", text)
	}
	p, ok := device.lib.Snapshot().Find("lobby-loop")
	if !ok {
		t.Fatal("the library does not serve the fleet playlist")
	}
	if len(p.Items) != 3 {
		t.Errorf("the library read %d items", len(p.Items))
	}
	for _, item := range p.Items {
		if item.Missing {
			t.Errorf("the item %q is missing on the card", item.Name)
		}
	}
	if device.fleetDefault != "lobby-loop" {
		t.Errorf("the scheduler got the default playlist %q", device.fleetDefault)
	}

	// The heartbeat reached the server, and the command is acknowledged.
	row, err = server.db.Device(device.id.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if row.LastSeen.IsZero() {
		t.Error("the server recorded no contact")
	}
	if !strings.Contains(row.Status, `"device_id"`) {
		t.Errorf("the server kept the status %q", row.Status)
	}
	if row.Version != "1.5.0-test" {
		t.Errorf("the server kept the version %q", row.Version)
	}
	if row.HardwareID != device.id.HardwareID {
		t.Errorf("the server kept the hardware ID %q", row.HardwareID)
	}

	commands, err := server.db.Commands(row.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].ID != commandID {
		t.Fatalf("the server holds %d commands", len(commands))
	}
	if commands[0].State != db.CommandAcked {
		t.Errorf("the command state is %q, want %q", commands[0].State, db.CommandAcked)
	}
	if device.commands == nil || device.commands[0] != "rescan" {
		t.Errorf("the device ran %v", device.commands)
	}

	// A second poll with no change writes nothing and downloads nothing.
	before := device.rescans
	if got := device.sync.Once(context.Background()); got <= 0 {
		t.Errorf("the wait after the second poll is %v", got)
	}
	if device.rescans != before {
		t.Errorf("the second poll rescanned %d times", device.rescans-before)
	}
}

// addObject puts one file in the media store and in the media table, the way an
// upload of the admin UI does.
func addObject(t *testing.T, f *fleet, name, content string) string {
	t.Helper()
	res, err := f.store.Put(bytes.NewReader([]byte(content)), name, int64(len(content)))
	if err != nil {
		t.Fatalf("store %s: %v", name, err)
	}
	err = f.db.AddMedia(db.Media{
		SHA256: res.SHA256, OrigName: name, Size: res.Size, MIME: res.MIME,
		Width: res.Width, Height: res.Height, HasThumb: res.HasThumb,
	})
	if err != nil {
		t.Fatalf("record %s: %v", name, err)
	}
	return res.SHA256
}

// ------------------------------------------------------------------ the device

// syncDevice is a device: real directories, a real library and the real fleet
// client. Only the parts that the daemon owns are fakes, and each of them records
// what the client asked for.
type syncDevice struct {
	media string
	state string
	id    identity.Identity
	lib   *library.Library
	sync  *syncer.Syncer

	st  identity.State
	cfg config.Config

	rescans      int
	commands     []string
	fleetDefault string
	fleetRules   []manifest.Rule
}

func newSyncDevice(t *testing.T, server *fleet) *syncDevice {
	t.Helper()
	root := t.TempDir()
	d := &syncDevice{
		media: filepath.Join(root, "media"),
		state: filepath.Join(root, "state"),
		id:    identity.Derive("/"),
		cfg:   config.Default(),
	}
	for _, dir := range []string{d.media, d.state} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d.cfg.Device.Name = "Lobby screen"
	d.cfg.Server.URL = server.srv.URL

	log := opslog.New(filepath.Join(d.state, "ops.log"))
	d.lib = library.New(library.Options{
		MediaRoot: d.media,
		StateDir:  d.state,
		Log:       log,
		Paired:    func() bool { return d.st.Paired() },
	})
	d.lib.Rescan()

	d.sync = syncer.New(syncer.Options{
		MediaRoot: d.media,
		Log:       log,
		Identity:  d.id,
		Version:   "1.5.0-test",
		Config:    func() config.Config { return d.cfg },
		State:     func() identity.State { return d.st },
		SaveState: func(change func(*identity.State)) error {
			change(&d.st)
			return d.st.Save(d.state)
		},
		SaveName: func(name string) error {
			d.cfg.Device.Name = name
			return nil
		},
		Status: func() manifest.Status {
			return manifest.Status{DeviceID: d.id.DeviceID, Name: d.cfg.Device.Name}
		},
		SetFleetRules: func(def string, rules []manifest.Rule, _ *manifest.ScreenRule) {
			d.fleetDefault, d.fleetRules = def, rules
		},
		ClearFleetRules: func() { d.fleetDefault, d.fleetRules = "", nil },
		Rescan:          func() { d.rescans++; d.lib.Rescan() },
		Command:         func(name string) error { d.commands = append(d.commands, name); return nil },
		CachedSHA:       d.lib.CachedSHA,
		FindSHA:         d.lib.FindSHA,
		NoteSHA:         d.lib.NoteSHA,
	})
	return d
}
