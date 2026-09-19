package httpd

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/browser"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/playlist"
)

// minRootPassword is the shortest root password that the API accepts. The device
// answers SSH on the network, so a two character password is not a password.
const minRootPassword = 8

// defaultOpslogLines is how many lines GET /api/opslog gives without a count.
const defaultOpslogLines = 200

// GET /api/status
//
// No session: the fallback screen on the device and a person with a browser both
// read it. The pairing code goes to the device itself only (D46).
func (d Deps) getStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Status(httpguard.IsLoopback(r.RemoteAddr)))
}

// POST /api/login {"password": "..."}
func (d Deps) postLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !d.Limiter.Allow(r.RemoteAddr) {
		writeError(w, http.StatusTooManyRequests, "too many attempts. Wait one minute.")
		return
	}
	if !httpguard.PasswordEqual(body.Password, d.Password()) {
		d.Limiter.Fail(r.RemoteAddr)
		d.Log.Log("web.login.fail", "from "+hostOf(r.RemoteAddr))
		writeError(w, http.StatusUnauthorized, "the password is not correct")
		return
	}
	d.Limiter.Reset(r.RemoteAddr)
	d.Sessions.Login(w)
	d.Log.Log("web.login", "from "+hostOf(r.RemoteAddr))
	writeJSON(w, http.StatusOK, ok)
}

// POST /api/logout
func (d Deps) postLogout(w http.ResponseWriter, r *http.Request) {
	d.Sessions.Logout(w, r)
	writeJSON(w, http.StatusOK, ok)
}

// GET /api/session tells the admin UI if it must show the login form.
func (d Deps) getSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": d.Sessions.Valid(r)})
}

// GET /api/config
func (d Deps) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Config())
}

// PUT /api/config takes the configuration object, the same shape that GET gives
// in its "config" field. A secret that still holds the mask keeps the value that
// the device has; an empty secret clears it.
func (d Deps) putConfig(w http.ResponseWriter, r *http.Request) {
	// Start from the current values, so that a body that leaves a key out does
	// not reset that key.
	//
	// The value must share no memory with the configuration that runs.
	// encoding/json writes into the elements of a slice that is already there. A
	// body with a schedule in it then changes the rules under the scheduler
	// goroutine. It does that even when the handler refuses the body with 422. A
	// round trip through JSON is the copy that this package can make on its own.
	base, err := json.Marshal(d.Config().Config)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the device could not read its own configuration")
		return
	}
	var incoming config.Config
	if err := json.Unmarshal(base, &incoming); err != nil {
		writeError(w, http.StatusInternalServerError, "the device could not read its own configuration")
		return
	}
	if !readJSON(w, r, &incoming) {
		return
	}
	applied, saveErr := d.SaveConfig(incoming)
	if err := saveErr; err != nil {
		var fields config.Errors
		if errors.As(err, &fields) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error":  "some settings are not correct",
				"fields": fields,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, applied)
}

// GET /api/playlists
func (d Deps) getPlaylists(w http.ResponseWriter, r *http.Request) {
	snap := d.Library.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"playlists": emptyIfNil(snap.Playlists),
		"problems":  emptyIfNil(snap.Problems),
		"active":    d.ActivePlaylist(),
		"hashing":   snap.Hashing,
	})
}

