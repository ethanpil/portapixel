package api

import (
	"net/http"
	"strings"

	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
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
	// Limiter counts the enroll attempts of each address.
	Limiter *httpguard.Limiter
	// PendingLimiter counts the enroll requests that make a pending row. A
	// waiting device polls with a good claim secret and does not count.
	PendingLimiter *httpguard.Limiter
	// ClientIP gives the address of a caller. The caller of this package decides
	// the rule, because a reverse proxy changes the answer and the configuration
	// names the proxies that the server believes.
	ClientIP func(*http.Request) string
	// Fleet gives the values that every manifest needs. The caller caches them,
	// so a poll costs no query for them.
	Fleet func() Fleet
}

// Fleet holds the values that the manifest needs and that no device row has.
type Fleet struct {
	// ServerName is the name that a device shows for this server.
	ServerName string
	// DefaultPoll is the poll interval of a device with no value of its own.
	DefaultPoll int
	// Release is the approved release, or nil when none goes out.
	Release *db.Release
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
		httpjson.Error(w, http.StatusUnauthorized,
			"this route needs the device token in an Authorization header")
		return db.Device{}, false
	}
	dev, err := d.DB.DeviceByToken(token)
	if err != nil {
		// One answer for "no such token" and for "the token was revoked". A
		// device that guesses tokens must learn nothing from the difference.
		// The code says that the token itself is gone, so the device drops its
		// pairing. A 401 with no code is a fault of something in between, and the
		// device then keeps the pairing and waits (httpjson.TokenRevokedCode).
		httpjson.Revoked(w, "this device token is not valid")
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

// clientIP gives the address of the caller. One rule decides it for the enroll
// limiter, the login limiter and the last_ip column, so a reverse proxy cannot make
// the three disagree.
func (d Deps) clientIP(r *http.Request) string { return d.ClientIP(r) }
