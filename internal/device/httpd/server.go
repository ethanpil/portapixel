package httpd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/browser"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
)

// PlayerHeader carries the boot secret when the player calls the API. EventSource
// cannot set a header, so the SSE URL carries the secret in ?k= instead
// (ARCHITECTURE 7a).
const PlayerHeader = "X-PortaPixel-Player"

// NotImplemented is the answer of a route that the route table names but that
// v0.1 does not have. The admin UI is written against the whole table, so the
// route must exist and must answer JSON.
const NotImplemented = "not implemented yet"

// ConfigView is the answer of GET /api/config.
type ConfigView struct {
	// Config holds the masked secrets: the API never sends the WiFi key, the
	// admin password or the fleet token to the network.
	Config     config.Config `json:"config"`
	FromShadow bool          `json:"from_shadow"`
	Warning    string        `json:"warning,omitempty"`
}

// Applied is the answer of PUT /api/config. It says what the device did with the
// change: applied it, or waits for a browser restart or a reboot.
type Applied struct {
	Applied string          `json:"applied"` // live | browser | reboot
	Changes []config.Change `json:"changes"`
}

// Deps are the functions and the objects that the handlers use. Everything that
// holds a rule is a function, so that the rule lives in its own package and a
// test can give a fake.
type Deps struct {
	MediaRoot string
	Log       *opslog.Log
	Library   *library.Library
	Sessions  *httpguard.Sessions
	Limiter   *httpguard.Limiter
	Hub       *Hub

	// PlayerSecret gates /api/player/*. It is 32 random hex characters, made at
	// each start of the daemon.
	PlayerSecret string

	// Hosts gives the Host header allowlist. It is a function, because the
	// addresses and the names change while the daemon runs.
	Hosts func() []string
	// Password gives the admin password of the configuration.
	Password func() string
	// Status builds the health report. loopback says if the caller is the device
	// itself, which is the only caller that may see the pairing code.
	Status func(loopback bool) manifest.Status
	// Config gives the masked configuration.
	Config func() ConfigView
	// SaveConfig checks, writes and applies a configuration.
	SaveConfig func(incoming config.Config) (Applied, error)
	// ActivePlaylist gives the name of the playlist that plays now.
	ActivePlaylist func() string
	// PlayerManifest builds the manifest of the active playlist.
	PlayerManifest func() library.PlayerManifest
	// Heartbeat takes one player heartbeat.
	Heartbeat func(browser.Heartbeat)
	// URLItem hands the browser to a URL item. It gives true when the player must
	// skip the item (D19).
	URLItem func(index int) (skip bool, err error)
	// PlayerReady is the answer of the player to a grace request.
	PlayerReady func()
	// Command runs a device command: reboot, restart-browser, screen-on,
	// screen-off, rescan.
	Command func(name string) error
	// AdminURL is the address that the QR code on the fallback screen carries.
	AdminURL func() string
	// SetRootPassword changes the root password of the system.
	SetRootPassword func(password string) error
}

// SecretBytes is the length of the boot secret before it becomes hexadecimal. It
// is the length that httpguard gives a session token: the player secret opens the
// same kind of door, so it gets the same strength.
const SecretBytes = 32

