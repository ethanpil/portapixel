package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
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
//
// A request that waits for approval has no devices row: it lives in its own table,
// so that an enroll request can never change a screen that works. Such a request
// goes into the same list with Pending set, and the pending rows come first. The UI
// keeps one shape for "every screen that I must look at".
func (d Deps) getDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := d.DB.Devices()
	if err != nil {
		fail(w, err)
		return
	}
	pending, err := d.DB.PendingEnrollments()
	if err != nil {
		fail(w, err)
		return
	}
	poll, now := d.defaultPoll(), time.Now()
	view := DeviceListView{Devices: []db.Device{}}
	for _, p := range pending {
		view.Devices = append(view.Devices, pendingAsDevice(p))
	}
	for _, dev := range devices {
		dev.State = db.State(dev, poll, now)
		view.Devices = append(view.Devices, dev)
	}
	for _, dev := range view.Devices {
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
	httpjson.Write(w, http.StatusOK, view)
}

// pendingAsDevice shows one waiting request in the shape of a device row.
func pendingAsDevice(p db.PendingEnrollment) db.Device {
	return db.Device{
		ID:           p.DeviceID,
		Name:         p.Name,
		HardwareID:   p.HardwareID,
		Version:      p.Version,
		LastIP:       p.IP,
		CreatedAt:    p.CreatedAt,
		Pending:      true,
		PendingCode:  p.PairingCode,
		PendingID:    p.ID,
		CollidesWith: p.CollidesWith,
		State:        db.StatePending,
	}
}

// getDevice gives one device with its rules and its recent commands.
func (d Deps) getDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dev, err := d.DB.Device(id)
	if err != nil {
		// A request that waits has no devices row yet. The detail page of the
		// pending card asks for it by the same path, so it answers here.
		if p, pendErr := d.DB.PendingByDevice(id); pendErr == nil {
			httpjson.Write(w, http.StatusOK, DeviceView{
				Device:           pendingAsDevice(p),
				Assignments:      []db.Assignment{},
				GroupAssignments: []db.Assignment{},
				Commands:         []db.Command{},
			})
			return
		}
		fail(w, err)
		return
	}
	dev.State = db.State(dev, d.defaultPoll(), time.Now())

	view := DeviceView{Device: dev}
	if view.Assignments, err = d.DB.Assignments(0, dev.ID); err != nil {
		fail(w, err)
		return
	}
	view.GroupAssignments = []db.Assignment{}
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
	httpjson.Write(w, http.StatusOK, view)
}

