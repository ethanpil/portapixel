package httpd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/browser"
	"github.com/ethanpil/portapixel/internal/device/installer"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/updater"
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
	// Rescan reads the media root again and looks for a sideloaded release bundle.
	// It is the work of POST /api/rescan, which is the documented curl hook after
	// a person copied files onto the stick (plan section 5, D52).
	Rescan func() library.Snapshot
	// AdminURL is the address that the QR code on the fallback screen carries.
	AdminURL func() string
	// SetRootPassword changes the root password of the system.
	SetRootPassword func(password string) error
	// SetCodecs takes the codec report that the player sends with its first
	// heartbeat (D12).
	SetCodecs func(manifest.CodecReport)

	// PairState gives the pairing state of GET /api/pair.
	PairState func() PairState
	// Pair starts a pairing with a server address and a token. An empty token
	// starts the code pairing (D25).
	Pair func(ctx context.Context, url, token string) (PairState, error)
	// Unpair forgets the fleet server and keeps the cached objects.
	Unpair func() error
	// Managed gives the name of the fleet server that owns the content of this
	// device. paired is false for a standalone device, and then nothing is locked
	// (D48).
	Managed func() (name string, paired bool)

	// CheckUpdate asks the release source for a newer release.
	CheckUpdate func(ctx context.Context) (updater.Release, error)
	// ApplyUpdate installs the release that the last check found.
	ApplyUpdate func(ctx context.Context) error
	// Disks gives the candidate targets of an install onto a disk (D54).
	Disks func() ([]installer.Disk, error)
	// StartInstall starts an install onto a disk. It gives an error for every
	// refusal, and then the progress stream never opens.
	StartInstall func(device, confirm string) error
	// InstallEvents is the progress stream of the install that runs.
	InstallEvents *Hub
	// ShareRoot is /usr/share/portapixel, which holds the licence list on a
	// device. An empty value uses the copy in the binary.
	ShareRoot string
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
	// known holds the same API patterns without their methods. It answers the
	// requests that fall through to the catch-all: a path that a route has, with a
	// method that it does not, gives 405; everything else under /api/ gives 404.
	//
	// A wrapper around the whole router did this before, and it could not tell the
	// 404 of the router from the 404 of a handler. "there is no playlist with this
	// name" became "this device has no route with this path". The wrapper also hid
	// the reader of http.MaxBytesReader, so an oversized body no longer closed the
	// connection.
	known := http.NewServeMux()
	paths := map[string]bool{}
	api := func(pattern string, h http.Handler) {
		mux.Handle(pattern, h)
		// One path can carry three methods, and a mux takes each pattern one time.
		if path := pathOf(pattern); !paths[path] {
			paths[path] = true
			known.Handle(path, http.HandlerFunc(methodNotAllowed))
		}
	}

	// Static files. No session: the player must work before anybody logs in, and
	// the admin UI has to be able to show its own login form.
	mux.Handle("GET /", d.adminUI())
	mux.Handle("GET /shared/", d.sharedAssets())
	mux.Handle("GET /player", d.playerIndex())
	mux.Handle("GET /player/", d.playerAssets())
	mux.HandleFunc("GET /media/", d.serveMedia)

	// Open API.
	api("GET /api/status", http.HandlerFunc(d.getStatus))
	api("POST /api/login", http.HandlerFunc(d.postLogin))
	api("POST /api/logout", http.HandlerFunc(d.postLogout))
	api("GET /api/session", http.HandlerFunc(d.getSession))

	// The API of the admin UI. Every route needs a session.
	auth := func(pattern string, h http.HandlerFunc) {
		api(pattern, d.Sessions.Require(h))
	}
	// fleet locks a route that the fleet server owns while the device is paired
	// (D48). PUT /api/config is not locked as a route: the local admin keeps the
	// rotation, the audio, the network and the passwords there, so that refusal is
	// per field and lives in the configuration save.
	fleet := func(pattern string, h http.HandlerFunc) {
		api(pattern, d.Sessions.Require(d.fleetGuard(h)))
	}
	auth("GET /api/config", d.getConfig)
	auth("PUT /api/config", d.putConfig)
	auth("GET /api/playlists", d.getPlaylists)
	fleet("POST /api/playlists", d.postPlaylist)
	fleet("PUT /api/playlists/{name}", d.putPlaylist)
	fleet("POST /api/playlists/{name}/rename", d.renamePlaylist)
	fleet("DELETE /api/playlists/{name}", d.deletePlaylist)
	auth("GET /api/media/{playlist}", d.getMedia)
	fleet("POST /api/media/{playlist}", d.postMedia)
	fleet("DELETE /api/media/{playlist}/{file}", d.deleteMedia)
	auth("POST /api/rescan", d.postRescan)
	auth("POST /api/commands/{name}", d.postCommand)
	auth("GET /api/opslog", d.getOpslog)
	auth("POST /api/system/root-password", d.postRootPassword)
	auth("POST /api/update/check", d.postUpdateCheck)
	auth("POST /api/update/apply", d.postUpdateApply)
	auth("GET /api/disks", d.getDisks)
	auth("POST /api/install-to-disk", d.postInstallToDisk)
	auth("GET /api/install-to-disk/events", d.installEvents())

	// The licence list. No session: a licence list is a public document, and the
	// About page opens it in a second tab (D33).
	mux.HandleFunc("GET /licenses", d.getLicenses)

	// Pairing (D25). GET is in the list too: without it the request would fall
	// through to the file server and get an HTML 404, which no caller of a JSON API
	// can read.
	auth("GET /api/pair", d.getPair)
	auth("POST /api/pair", d.postPair)
	auth("DELETE /api/pair", d.deletePair)

	// The player API: the device itself, with the boot secret.
	player := func(pattern string, h http.HandlerFunc) {
		api(pattern, httpguard.LoopbackOnly(d.requireSecret(h)))
	}
	player("GET /api/player/manifest", d.getPlayerManifest)
	player("POST /api/player/heartbeat", d.postHeartbeat)
	player("GET /api/player/events", d.Hub.serve)
	player("POST /api/player/url-item", d.postURLItem)
	player("POST /api/player/ready", d.postPlayerReady)
	player("GET /api/player/qr.svg", d.getQR)

	// The catch-all. Go 1.22 gives the more specific pattern to a request, so every
	// real route wins over it.
	//
	// It is registered for each method and not as one pattern with no method: a
	// pattern of "/api/" takes every method, and "GET /" takes every path, so the two
	// cross and the router refuses to hold both. HEAD is not in the list, because a
	// GET pattern already answers HEAD.
	known.HandleFunc("/api/", notFound)
	for _, method := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions,
	} {
		mux.Handle(method+" /api/", known)
	}

	var h http.Handler = mux
	h = httpguard.RequireHeader(h)
	h = httpguard.HostAllowlist(d.Hosts)(h)
	return h
}

// pathOf gives the path of a route pattern: "GET /api/status" becomes
// "/api/status". The catch-all mux registers the paths with no method, so it can
// tell a path that this device does not have from a method that a route does not
// take.
func pathOf(pattern string) string {
	if at := strings.IndexByte(pattern, ' '); at >= 0 {
		return pattern[at+1:]
	}
	return pattern
}

// notFound and methodNotAllowed are the two answers of the catch-all. Every failure
// under /api/ is JSON: web/shared/api.js reads {"error": "..."} from all of them, and
// the text page of http.ServeMux gave it nothing to parse.
func notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "this device has no route with this path")
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, "this route does not take this method")
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

// installEvents is the progress stream of GET /api/install-to-disk/events. A build
// with no installer still answers JSON, never an HTML 404.
func (d Deps) installEvents() http.HandlerFunc {
	if d.InstallEvents == nil {
		return notImplemented
	}
	return d.InstallEvents.serve
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
