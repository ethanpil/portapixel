package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/media"
	"github.com/ethanpil/portapixel/internal/server/releases"
)

// Settings is what the admin UI may change while the server runs.
type Settings struct {
	ServerName  string `json:"server_name"`
	PollSeconds int    `json:"default_poll_seconds"`
	PublicURL   string `json:"public_url"`
	// QuietAfterSeconds is 2.5 poll intervals. The UI shows it; it is computed,
	// not stored, so that one rule says when a device is quiet.
	QuietAfterSeconds int `json:"quiet_after_seconds"`
	// WeakPassword is true while the admin still uses the password that the
	// first run made. The UI shows a loud banner.
	WeakPassword bool `json:"weak_password"`
}

// Deps are the objects and the functions that the admin routes use.
type Deps struct {
	DB       *db.DB
	Media    *media.Store
	Mirror   *releases.Mirror
	Log      *opslog.Log
	Sessions *httpguard.Sessions
	Limiter  *httpguard.Limiter

	// DataDir is the directory that holds the database and the media. The health
	// page reports its free space.
	DataDir string
	// StartedAt is when the server came up. The health page shows the uptime.
	StartedAt time.Time

	// Hosts gives the Host header allowlist.
	Hosts func() []string
	// CheckPassword compares a password with the stored hash.
	CheckPassword func(password string) bool
	// SetPassword writes a new admin password into server.toml.
	SetPassword func(password string) error
	// Settings gives the current settings.
	Settings func() Settings
	// SaveSettings writes the settings. The values that live in server.toml need
	// a restart, and the answer says so.
	SaveSettings func(s Settings) error
}

// Routes gives the handler of /api/admin with its guards.
func (d Deps) Routes(mux *http.ServeMux) {
	// Open routes. A login form must work before there is a session.
	mux.HandleFunc("POST /api/admin/login", d.postLogin)
	mux.HandleFunc("POST /api/admin/logout", d.postLogout)
	mux.HandleFunc("GET /api/admin/session", d.getSession)

	// Every other route needs a session.
	auth := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, d.Sessions.Require(h))
	}

	auth("GET /api/admin/devices", d.getDevices)
	auth("GET /api/admin/devices/{id}", d.getDevice)
	auth("POST /api/admin/devices/{id}/rename", d.renameDevice)
	auth("POST /api/admin/devices/{id}/group", d.moveDevice)
	auth("POST /api/admin/devices/{id}/overrides", d.setOverrides)
	auth("POST /api/admin/devices/{id}/approve", d.approveDevice)
	auth("POST /api/admin/devices/{id}/reject", d.rejectDevice)
	auth("POST /api/admin/devices/{id}/confirm-hardware", d.confirmHardware)
	auth("POST /api/admin/devices/{id}/resolve-conflict", d.resolveConflict)
	auth("POST /api/admin/devices/{id}/commands", d.queueCommand)
	auth("GET /api/admin/devices/{id}/commands", d.getCommands)
	auth("DELETE /api/admin/devices/{id}", d.deleteDevice)
	auth("POST /api/admin/pending/{code}/approve", d.approveByCode)
	auth("POST /api/admin/pending/{code}/reject", d.rejectByCode)

	auth("GET /api/admin/groups", d.getGroups)
	auth("POST /api/admin/groups", d.createGroup)
	auth("PUT /api/admin/groups/{id}", d.updateGroup)
	auth("DELETE /api/admin/groups/{id}", d.deleteGroup)
	auth("POST /api/admin/groups/{id}/commands", d.queueGroupCommand)

	auth("GET /api/admin/assignments", d.getAssignments)
	auth("POST /api/admin/assignments", d.createAssignment)
	auth("PUT /api/admin/assignments/{id}", d.updateAssignment)
	auth("DELETE /api/admin/assignments/{id}", d.deleteAssignment)

	auth("GET /api/admin/playlists", d.getPlaylists)
	auth("GET /api/admin/playlists/{id}", d.getPlaylist)
	auth("POST /api/admin/playlists", d.createPlaylist)
	auth("PUT /api/admin/playlists/{id}", d.updatePlaylist)
	auth("DELETE /api/admin/playlists/{id}", d.deletePlaylist)
	auth("GET /api/admin/playlists/{id}/devices", d.getPlaylistDevices)

	auth("GET /api/admin/media", d.getMediaList)
	auth("POST /api/admin/media", d.postMedia)
	auth("DELETE /api/admin/media/{sha256}", d.deleteMedia)
	auth("GET /api/admin/media/{sha256}/thumb", d.getThumb)

	auth("GET /api/admin/tokens", d.getTokens)
	auth("POST /api/admin/tokens", d.createToken)
	auth("POST /api/admin/tokens/{id}/revoke", d.revokeToken)
	auth("DELETE /api/admin/tokens/{id}", d.deleteToken)

	auth("GET /api/admin/releases", d.getReleases)
	auth("POST /api/admin/releases/refresh", d.refreshReleases)
	auth("POST /api/admin/releases/{version}/approve", d.approveRelease)
	auth("POST /api/admin/releases/{version}/unapprove", d.unapproveRelease)
	auth("POST /api/admin/releases/{version}/mirror", d.mirrorRelease)
	auth("POST /api/admin/releases/{version}/bundle", d.uploadBundle)

	auth("GET /api/admin/health", d.getHealth)
	auth("POST /api/admin/health/integrity", d.postIntegrity)

	auth("GET /api/admin/settings", d.getSettings)
	auth("PUT /api/admin/settings", d.putSettings)
	auth("POST /api/admin/password", d.postPassword)
}

// The helpers below mirror the ones in internal/server/api. The shapes
// {"error": "..."} and {"error": "...", "fields": [...]} are the contract with
// web/shared/api.js, and both route packages answer with the same shapes.

// maxJSONBody is the largest JSON body that these routes read. A playlist of a
// thousand items is under 100 kB.
const maxJSONBody = 1 << 20

func writeJSON(w http.ResponseWriter, code int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		data = []byte(`{"error":"the server could not build the answer"}`)
		code = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(data)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

func writeFields(w http.ResponseWriter, message string, fields db.Errors) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error":  message,
		"fields": fields,
	})
}

func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err := dec.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "the request body is not the JSON that this route needs: "+err.Error())
		return false
	}
	return true
}

// ok is the answer of a call that only has to work.
var ok = map[string]bool{"ok": true}

// fail writes the answer of an error from the db package. One function holds the
// mapping, so every route answers 404, 409 and 422 for the same reasons.
func fail(w http.ResponseWriter, err error) {
	var fieldErrs db.Errors
	switch {
	case errors.As(err, &fieldErrs):
		writeFields(w, "the request has a field that this server cannot use", fieldErrs)
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "there is no record with this name")
	case errors.Is(err, db.ErrInUse):
		writeError(w, http.StatusConflict, "something still uses this record")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// pathID reads a number from the path. It writes the error answer and gives
// false when the value is not a number.
func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	v, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || v <= 0 {
		writeError(w, http.StatusBadRequest, "the "+name+" in the path is not a record number")
		return 0, false
	}
	return v, true
}

// defaultPoll gives the poll interval of a device with no value of its own.
func (d Deps) defaultPoll() int { return d.Settings().PollSeconds }