// renameDevice sets the display name.
func (d Deps) renameDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !httpjson.Read(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		httpjson.Fields(w, "the request has a field that this server cannot use",
			db.Errors{{Field: "name", Message: "a screen needs a name"}})
		return
	}
	if err := d.DB.RenameDevice(r.PathValue("id"), name); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("device-rename", r.PathValue("id")+" is now "+name)
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// moveDevice puts the device in a group. A group_id of 0 takes it out of every
// group.
func (d Deps) moveDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		GroupID int64 `json:"group_id"`
	}
	if !httpjson.Read(w, r, &body) {
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
	httpjson.Write(w, http.StatusOK, httpjson.OK)
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
	if !httpjson.Read(w, r, &body) {
		return
	}
	days := strings.Join(db.CleanDays(body.ScreenDays), ",")
	if err := db.ValidScreenRule(body.ScreenOn, body.ScreenOff, days); err != nil {
		httpjson.Fields(w, "the request has a field that this server cannot use",
			db.Errors{{Field: "screen_on", Message: err.Error()}})
		return
	}
	if body.DefaultPlaylistID != 0 {
		if _, err := d.DB.PlaylistNoCount(body.DefaultPlaylistID); err != nil {
			fail(w, err)
			return
		}
	}
	if err := d.DB.SetDeviceOverrides(r.PathValue("id"), body.DefaultPlaylistID,
		body.ScreenOn, body.ScreenOff, days); err != nil {
		fail(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// deleteDevice removes the screen. The token goes with the row, so the device
// must enroll again before this server answers it. A request that only waits for
// approval goes away with its row in the pending list.
func (d Deps) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := d.DB.DeleteDevice(id)
	if err != nil {
		if p, pendErr := d.DB.PendingByDevice(id); pendErr == nil {
			if rejectErr := d.DB.RejectPending(p.ID); rejectErr != nil {
				fail(w, rejectErr)
				return
			}
			d.Log.Log("device-delete", id+" waited for approval and is gone")
			httpjson.Write(w, http.StatusOK, httpjson.OK)
			return
		}
		fail(w, err)
		return
	}
	d.Log.Log("device-delete", id+" is gone; its token is revoked")
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// approveDevice lets a waiting request in by the device ID that it asks for.
func (d Deps) approveDevice(w http.ResponseWriter, r *http.Request) {
	p, err := d.DB.PendingByDevice(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	d.approve(w, r, p)
}

// approveByCode lets a waiting request in by the code on its screen. That is what
// a person reads, so the UI approves by code (D25).
func (d Deps) approveByCode(w http.ResponseWriter, r *http.Request) {
	p, err := d.DB.PendingByCode(r.PathValue("code"))
	if err != nil {
		fail(w, err)
		return
	}
	d.approve(w, r, p)
}

func (d Deps) approve(w http.ResponseWriter, r *http.Request, p db.PendingEnrollment) {
	var body struct {
		GroupID int64 `json:"group_id"`
	}
	// Every field is optional, so an empty body is an empty object. The UI sends
	// no body from the "approve" button of the pending card.
	if !httpjson.ReadOptional(w, r, &body) {
		return
	}
	if body.GroupID != 0 {
		if _, err := d.DB.Group(body.GroupID); err != nil {
			fail(w, err)
			return
		}
	}
	if err := d.DB.ApprovePending(p.ID, body.GroupID); err != nil {
		fail(w, err)
		return
	}
	detail := p.DeviceID + " may join; it gets its token on its next call"
	if p.CollidesWith != "" {
		detail = p.DeviceID + " may join; the token of the machine that held this ID is revoked"
	}
	d.Log.Log("device-approve", detail)
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// rejectDevice turns a waiting request away by the device ID that it asks for.
func (d Deps) rejectDevice(w http.ResponseWriter, r *http.Request) {
	p, err := d.DB.PendingByDevice(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	d.reject(w, p)
}

// rejectByCode turns a waiting request away by its code.
func (d Deps) rejectByCode(w http.ResponseWriter, r *http.Request) {
	p, err := d.DB.PendingByCode(r.PathValue("code"))
	if err != nil {
		fail(w, err)
		return
	}
	d.reject(w, p)
}

func (d Deps) reject(w http.ResponseWriter, p db.PendingEnrollment) {
	if err := d.DB.RejectPending(p.ID); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("device-reject", p.DeviceID+" was turned away")
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// confirmHardware is the one click of D21: the admin agrees that the card moved
// into another box. The server stores the new hardware ID now and not before.
func (d Deps) confirmHardware(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := d.DB.ConfirmHardware(id); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("hardware-confirm", id+": the new hardware is confirmed")
	httpjson.Write(w, http.StatusOK, httpjson.OK)
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
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// queueCommand puts one command in the queue of one device. The device runs it
// at its next poll (D24).
func (d Deps) queueCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type string            `json:"type"`
		Args map[string]string `json:"args"`
	}
	if !httpjson.Read(w, r, &body) {
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
	httpjson.Write(w, http.StatusOK, map[string]any{"id": cmdID})
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
	if !httpjson.Read(w, r, &body) {
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
	httpjson.Write(w, http.StatusOK, map[string]any{"queued": n})
}

// getCommands gives the recent commands of one device with their state.
func (d Deps) getCommands(w http.ResponseWriter, r *http.Request) {
	commands, err := d.DB.Commands(r.PathValue("id"), 50)
	if err != nil {
		fail(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"commands": commands})
}
