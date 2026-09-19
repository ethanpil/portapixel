package server_test

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"testing"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// pairDevice pairs one device through the real enroll route and gives its token.
func (f *fleet) pairDevice(id string) string {
	f.t.Helper()
	token := f.makeToken("auto", 0, "")
	out, res := f.enroll(id, token)
	f.mustOK(res, "enroll "+id)
	if out.DeviceToken == "" {
		f.t.Fatalf("%s got no token: %s", id, res.body)
	}
	return out.DeviceToken
}

// imageBytes makes a PNG of the given size.
func imageBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// uploadMedia uploads one file and gives its hash.
func (f *fleet) uploadMedia(name string, body []byte) string {
	f.t.Helper()
	res := f.mustOK(f.upload("/api/admin/media", name, body), "upload "+name)
	var out struct {
		Media struct {
			SHA256   string `json:"sha256"`
			OrigName string `json:"orig_name"`
			Size     int64  `json:"size"`
			HasThumb bool   `json:"has_thumb"`
		} `json:"media"`
		Duplicate bool `json:"duplicate"`
	}
	res.json(f.t, &out)
	if out.Media.SHA256 == "" {
		f.t.Fatalf("the upload answer is %s", res.body)
	}
	return out.Media.SHA256
}

// makePlaylist makes a playlist of media items and gives its row number.
func (f *fleet) makePlaylist(title string, items ...map[string]any) int64 {
	f.t.Helper()
	res := f.mustOK(f.adminCall(http.MethodPost, "/api/admin/playlists", map[string]any{
		"title": title, "transition": "crossfade", "items": items,
	}), "make the playlist "+title)
	var out struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	res.json(f.t, &out)
	return out.ID
}

// makeGroup makes a group and gives its row number.
func (f *fleet) makeGroup(name string) int64 {
	f.t.Helper()
	res := f.mustOK(f.adminCall(http.MethodPost, "/api/admin/groups",
		map[string]string{"name": name}), "make the group "+name)
	var out struct {
		ID int64 `json:"id"`
	}
	res.json(f.t, &out)
	return out.ID
}

// getManifest asks for the manifest of one device.
func (f *fleet) getManifest(token string) manifest.Manifest {
	f.t.Helper()
	res := f.mustOK(f.device(http.MethodGet, "/api/v1/manifest", token, nil), "manifest")
	var m manifest.Manifest
	res.json(f.t, &m)
	return m
}

func TestManifestResolvesDeviceOverGroup(t *testing.T) {
	f := newFleet(t)
	f.login()

	sha := f.uploadMedia("welcome.png", imageBytes(t, 40, 30))
	item := map[string]any{"sha256": sha, "name": "welcome.png", "duration": 15}
	groupList := f.makePlaylist("Group loop", item)
	deviceList := f.makePlaylist("Device loop", item)

	group := f.makeGroup("Lobby")
	token := f.pairDevice("px-res00001")
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/px-res00001/group",
		map[string]int64{"group_id": group}), "move the device")

	// A group rule and a group default.
	f.mustOK(f.adminCall(http.MethodPut, fmt.Sprintf("/api/admin/groups/%d", group), map[string]any{
		"name": "Lobby", "default_playlist_id": groupList,
	}), "set the group default")
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/assignments", map[string]any{
		"group_id": group, "playlist_id": groupList,
		"days": []string{"mon", "tue"}, "start": "08:00", "end": "18:00", "priority": 10,
	}), "add a group rule")

	m := f.getManifest(token)
	if m.DefaultPlaylist != "group-loop" {
		t.Fatalf("the default playlist is %q", m.DefaultPlaylist)
	}
	if len(m.Schedule) != 1 || m.Schedule[0].Playlist != "group-loop" {
		t.Fatalf("the schedule is %+v", m.Schedule)
	}
	if len(m.Media) != 1 || m.Media[0].URL != "/api/v1/media/"+sha {
		t.Fatalf("the media list is %+v", m.Media)
	}
	if m.ServerName != "Test fleet" || m.PollSeconds != 60 {
		t.Fatalf("the header is %+v", m)
	}

	// One device rule replaces every group rule.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/assignments", map[string]any{
		"device_id": "px-res00001", "playlist_id": deviceList,
		"start": "00:00", "end": "23:59", "priority": 5,
	}), "add a device rule")

	m = f.getManifest(token)
	if len(m.Schedule) != 1 || m.Schedule[0].Playlist != "device-loop" {
		t.Fatalf("the device rule did not replace the group rules: %+v", m.Schedule)
	}
}

