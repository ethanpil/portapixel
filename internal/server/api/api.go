package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/media"
	"github.com/ethanpil/portapixel/internal/server/releases"
)

// The URL paths of the two file routes. The manifest gives these paths to the
// device, so one constant holds each of them.
const (
	MediaBase   = "/api/v1/media/"
	ReleaseBase = "/api/v1/releases/"
)

// Deps are the objects that the device routes use.
type Deps struct {
	DB     *db.DB
	Media  *media.Store
	Mirror *releases.Mirror
	Log    *opslog.Log
	// Limiter counts the failed enroll attempts of each address.
	Limiter *httpguard.Limiter
	// ServerName and DefaultPoll come from the settings.
	ServerName  func() string
	DefaultPoll func() int
}

// Routes adds the device routes to a mux.
//
// There is no session and no Host allowlist here. A device is not a browser: it
// carries a bearer token, it has no cookie to steal, and its Host header is
// whatever the admin typed into the device configuration. The admin routes have
// the browser guards.
func (d Deps) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/enroll", d.postEnroll)
	mux.HandleFunc("GET /api/v1/manifest", d.getManifest)
	mux.HandleFunc("POST /api/v1/heartbeat", d.postHeartbeat)
	mux.HandleFunc("GET /api/v1/media/{sha256}", d.getMedia)
	mux.HandleFunc("GET /api/v1/releases/{version}/{file}", d.getRelease)
}

// device reads the bearer token of a request and gives the device that holds it.
// It writes the error answer and gives false when the token is missing or wrong.
func (d Deps) device(w http.ResponseWriter, r *http.Request) (db.Device, bool) {
	token := bearer(r)
	if token == "" {
		WriteError(w, http.StatusUnauthorized, "this route needs the device token in an Authorization header")
		return db.Device{}, false
	}
	dev, err := d.DB.DeviceByToken(token)
	if err != nil {
		// One answer for "no such token" and for "the token was revoked". A
		// device that guesses tokens must learn nothing from the difference.
		WriteError(w, http.StatusUnauthorized, "this device token is not valid")
		return db.Device{}, false
	}
	return dev, true
}

// bearer gives the token of an Authorization header.
func bearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if len(v) < 7 || !strings.EqualFold(v[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(v[7:])
}

// peerIP gives the address of the caller with no port. The enroll limiter and
// the device row use it.
func peerIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return strings.Trim(r.RemoteAddr, "[]")
}

// maxJSONBody is the largest JSON body that these routes read. A heartbeat with
// a long list of warnings is a few kilobytes.
const maxJSONBody = 1 << 20

// WriteJSON writes one JSON answer.
//
// This helper and the two below also exist in internal/server/admin. The shapes
// {"error": "..."} and {"error": "...", "fields": [...]} are the contract with
// web/shared/api.js, and the two route packages answer with the same shapes. A
// change to one must go to the other; tests/server checks that they agree.
func WriteJSON(w http.ResponseWriter, code int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		// A value that cannot be JSON is a fault in our own code.
		data = []byte(`{"error":"the server could not build the answer"}`)
		code = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(data)
}

// WriteError writes {"error": "..."}.
func WriteError(w http.ResponseWriter, code int, message string) {
	WriteJSON(w, code, map[string]string{"error": message})
}

// WriteFields writes the 422 answer: {"error": "...", "fields": [...]}.
func WriteFields(w http.ResponseWriter, message string, fields db.Errors) {
	WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error":  message,
		"fields": fields,
	})
}

// ReadJSON reads a JSON body and writes the error answer when it is not the
// shape that the route needs.
func ReadJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err := dec.Decode(into); err != nil {
		WriteError(w, http.StatusBadRequest, "the request body is not the JSON that this route needs: "+err.Error())
		return false
	}
	return true
}
