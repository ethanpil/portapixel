package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/ethanpil/portapixel/internal/server/db"
)

// DeviceListView is the answer of GET /api/admin/devices. It carries the totals
// of the sidebar, so the fleet list needs one request and not two.
type DeviceListView struct {
	Devices []db.Device `json:"devices"`
	Totals  db.Totals   `json:"totals"`
}

// DeviceView is the answer of GET /api/admin/devices/{id}. It holds everything
// that the screen detail page shows.
type DeviceView struct {
	Device db.Device `json:"device"`
	// Group is the group of the device, or nil when it has none.
	Group *db.Group `json:"group,omitempty"`
	// Assignments are the rules of this device. GroupAssignments are the rules of
	// its group, which apply when the device has none of its own.
	Assignments      []db.Assignment `json:"assignments"`
	GroupAssignments []db.Assignment `json:"group_assignments"`
	Commands         []db.Command    `json:"commands"`
}

// getDevices lists the fleet.
func (d Deps) getDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := d.DB.Devices()
	if err != nil {
		fail(w, err)
		return
	}
	poll, now := d.defaultPoll(), time.Now()
	view := DeviceListView{Devices: []db.Device{}}
	for _, dev := range devices {
		dev.State = db.State(dev, poll, now)
		view.Devices = append(view.Devices, dev)
		view.Totals.Screens++
		switch dev.State {
		case db.StateOnline:
			view.Totals.CheckedIn++
		case db.StateQuiet:
			view.Totals.Quiet++
		default:
			view.Totals.NeedsALook++
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// getDevice gives one device with its rules and its recent commands.
func (d Deps) getDevice(w http.ResponseWriter, r *http.Request) {
	dev, err := d.DB.Device(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	dev.State = db.State(dev, d.defaultPoll(), time.Now())

	view := DeviceView{Device: dev}
	if view.Assignments, err = d.DB.Assignments(0, dev.ID); err != nil {
		fail(w, err)
		return
	}
	if dev.GroupID != 0 {
		if g, err := d.DB.Group(dev.GroupID); err == nil {
			view.Group = &g
			if view.GroupAssignments, err = d.DB.Assignments(g.ID, ""); err != nil {
				fail(w, err)
				return
			}
		}
	}
	if view.Commands, err = d.DB.Commands(dev.ID, 20); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// renameDevice sets the display name.
func (d Deps) renameDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeFields(w, "the request has a field that this server cannot use",
			db.Errors{{Field: "name", Message: "a screen needs a name"}})
		return
	}
	if err := d.DB.RenameDevice(r.PathValue("id"), name); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("device-rename", r.PathValue("id")+" is now "+name)
	writeJSON(w, http.StatusOK, ok)
}

// moveDevice puts the device in a group. A group_id of 0 takes it out of every
// group.
func (d Deps) moveDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		GroupID int64 `json:"group_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.GroupID != 0 {
		if _, err := d.DB.Group(body.GroupID); err != nil {
			fail(w, err)
			return
		}
	}
	if err := d.DB.MoveDevice(r.PathValue("id"), body.GroupID); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ok)
}

// setOverrides sets the per-device default playlist and screen rule. They win
// over the values of the group (D48 gives both to the server).
func (d Deps) setOverrides(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DefaultPlaylistID int64    `json:"default_playlist_id"`
		ScreenOn          string   `json:"screen_on"`
		ScreenOff         string   `json:"screen_off"`
		ScreenDays        []string `json:"screen_days"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	days := strings.Join(db.CleanDays(body.ScreenDays), ",")
	if err := db.ValidScreenRule(body.ScreenOn, body.ScreenOff, days); err != nil {
		writeFields(w, "the request has a field that this server cannot use",
			db.Errors{{Field: "screen_on", Message: err.Error()}})
		return
	}
	if body.DefaultPlaylistID != 0 {
		if _, err := d.DB.Playlist(body.DefaultPlaylistID); err != nil {
			fail(w, err)
			return
		}
	}
	if err := d.DB.SetDeviceOverrides(r.PathValue("id"), body.DefaultPlaylistID,
		body.ScreenOn, body.ScreenOff, days); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ok)
}