func TestManifestSchedulePriorityOrder(t *testing.T) {
	f := newFleet(t)
	f.login()

	sha := f.uploadMedia("a.png", imageBytes(t, 10, 10))
	item := map[string]any{"sha256": sha, "name": "a.png", "duration": 5}
	first := f.makePlaylist("Morning", item)
	second := f.makePlaylist("Afternoon", item)
	third := f.makePlaylist("Evening", item)

	group := f.makeGroup("Retail")
	token := f.pairDevice("px-prio0002")
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/px-prio0002/group",
		map[string]int64{"group_id": group}), "move the device")

	// Add them in the wrong order. The priority decides the order that the device
	// sees, and the device takes the first rule that matches.
	for _, c := range []struct {
		playlist int64
		priority int
	}{{third, 30}, {first, 10}, {second, 20}} {
		f.mustOK(f.adminCall(http.MethodPost, "/api/admin/assignments", map[string]any{
			"group_id": group, "playlist_id": c.playlist,
			"start": "08:00", "end": "18:00", "priority": c.priority,
		}), "add a rule")
	}

	m := f.getManifest(token)
	want := []string{"morning", "afternoon", "evening"}
	if len(m.Schedule) != 3 {
		t.Fatalf("the schedule holds %d rules: %+v", len(m.Schedule), m.Schedule)
	}
	for i, name := range want {
		if m.Schedule[i].Playlist != name {
			t.Fatalf("rule %d is %q, want %q: %+v", i, m.Schedule[i].Playlist, name, m.Schedule)
		}
	}
}

func TestHeartbeatAndCommandAck(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.pairDevice("px-hb000001")

	// Queue a command through the admin API.
	res := f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/px-hb000001/commands",
		map[string]any{"type": "restart-browser"}), "queue a command")
	var queued struct {
		ID int64 `json:"id"`
	}
	res.json(t, &queued)

	// It is queued and not delivered.
	if state := f.commandState(t, "px-hb000001", queued.ID); state != "queued" {
		t.Fatalf("the command is %q, want queued", state)
	}

	// The manifest delivers it.
	m := f.getManifest(token)
	if len(m.Commands) != 1 || m.Commands[0].ID != queued.ID || m.Commands[0].Type != "restart-browser" {
		t.Fatalf("the manifest commands are %+v", m.Commands)
	}
	if state := f.commandState(t, "px-hb000001", queued.ID); state != "delivered" {
		t.Fatalf("the command is %q, want delivered", state)
	}
	// A second poll does not deliver it again.
	if m = f.getManifest(token); len(m.Commands) != 0 {
		t.Fatalf("the command went out twice: %+v", m.Commands)
	}

	// The heartbeat acknowledges it.
	hb := manifest.Heartbeat{
		DeviceID: "px-hb000001", HardwareID: hardwareOf("px-hb000001"), Version: "1.4.2",
		Acks: []int64{queued.ID},
		Status: manifest.Status{
			DeviceID: "px-hb000001", Version: "1.4.2", TempC: 42.5,
			MediaFreeBytes: 1 << 30, BrowserState: "running",
		},
	}
	f.mustOK(f.device(http.MethodPost, "/api/v1/heartbeat", token, hb), "heartbeat")
	if state := f.commandState(t, "px-hb000001", queued.ID); state != "acked" {
		t.Fatalf("the command is %q, want acked", state)
	}

	// The device detail shows the state that the heartbeat reported.
	detail := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/devices/px-hb000001", nil), "the detail")
	var view struct {
		Device struct {
			State   string `json:"state"`
			Version string `json:"version"`
			Status  string `json:"status"`
		} `json:"device"`
	}
	detail.json(t, &view)
	if view.Device.State != "online" {
		t.Fatalf("the state is %q, want online", view.Device.State)
	}
	if !bytes.Contains([]byte(view.Device.Status), []byte("42.5")) {
		t.Fatalf("the status does not hold the temperature: %s", view.Device.Status)
	}
}