// NewSecret makes the boot secret of the player endpoints.
func NewSecret() string {
	var b [SecretBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any system that we run on. A secret from a
		// source that failed would be a secret of zeros, so the daemon must not
		// start with one.
		panic("httpd: the system gave no random bytes for the player secret: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// New builds the router with the hardening around it.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()

	// Static files. No session: the player must work before anybody logs in, and
	// the admin UI has to be able to show its own login form.
	mux.Handle("GET /", d.adminUI())
	mux.Handle("GET /shared/", d.sharedAssets())
	mux.Handle("GET /player", d.playerIndex())
	mux.Handle("GET /player/", d.playerAssets())
	mux.HandleFunc("GET /media/", d.serveMedia)

	// Open API.
	mux.HandleFunc("GET /api/status", d.getStatus)
	mux.HandleFunc("POST /api/login", d.postLogin)
	mux.HandleFunc("POST /api/logout", d.postLogout)
	mux.HandleFunc("GET /api/session", d.getSession)

	// The API of the admin UI. Every route needs a session.
	auth := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, d.Sessions.Require(h))
	}
	auth("GET /api/config", d.getConfig)
	auth("PUT /api/config", d.putConfig)
	auth("GET /api/playlists", d.getPlaylists)
	auth("POST /api/playlists", d.postPlaylist)
	auth("PUT /api/playlists/{name}", d.putPlaylist)
	auth("POST /api/playlists/{name}/rename", d.renamePlaylist)
	auth("DELETE /api/playlists/{name}", d.deletePlaylist)
	auth("POST /api/media/{playlist}", d.postMedia)
	auth("DELETE /api/media/{playlist}/{file}", d.deleteMedia)
	auth("POST /api/rescan", d.postRescan)
	auth("POST /api/commands/{name}", d.postCommand)
	auth("GET /api/opslog", d.getOpslog)
	auth("POST /api/system/root-password", d.postRootPassword)

	// The routes of the later milestones. They answer 501 so that the admin UI
	// can be written against the whole route table now (plan section 8).
	for _, pattern := range []string{
		"POST /api/pair", "DELETE /api/pair",
		"POST /api/update/check", "POST /api/update/apply",
		"GET /api/disks", "POST /api/install-to-disk",
	} {
		auth(pattern, notImplemented)
	}

	// The player API: the device itself, with the boot secret.
	player := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, httpguard.LoopbackOnly(d.requireSecret(h)))
	}
	player("GET /api/player/manifest", d.getPlayerManifest)
	player("POST /api/player/heartbeat", d.postHeartbeat)
	player("GET /api/player/events", d.Hub.serve)
	player("POST /api/player/url-item", d.postURLItem)
	player("POST /api/player/ready", d.postPlayerReady)
	player("GET /api/player/qr.svg", d.getQR)

	var h http.Handler = mux
	h = httpguard.RequireHeader(h)
	h = httpguard.HostAllowlist(d.Hosts)(h)
	return h
}

// requireSecret checks the boot secret of the player endpoints (D46). The secret
// comes in the header, or in ?k= for the SSE stream, which cannot set a header.
//
// httpguard.PasswordEqual does the comparison: it takes constant time and it
// refuses an empty value on either side. One helper owns that rule, so a second
// copy of it cannot drift.
func (d Deps) requireSecret(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		given := r.Header.Get(PlayerHeader)
		if given == "" {
			given = r.URL.Query().Get("k")
		}
		if !httpguard.PasswordEqual(given, d.PlayerSecret) {
			writeError(w, http.StatusForbidden, "this endpoint needs the player secret")
			return
		}
		next(w, r)
	}
}

// notImplemented answers a route of a later milestone.
func notImplemented(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, NotImplemented)
}

// writeJSON writes one JSON answer.
func writeJSON(w http.ResponseWriter, code int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		// The only way here is a value that cannot be JSON, which is a fault in
		// our own code, not in the request.
		data = []byte(`{"error":"the device could not build the answer"}`)
		code = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(data)
}

// writeError writes {"error": "..."}.
func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

// ok is the answer of a call that only has to work.
var ok = map[string]bool{"ok": true}

// readJSON reads a JSON body. It gives false and writes the error when the body
// is not the shape that the handler needs.
func readJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if err := dec.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "the request body is not the JSON that this route needs: "+err.Error())
		return false
	}
	return true
}

// maxJSONBody is the largest JSON body that the API reads. A playlist of a
// thousand items is under 100 kB, so a megabyte is generous. Media files do not
// come this way: they are the raw body of POST /api/media.
const maxJSONBody = 1 << 20