// deleteDevice removes the screen. The token goes with the row, so the device
// must enroll again before this server answers it.
func (d Deps) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := d.DB.DeleteDevice(id); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("device-delete", id+" is gone; its token is revoked")
	writeJSON(w, http.StatusOK, ok)
}

// approveDevice lets a pending device in by its ID.
func (d Deps) approveDevice(w http.ResponseWriter, r *http.Request) {
	d.approve(w, r, r.PathValue("id"))
}

// approveByCode lets a pending device in by the code on its screen. That is what
// a person reads, so the UI approves by code (D25).
func (d Deps) approveByCode(w http.ResponseWriter, r *http.Request) {
	dev, err := d.DB.DeviceByCode(r.PathValue("code"))
	if err != nil {
		fail(w, err)
		return
	}
	d.approve(w, r, dev.ID)
}

func (d Deps) approve(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		GroupID int64 `json:"group_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.GroupID != 0 {
		if _, err := d.DB.Group(body.GroupID); err != nil {
			fail(w, err)
			return
		}
	}
	if err := d.DB.Approve(id, body.GroupID); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("device-approve", id+" may join; it gets its token on its next call")
	writeJSON(w, http.StatusOK, ok)
}

// rejectDevice turns a pending device away by its ID.
func (d Deps) rejectDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := d.DB.Reject(id); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("device-reject", id+" was turned away")
	writeJSON(w, http.StatusOK, ok)
}

// rejectByCode turns a pending device away by its code.
func (d Deps) rejectByCode(w http.ResponseWriter, r *http.Request) {
	dev, err := d.DB.DeviceByCode(r.PathValue("code"))
	if err != nil {
		fail(w, err)
		return
	}
	if err := d.DB.Reject(dev.ID); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("device-reject", dev.ID+" was turned away")
	writeJSON(w, http.StatusOK, ok)
}

// confirmHardware is the one click of D21: the admin agrees that the card moved
// into another box.
func (d Deps) confirmHardware(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := d.DB.ConfirmHardware(id); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("hardware-confirm", id+": the new hardware is confirmed")
	writeJSON(w, http.StatusOK, ok)
}

// resolveConflict clears a clone conflict. It revokes the token, so both boxes
// have to enroll again and the admin sees which one comes back (D21).
func (d Deps) resolveConflict(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := d.DB.ResolveConflict(id); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("conflict-resolve", id+": the token is revoked; the device must pair again")
	writeJSON(w, http.StatusOK, ok)
}

// queueCommand puts one command in the queue of one device. The device runs it
// at its next poll (D24).
func (d Deps) queueCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type string            `json:"type"`
		Args map[string]string `json:"args"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	if _, err := d.DB.Device(id); err != nil {
		fail(w, err)
		return
	}
	cmdID, err := d.DB.QueueCommand(id, body.Type, body.Args)
	if err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("command", body.Type+" for "+id)
	writeJSON(w, http.StatusOK, map[string]any{"id": cmdID})
}

// queueGroupCommand puts one command in the queue of every device of a group.
func (d Deps) queueGroupCommand(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r, "id")
	if !okID {
		return
	}
	var body struct {
		Type string            `json:"type"`
		Args map[string]string `json:"args"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	g, err := d.DB.Group(id)
	if err != nil {
		fail(w, err)
		return
	}
	n, err := d.DB.QueueGroupCommand(id, body.Type, body.Args)
	if err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("command", body.Type+" for the group "+g.Name)
	writeJSON(w, http.StatusOK, map[string]any{"queued": n})
}

// getCommands gives the recent commands of one device with their state.
func (d Deps) getCommands(w http.ResponseWriter, r *http.Request) {
	commands, err := d.DB.Commands(r.PathValue("id"), 50)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": commands})
}