// commandState gives the state of one command of one device.
func (f *fleet) commandState(t *testing.T, deviceID string, id int64) string {
	t.Helper()
	res := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/devices/"+deviceID+"/commands", nil),
		"the command list")
	var view struct {
		Commands []struct {
			ID    int64  `json:"id"`
			State string `json:"state"`
		} `json:"commands"`
	}
	res.json(t, &view)
	for _, c := range view.Commands {
		if c.ID == id {
			return c.State
		}
	}
	t.Fatalf("the command %d is not in the list: %s", id, res.body)
	return ""
}

func TestBulkCommandForAGroup(t *testing.T) {
	f := newFleet(t)
	f.login()
	group := f.makeGroup("Cafeteria")

	var tokens []string
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("px-bulk000%d", i)
		tokens = append(tokens, f.pairDevice(id))
		f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/"+id+"/group",
			map[string]int64{"group_id": group}), "move the device")
	}

	res := f.mustOK(f.adminCall(http.MethodPost, fmt.Sprintf("/api/admin/groups/%d/commands", group),
		map[string]any{"type": "screen-off"}), "the bulk command")
	var out struct {
		Queued int `json:"queued"`
	}
	res.json(t, &out)
	if out.Queued != 3 {
		t.Fatalf("the bulk command reached %d devices, want 3", out.Queued)
	}
	for i, token := range tokens {
		m := f.getManifest(token)
		if len(m.Commands) != 1 || m.Commands[0].Type != "screen-off" {
			t.Fatalf("device %d got %+v", i, m.Commands)
		}
	}
}

// TestHeartbeatHardwareChangeAndConflict covers the clone of D21. Two machines that
// answer in turn on one token is the case, and nothing else is.
func TestHeartbeatHardwareChangeAndConflict(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.pairDevice("px-hw000001")

	// Box B answers first, which makes the row ask for a confirmation.
	boxB := manifest.Heartbeat{DeviceID: "px-hw000001",
		HardwareID: hardwareOf("another-box"), Version: "1.4.2"}
	f.mustOK(f.device(http.MethodPost, "/api/v1/heartbeat", token, boxB), "the heartbeat of box B")
	if view := f.deviceView(t, "px-hw000001"); view.Conflict {
		t.Fatalf("one machine that changed was read as a clone: %+v", view)
	}

	// Box A answers next. Now the two take turns, so one card runs in two boxes.
	boxA := manifest.Heartbeat{DeviceID: "px-hw000001",
		HardwareID: hardwareOf("px-hw000001"), Version: "1.4.2"}
	f.mustOK(f.device(http.MethodPost, "/api/v1/heartbeat", token, boxA), "the heartbeat of box A")

	view := f.deviceView(t, "px-hw000001")
	if !view.Conflict || view.State != "conflict" {
		t.Fatalf("the device is %+v, want a conflict", view)
	}
	if view.ConflictHardwareID != hardwareOf("another-box") {
		t.Fatalf("the conflict names %q", view.ConflictHardwareID)
	}

	// The admin resolves it. That revokes the token, so both boxes must pair again.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/px-hw000001/resolve-conflict", nil),
		"resolve the conflict")
	res := f.device(http.MethodGet, "/api/v1/manifest", token, nil)
	if res.status != http.StatusUnauthorized {
		t.Fatalf("the revoked token answered %d: %s", res.status, res.body)
	}
}

// TestHeartbeatHardwareRepair covers the repair of D21 with the call order of a real
// device: the manifest poll first, the heartbeat after it.
//
// The old rule compared the hardware ID against last_seen, which the poll had just
// moved, so every repair read as a clone and this branch was unreachable. The test
// needs no jump of the clock either.
func TestHeartbeatHardwareRepair(t *testing.T) {
	f := newFleet(t)
	f.login()
	token := f.pairDevice("px-hw000002")

	// The card is in another box now. The device polls, then it reports.
	f.mustOK(f.device(http.MethodGet, "/api/v1/manifest", token, nil), "the poll")
	hb := manifest.Heartbeat{DeviceID: "px-hw000002",
		HardwareID: hardwareOf("repaired"), Version: "1.4.2"}
	f.mustOK(f.device(http.MethodPost, "/api/v1/heartbeat", token, hb), "heartbeat")

	view := f.deviceView(t, "px-hw000002")
	if !view.NeedsConfirm || view.Conflict {
		t.Fatalf("the device is %+v, want a hardware change and no conflict", view)
	}
	if view.HardwareID != hardwareOf("repaired") || view.PrevHardwareID != hardwareOf("px-hw000002") {
		t.Fatalf("the hardware IDs are %q and %q", view.HardwareID, view.PrevHardwareID)
	}

	// One click confirms it, and the token keeps working.
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/px-hw000002/confirm-hardware", nil),
		"confirm the hardware")
	if view = f.deviceView(t, "px-hw000002"); view.NeedsConfirm {
		t.Fatal("the confirmation did not clear the flag")
	}
	f.mustOK(f.device(http.MethodGet, "/api/v1/manifest", token, nil), "the manifest after the repair")
}

