package server_test

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/server/db"
)

// The device owns its name: mDNS, the host name and the fallback screen use it,
// and a person can change it on the device. The server row takes the name that the
// heartbeat reports. Before this, the server kept the name of the enrollment for
// ever.
func TestTheRowTakesTheNameThatTheHeartbeatReports(t *testing.T) {
	f := newFleet(t)
	f.login()
	id := "px-name0001"
	token := f.pairDevice(id)
	f.getManifest(token)

	beat := func(name string) {
		t.Helper()
		f.mustOK(f.device(http.MethodPost, "/api/v1/heartbeat", token, manifest.Heartbeat{
			DeviceID: id, HardwareID: hardwareOf(id), Name: name, Version: "1.5.0",
			Status: manifest.Status{DeviceID: id, Name: name},
		}), "heartbeat")
	}

	beat("  Front desk ")
	if got := f.deviceView(t, id).Name; got != "Front desk" {
		t.Fatalf("the row holds the name %q, want %q", got, "Front desk")
	}

	// A name that breaks the rule keeps the name of the row, and the ops log says
	// so one time, not at every poll.
	for _, bad := range []string{"two\nlines", strings.Repeat("x", manifest.MaxNameLength+1), ""} {
		beat(bad)
		beat(bad)
		if got := f.deviceView(t, id).Name; got != "Front desk" {
			t.Fatalf("the reported name %q changed the row to %q", bad, got)
		}
	}
	log := opsText(t, f)
	if n := strings.Count(log, "device-name-refused"); n != 3 {
		t.Errorf("the ops log has %d lines for three bad names, want 3:\n%s", n, log)
	}
	if strings.Contains(log, "two\nlines") {
		t.Errorf("the ops log holds the control character of the name:\n%s", log)
	}
	if !strings.Contains(log, `device-rename `+id+` reports the name "Front desk"`) {
		t.Errorf("the ops log does not record the new name:\n%s", log)
	}
}

// A rename from the server is a command. The route queues it and does not write the
// row, because the next heartbeat would write the old name back. One mechanism: the
// route and the command dialog send the same command.
func TestTheRenameRouteQueuesACommand(t *testing.T) {
	f := newFleet(t)
	f.login()
	id := "px-name0002"
	token := f.pairDevice(id)
	before := f.deviceView(t, id).Name

	res := f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/"+id+"/rename",
		map[string]string{"name": "  Lobby north  "}), "the rename")
	var queued struct {
		ID int64 `json:"id"`
	}
	res.json(t, &queued)
	if queued.ID == 0 {
		t.Fatalf("the rename answered no command: %s", res.body)
	}
	if got := f.deviceView(t, id).Name; got != before {
		t.Errorf("the route wrote the row: %q, want %q until the screen reports", got, before)
	}

	// The command goes out with the clean name.
	m := f.getManifest(token)
	if len(m.Commands) != 1 || m.Commands[0].Type != db.CommandRename ||
		m.Commands[0].Args["name"] != "Lobby north" {
		t.Fatalf("the manifest commands are %+v", m.Commands)
	}

	// The device reports the new name in the heartbeat that acknowledges it.
	f.mustOK(f.device(http.MethodPost, "/api/v1/heartbeat", token, manifest.Heartbeat{
		DeviceID: id, HardwareID: hardwareOf(id), Name: "Lobby north", Version: "1.5.0",
		Acks: []int64{queued.ID},
	}), "heartbeat")
	if got := f.deviceView(t, id).Name; got != "Lobby north" {
		t.Errorf("the row holds %q after the heartbeat", got)
	}
	if state := f.commandState(t, id, queued.ID); state != db.CommandAcked {
		t.Errorf("the command is %q", state)
	}

	// The command dialog sends the same command through the command route.
	res = f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/"+id+"/commands",
		map[string]any{"type": "rename", "args": map[string]string{"name": "Front desk"}}), "the command")
	res.json(t, &queued)
	if m := f.getManifest(token); len(m.Commands) != 1 || m.Commands[0].Args["name"] != "Front desk" {
		t.Errorf("the manifest commands are %+v", m.Commands)
	}
}