// POST /api/playlists {"title": "Lobby Loop"}
func (d Deps) postPlaylist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, err := d.Library.CreatePlaylist(body.Title)
	if err != nil {
		writeLibraryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// PUT /api/playlists/{name} writes playlist.toml again. The admin UI asks the
// user about the lost comments before it calls this (D15).
func (d Deps) putPlaylist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title      string          `json:"title"`
		Transition string          `json:"transition"`
		Shuffle    *bool           `json:"shuffle"`
		Items      []playlist.Item `json:"items"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	p := playlist.Playlist{
		Meta:  playlist.Meta{Name: body.Title, Transition: body.Transition, Shuffle: body.Shuffle},
		Items: body.Items,
	}
	if err := d.Library.SavePlaylist(r.PathValue("name"), p); err != nil {
		writeLibraryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ok)
}

// POST /api/playlists/{name}/rename {"title": "Front Desk"}
//
// The answer carries the new name, because the name is the directory name and
// every schedule rule that names the playlist must be corrected by the UI.
func (d Deps) renamePlaylist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name, err := d.Library.RenamePlaylist(r.PathValue("name"), body.Title)
	if err != nil {
		writeLibraryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// DELETE /api/playlists/{name}
func (d Deps) deletePlaylist(w http.ResponseWriter, r *http.Request) {
	if err := d.Library.DeletePlaylist(r.PathValue("name")); err != nil {
		writeLibraryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ok)
}

// POST /api/media/{playlist} takes the file as the raw body. The name comes in
// X-Filename, so neither end needs a multipart parser.
//
// The header is escaped with encodeURIComponent, so it is unescaped as a path and
// not as a query. url.QueryUnescape turns a plus sign into a space, and
// "C++ intro.mp4" is a name that a person gives a file.
//
// The library caps the stream at the free space of the partition and answers 507
// when it does not fit. One upload must never fill PPMEDIA: every save of the
// device writes there.
func (d Deps) postMedia(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	name := r.Header.Get("X-Filename")
	if unescaped, err := url.PathUnescape(name); err == nil {
		name = unescaped
	}
	if strings.TrimSpace(name) == "" {
		writeError(w, http.StatusBadRequest, "the X-Filename header must carry the name of the file")
		return
	}
	saved, size, err := d.Library.AddMedia(r.PathValue("playlist"), name, r.Body, r.ContentLength)
	if err != nil {
		writeLibraryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": saved, "size": size})
}

// DELETE /api/media/{playlist}/{file}
func (d Deps) deleteMedia(w http.ResponseWriter, r *http.Request) {
	if err := d.Library.DeleteMedia(r.PathValue("playlist"), r.PathValue("file")); err != nil {
		writeLibraryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ok)
}

// POST /api/rescan is also the documented curl hook after a sideload (plan
// section 5).
func (d Deps) postRescan(w http.ResponseWriter, r *http.Request) {
	snap := d.Library.Rescan()
	writeJSON(w, http.StatusOK, map[string]any{
		"playlists": len(snap.Playlists),
		"problems":  emptyIfNil(snap.Problems),
	})
}

// POST /api/commands/{name}
//
// A browser that is busy answers 503. A command that reports success and then
// does nothing is worse than an error: the person looks at the screen and waits.
func (d Deps) postCommand(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := d.Command(name); err != nil {
		if errors.Is(err, browser.ErrBusy) {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ok)
}

// GET /api/opslog?n=200
func (d Deps) getOpslog(w http.ResponseWriter, r *http.Request) {
	lines := defaultOpslogLines
	if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && n > 0 && n <= 1200 {
		lines = n
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": emptyIfNil(d.Log.Tail(lines))})
}

// POST /api/system/root-password {"password": "..."}
//
// The root password is not in portapixel.toml: a password on a partition that
// every laptop can read is not a password. The API changes the system password
// and the warning goes away at the next status call (D23).
func (d Deps) postRootPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Password) < minRootPassword {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "the password is too short",
			"fields": []config.FieldError{{
				Field:   "password",
				Message: "must be " + strconv.Itoa(minRootPassword) + " characters or more",
			}},
		})
		return
	}
	if d.SetRootPassword == nil {
		writeError(w, http.StatusNotImplemented, NotImplemented)
		return
	}
	if err := d.SetRootPassword(body.Password); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	d.Log.Log("system.rootpw", "the root password changed")
	writeJSON(w, http.StatusOK, ok)
}

// writeLibraryError turns a library fault into the right status code.
func writeLibraryError(w http.ResponseWriter, err error) {
	var fields playlist.Errors
	switch {
	case errors.As(err, &fields):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":  "the playlist is not correct",
			"fields": fields,
		})
	case errors.Is(err, library.ErrNotFound), errors.Is(err, library.ErrNoFile):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, library.ErrExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, library.ErrBadName):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, library.ErrManaged):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, library.ErrNoSpace):
		writeError(w, http.StatusInsufficientStorage, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// hostOf gives the address without the port. It is for a log line only. The
// loopback test is httpguard.IsLoopback: the host allowlist, the login limiter and
// this package must all cut an address the same way.
func hostOf(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(remoteAddr, "[]")
}

// emptyIfNil makes a nil slice into an empty list, so that the JSON holds [] and
// not null. A UI that must test for both is a UI with a bug waiting in it.
func emptyIfNil[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}
