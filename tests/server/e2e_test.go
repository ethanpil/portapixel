package server_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/store"
)

// TestEndToEnd is the whole job of the server in one test, in the order that a
// person does it:
//
//	make a group, upload a picture, make a playlist, give it to the group,
//	enroll a screen, poll the manifest, download the picture, send a heartbeat,
//	queue a command, see it delivered and acknowledged.
//
// The client of the download is internal/store.Download, the code that the device
// runs, so this test also proves that the two ends of the protocol agree.
func TestEndToEnd(t *testing.T) {
	f := newFleet(t)
	f.login()

	// 1. A group.
	group := f.makeGroup("Lobby")

	// 2. A picture in the library.
	picture := imageBytes(t, 1280, 720)
	sha := f.uploadMedia("welcome-autumn.png", picture)

	// 3. A playlist with the picture and a web page.
	playlist := f.makePlaylist("Lobby loop",
		map[string]any{"sha256": sha, "name": "welcome-autumn.png", "duration": 15},
		map[string]any{"url": "https://dash.example.com/board", "duration": 60, "refresh_seconds": 300},
	)

	// 4. The group plays it, by default and by a rule, and its screens go off at
	// night.
	f.mustOK(f.adminCall(http.MethodPut, "/api/admin/groups/"+itoa(group), map[string]any{
		"name": "Lobby", "default_playlist_id": playlist,
		"screen_on": "07:00", "screen_off": "22:00",
		"screen_days": []string{"mon", "tue", "wed", "thu", "fri"},
	}), "set up the group")
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/assignments", map[string]any{
		"group_id": group, "playlist_id": playlist,
		"days":  []string{"mon", "tue", "wed", "thu", "fri"},
		"start": "08:00", "end": "18:00", "priority": 10,
	}), "add a time rule")

	// 5. An invite token, and a screen that enrolls itself with it.
	token := f.makeToken("auto", 50, "")
	out, res := f.enroll("px-e2e00001", token)
	f.mustOK(res, "the enroll of the screen")
	if out.Status != "paired" || out.DeviceToken == "" {
		t.Fatalf("the enroll gave %+v", out)
	}
	deviceToken := out.DeviceToken

	// The screen lands in the group of the token? No: this token has no group, so
	// the admin puts it in one.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/px-e2e00001/group",
		map[string]int64{"group_id": group}), "move the screen into the group")
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/px-e2e00001/rename",
		map[string]string{"name": "Lobby North"}), "rename the screen")

	// 6. The screen polls its manifest.
	m := f.getManifest(deviceToken)
	if m.ServerName != "Test fleet" || m.PollSeconds != 60 {
		t.Fatalf("the manifest header is %+v", m)
	}
	if m.DefaultPlaylist != "lobby-loop" {
		t.Fatalf("the default playlist is %q", m.DefaultPlaylist)
	}
	if len(m.Playlists) != 1 || len(m.Playlists[0].Items) != 2 {
		t.Fatalf("the playlists are %+v", m.Playlists)
	}
	if m.Playlists[0].Items[0].SHA256 != sha || m.Playlists[0].Items[0].Duration != 15 {
		t.Fatalf("the first item is %+v", m.Playlists[0].Items[0])
	}
	if m.Playlists[0].Items[1].URL == "" || m.Playlists[0].Items[1].RefreshSeconds != 300 {
		t.Fatalf("the second item is %+v", m.Playlists[0].Items[1])
	}
	if len(m.Schedule) != 1 || m.Schedule[0].Start != "08:00" || len(m.Schedule[0].Days) != 5 {
		t.Fatalf("the schedule is %+v", m.Schedule)
	}
	if m.Screen == nil || m.Screen.OffTime != "22:00" {
		t.Fatalf("the screen rule is %+v", m.Screen)
	}
	if len(m.Media) != 1 || m.Media[0].SHA256 != sha ||
		m.Media[0].Size != int64(len(picture)) || m.Media[0].Name != "welcome-autumn.png" {
		t.Fatalf("the media list is %+v", m.Media)
	}

	// 7. The screen downloads the picture the way the device does it, into the
	// object name that the device uses under _fleet/media/.
	objectName, err := store.ObjectName(m.Media[0].SHA256, m.Media[0].Name)
	if err != nil {
		t.Fatalf("the object name of the manifest entry is not usable: %v", err)
	}
	dest := filepath.Join(t.TempDir(), objectName)
	if err := store.Download(context.Background(), http.DefaultClient,
		f.srv.URL+m.Media[0].URL, deviceToken, dest, m.Media[0].SHA256, m.Media[0].Size); err != nil {
		t.Fatalf("the download failed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, picture) {
		t.Fatal("the picture that arrived is not the picture that went up")
	}

	// 8. The screen reports in.
	hb := manifest.Heartbeat{
		DeviceID: "px-e2e00001", HardwareID: "hw-px-e2e00001", Name: "Lobby North", Version: "1.4.2",
		Status: manifest.Status{
			DeviceID: "px-e2e00001", Name: "Lobby North", Version: "1.4.2",
			TempC: 47.5, MediaFreeBytes: 3 << 30, MediaTotalBytes: 8 << 30,
			BrowserState: "running", ScreenOn: true,
			NowPlaying: &manifest.NowPlaying{
				Playlist: "lobby-loop", Index: 0, Item: "welcome-autumn.png", Kind: "image",
			},
		},
	}
	f.mustOK(f.device(http.MethodPost, "/api/v1/heartbeat", deviceToken, hb), "the heartbeat")

	// The fleet list shows the screen as online, in its group, with its name.
	list := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/devices", nil), "the fleet list")
	var view struct {
		Devices []deviceRow `json:"devices"`
		Totals  struct {
			Screens    int `json:"screens"`
			CheckedIn  int `json:"checked_in"`
			NeedsALook int `json:"needs_a_look"`
		} `json:"totals"`
	}
	list.json(t, &view)
	if len(view.Devices) != 1 {
		t.Fatalf("the fleet holds %d screens", len(view.Devices))
	}
	row := view.Devices[0]
	if row.Name != "Lobby North" || row.GroupName != "Lobby" || row.State != "online" {
		t.Fatalf("the row is %+v", row)
	}
	if view.Totals.Screens != 1 || view.Totals.CheckedIn != 1 || view.Totals.NeedsALook != 0 {
		t.Fatalf("the totals are %+v", view.Totals)
	}

	// 9. A command reaches the screen on its next poll, and the heartbeat after it
	// says that the screen ran it.
	queued := f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/px-e2e00001/commands",
		map[string]any{"type": "rescan"}), "queue a command")
	var cmd struct {
		ID int64 `json:"id"`
	}
	queued.json(t, &cmd)

	m = f.getManifest(deviceToken)
	if len(m.Commands) != 1 || m.Commands[0].Type != "rescan" || m.Commands[0].ID != cmd.ID {
		t.Fatalf("the commands of the manifest are %+v", m.Commands)
	}
	if state := f.commandState(t, "px-e2e00001", cmd.ID); state != "delivered" {
		t.Fatalf("the command is %q, want delivered", state)
	}

	hb.Acks = []int64{cmd.ID}
	f.mustOK(f.device(http.MethodPost, "/api/v1/heartbeat", deviceToken, hb), "the heartbeat with the ack")
	if state := f.commandState(t, "px-e2e00001", cmd.ID); state != "acked" {
		t.Fatalf("the command is %q, want acked", state)
	}

	// 10. The playlist says how many screens it reaches, which is what the editor
	// shows before a save.
	count := f.mustOK(f.adminCall(http.MethodGet,
		"/api/admin/playlists/"+itoa(playlist)+"/devices", nil), "the device count")
	var counted struct {
		Devices int `json:"devices"`
	}
	count.json(t, &counted)
	if counted.Devices != 1 {
		t.Fatalf("the playlist reaches %d screens, want 1", counted.Devices)
	}
}