// deviceRow is the part of a device that the tests read.
type deviceRow struct {
	ID                 string `json:"id"`
	State              string `json:"state"`
	HardwareID         string `json:"hardware_id"`
	PrevHardwareID     string `json:"prev_hardware_id"`
	Conflict           bool   `json:"conflict"`
	ConflictHardwareID string `json:"conflict_hardware_id"`
	NeedsConfirm       bool   `json:"needs_confirm"`
	GroupName          string `json:"group_name"`
	Name               string `json:"name"`
	Pending            bool   `json:"pending"`
	PendingCode        string `json:"pending_code"`
	CollidesWith       string `json:"collides_with"`
}

func (f *fleet) deviceView(t *testing.T, id string) deviceRow {
	t.Helper()
	res := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/devices/"+id, nil), "the device detail")
	var view struct {
		Device deviceRow `json:"device"`
	}
	res.json(t, &view)
	return view.Device
}

func TestDeviceTokenIsScopedToItsOwnRow(t *testing.T) {
	f := newFleet(t)
	f.login()
	first := f.pairDevice("px-scope001")
	second := f.pairDevice("px-scope002")

	if first == second {
		t.Fatal("two devices got one token")
	}

	// Queue a command for the first device only.
	res := f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/px-scope001/commands",
		map[string]any{"type": "reboot"}), "queue a command")
	var queued struct {
		ID int64 `json:"id"`
	}
	res.json(t, &queued)

	// The second device must not see it.
	if m := f.getManifest(second); len(m.Commands) != 0 {
		t.Fatalf("the second device saw the command of the first: %+v", m.Commands)
	}
	// It must not be able to acknowledge it either.
	hb := manifest.Heartbeat{DeviceID: "px-scope002", HardwareID: hardwareOf("px-scope002"), Acks: []int64{queued.ID}}
	f.mustOK(f.device(http.MethodPost, "/api/v1/heartbeat", second, hb), "heartbeat")
	if state := f.commandState(t, "px-scope001", queued.ID); state == "acked" {
		t.Fatal("one device acknowledged the command of another device")
	}

	// A heartbeat with a list of acknowledgements that has no end is refused.
	acks := make([]int64, 200)
	for i := range acks {
		acks[i] = int64(i + 1)
	}
	flood := manifest.Heartbeat{DeviceID: "px-scope002",
		HardwareID: hardwareOf("px-scope002"), Acks: acks}
	if res := f.device(http.MethodPost, "/api/v1/heartbeat", second, flood); res.status != http.StatusUnprocessableEntity {
		t.Fatalf("a heartbeat with 200 acknowledgements answered %d: %s", res.status, res.body)
	}

	// A device that was deleted has no token any more.
	f.mustOK(f.adminCall(http.MethodDelete, "/api/admin/devices/px-scope001", nil), "delete the device")
	if res := f.device(http.MethodGet, "/api/v1/manifest", first, nil); res.status != http.StatusUnauthorized {
		t.Fatalf("the token of a deleted device answered %d", res.status)
	}
	// The other device still works.
	f.mustOK(f.device(http.MethodGet, "/api/v1/manifest", second, nil), "the manifest of the second device")
}

func TestDeviceRoutesNeedAToken(t *testing.T) {
	f := newFleet(t)
	for _, path := range []string{
		"/api/v1/manifest",
		"/api/v1/media/" + fmt.Sprintf("%064d", 0),
		"/api/v1/releases/1.5.0/portapixeld-arm64",
	} {
		res := f.device(http.MethodGet, path, "", nil)
		if res.status != http.StatusUnauthorized {
			t.Errorf("%s with no token answered %d", path, res.status)
		}
		res = f.device(http.MethodGet, path, "a token that nobody gave out", nil)
		if res.status != http.StatusUnauthorized {
			t.Errorf("%s with a wrong token answered %d", path, res.status)
		}
	}
}