// A name that breaks the rule is refused at the button, with the field that the
// dialog shows. A group never takes a rename: many screens with one name give mDNS
// two answers for one name.
func TestABadRenameIsRefused(t *testing.T) {
	f := newFleet(t)
	f.login()
	id := "px-name0003"
	f.pairDevice(id)
	group := f.makeGroup("Lobby")
	f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/"+id+"/group",
		map[string]any{"group_id": group}), "the move")

	for _, bad := range []map[string]any{
		{"name": ""},
		{"name": "   "},
		{"name": "two\nlines"},
		{"name": strings.Repeat("x", manifest.MaxNameLength+1)},
	} {
		res := f.adminCall(http.MethodPost, "/api/admin/devices/"+id+"/rename", bad)
		if res.status != http.StatusUnprocessableEntity {
			t.Errorf("the rename to %q answered %d: %s", bad["name"], res.status, res.body)
			continue
		}
		if fields := res.fields(t); len(fields) != 1 || fields[0].Field != "name" {
			t.Errorf("the rename to %q named the fields %+v", bad["name"], fields)
		}
		res = f.adminCall(http.MethodPost, "/api/admin/devices/"+id+"/commands",
			map[string]any{"type": "rename", "args": bad})
		if res.status != http.StatusUnprocessableEntity {
			t.Errorf("the rename command to %q answered %d", bad["name"], res.status)
		}
	}
	res := f.adminCall(http.MethodPost, "/api/admin/devices/"+id+"/commands",
		map[string]any{"type": "rename"})
	if res.status != http.StatusUnprocessableEntity {
		t.Errorf("a rename with no argument answered %d", res.status)
	}

	res = f.adminCall(http.MethodPost, "/api/admin/groups/"+itoa(group)+"/commands",
		map[string]any{"type": "rename", "args": map[string]string{"name": "Everybody"}})
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("a rename for a group answered %d: %s", res.status, res.body)
	}
	if fields := res.fields(t); len(fields) != 1 || fields[0].Field != "type" {
		t.Errorf("the refusal named the fields %+v", fields)
	}
	if res := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/devices/"+id+"/commands", nil), "the list"); strings.Contains(string(res.body), "rename") {
		t.Errorf("a refused rename is in the queue: %s", res.body)
	}

	// A screen that is not there.
	if res := f.adminCall(http.MethodPost, "/api/admin/devices/px-nothere1/rename",
		map[string]string{"name": "Lobby"}); res.status != http.StatusNotFound {
		t.Errorf("a rename of no screen answered %d", res.status)
	}
}

// TestARenameFromTheServerReachesTheDeviceInOnePoll drives the real fleet client:
// the admin renames, the device saves the name on its next poll, and the heartbeat
// of that same poll changes the row.
func TestARenameFromTheServerReachesTheDeviceInOnePoll(t *testing.T) {
	server := newFleet(t)
	server.login()
	device := newSyncDevice(t, server)

	_, token, err := server.db.CreateEnrollToken("every card", "auto", 0, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := device.sync.Pair(context.Background(), server.srv.URL, token, false); err != nil {
		t.Fatalf("pair: %v", err)
	}
	id := device.id.DeviceID
	if got := server.deviceView(t, id).Name; got != "Lobby screen" {
		t.Fatalf("the row holds the name %q after the pairing", got)
	}

	server.mustOK(server.adminCall(http.MethodPost, "/api/admin/devices/"+id+"/rename",
		map[string]string{"name": "Front desk"}), "the rename")
	device.sync.Once(context.Background())

	if device.cfg.Device.Name != "Front desk" {
		t.Errorf("the device holds the name %q", device.cfg.Device.Name)
	}
	if got := server.deviceView(t, id).Name; got != "Front desk" {
		t.Errorf("the row holds the name %q after one poll", got)
	}

	// A rename on the device reaches the row at the next poll.
	device.cfg.Device.Name = "Back office"
	device.sync.Once(context.Background())
	if got := server.deviceView(t, id).Name; got != "Back office" {
		t.Errorf("the row holds the name %q after a rename on the device", got)
	}
}

// opsText gives the ops log of the server under test.
func opsText(t *testing.T, f *fleet) string {
	t.Helper()
	var b strings.Builder
	for _, e := range opslog.New(filepath.Join(f.dir, "ops.log")).Tail(200) {
		b.WriteString(e.Event + " " + e.Details + "\n")
	}
	return b.String()
}
