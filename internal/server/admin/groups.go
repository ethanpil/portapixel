package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
)

// getGroups lists the groups with the number of devices in each one.
func (d Deps) getGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := d.DB.Groups()
	if err != nil {
		fail(w, err)
		return
	}
	if groups == nil {
		groups = []db.Group{}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"groups": groups})
}

// createGroup makes a group.
func (d Deps) createGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !httpjson.Read(w, r, &body) {
		return
	}
	id, err := d.DB.CreateGroup(body.Name)
	if err != nil {
		// The name is unique in the schema, and the sentinel comes from the result
		// code of SQLite. A check on the text of the message would turn a busy
		// database into "another group has this name" the day that the wording
		// moves.
		if errors.Is(err, db.ErrDuplicate) {
			httpjson.Fields(w, "the request has a field that this server cannot use",
				db.Errors{{Field: "name", Message: "another group already has this name"}})
			return
		}
		var fieldErrs db.Errors
		if errors.As(err, &fieldErrs) {
			httpjson.Fields(w, "the request has a field that this server cannot use", fieldErrs)
			return
		}
		if err.Error() == "a group needs a name" {
			httpjson.Fields(w, "the request has a field that this server cannot use",
				db.Errors{{Field: "name", Message: err.Error()}})
			return
		}
		fail(w, err)
		return
	}
	d.Log.Log("group-create", body.Name)
	httpjson.Write(w, http.StatusOK, map[string]any{"id": id})
}

// updateGroup sets the name, the default playlist and the screen rule of a group.
func (d Deps) updateGroup(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r, "id")
	if !okID {
		return
	}
	var body struct {
		Name              string   `json:"name"`
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
	err := d.DB.UpdateGroup(db.Group{
		ID: id, Name: body.Name, DefaultPlaylistID: body.DefaultPlaylistID,
		ScreenOn: body.ScreenOn, ScreenOff: body.ScreenOff, ScreenDays: days,
	})
	if err != nil {
		if errors.Is(err, db.ErrDuplicate) {
			httpjson.Fields(w, "the request has a field that this server cannot use",
				db.Errors{{Field: "name", Message: "another group already has this name"}})
			return
		}
		if err.Error() == "a group needs a name" {
			httpjson.Fields(w, "the request has a field that this server cannot use",
				db.Errors{{Field: "name", Message: err.Error()}})
			return
		}
		fail(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// deleteGroup removes an empty group. A group that holds devices stays: the
// admin moves the devices first.
func (d Deps) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r, "id")
	if !okID {
		return
	}
	if err := d.DB.DeleteGroup(id); err != nil {
		if errors.Is(err, db.ErrInUse) {
			httpjson.Error(w, http.StatusConflict, "this group still holds screens; move them first")
			return
		}
		fail(w, err)
		return
	}
	d.Log.Log("group-delete", strconv.FormatInt(id, 10))
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// getAssignments lists the rules of one group or of one device, or all of them.
func (d Deps) getAssignments(w http.ResponseWriter, r *http.Request) {
	var groupID int64
	if v := r.URL.Query().Get("group_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			httpjson.Error(w, http.StatusBadRequest, "group_id is not a record number")
			return
		}
		groupID = n
	}
	list, err := d.DB.Assignments(groupID, r.URL.Query().Get("device_id"))
	if err != nil {
		fail(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"assignments": list})
}

// createAssignment makes one schedule rule.
func (d Deps) createAssignment(w http.ResponseWriter, r *http.Request) {
	a, ok2 := d.readAssignment(w, r, 0)
	if !ok2 {
		return
	}
	id, err := d.DB.SaveAssignment(a)
	if err != nil {
		fail(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"id": id})
}

// updateAssignment replaces one schedule rule.
func (d Deps) updateAssignment(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r, "id")
	if !okID {
		return
	}
	a, ok2 := d.readAssignment(w, r, id)
	if !ok2 {
		return
	}
	if _, err := d.DB.SaveAssignment(a); err != nil {
		fail(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// deleteAssignment removes one schedule rule.
func (d Deps) deleteAssignment(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r, "id")
	if !okID {
		return
	}
	if err := d.DB.DeleteAssignment(id); err != nil {
		fail(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// readAssignment reads the body of a rule and checks that its playlist and its
// owner are there.
func (d Deps) readAssignment(w http.ResponseWriter, r *http.Request, id int64) (db.Assignment, bool) {
	var body struct {
		GroupID    int64    `json:"group_id"`
		DeviceID   string   `json:"device_id"`
		PlaylistID int64    `json:"playlist_id"`
		Days       []string `json:"days"`
		Start      string   `json:"start"`
		End        string   `json:"end"`
		Priority   int      `json:"priority"`
	}
	if !httpjson.Read(w, r, &body) {
		return db.Assignment{}, false
	}
	if body.PlaylistID != 0 {
		if _, err := d.DB.PlaylistNoCount(body.PlaylistID); err != nil {
			fail(w, err)
			return db.Assignment{}, false
		}
	}
	if body.GroupID != 0 {
		if _, err := d.DB.Group(body.GroupID); err != nil {
			fail(w, err)
			return db.Assignment{}, false
		}
	}
	if body.DeviceID != "" {
		if _, err := d.DB.Device(body.DeviceID); err != nil {
			fail(w, err)
			return db.Assignment{}, false
		}
	}
	return db.Assignment{
		ID: id, GroupID: body.GroupID, DeviceID: body.DeviceID,
		PlaylistID: body.PlaylistID, Days: db.CleanDays(body.Days),
		Start: body.Start, End: body.End, Priority: body.Priority,
	}, true
}
