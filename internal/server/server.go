// Package server builds the HTTP handler of the fleet server.
//
// Why this package exists: the route stack is the security boundary of the
// product. The Host allowlist, the CSRF header, the session guard and the choice
// of which subtree gets which of them are all here. The wiring lived in the
// command and again in the route tests, and the two copies had already drifted:
// the tests then proved what the test file built and not what the binary serves.
//
// One constructor, two callers. A guard that somebody removes here fails a test.
package server

import (
	"net/http"

	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/server/admin"
	"github.com/ethanpil/portapixel/internal/server/api"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
	"github.com/ethanpil/portapixel/web"
)

// Deps are the two route packages and the Host allowlist.
type Deps struct {
	// Device holds the routes of /api/v1.
	Device api.Deps
	// Admin holds the routes of /api/admin.
	Admin admin.Deps
	// Hosts gives the Host header allowlist of the browser side. It is a function
	// because the public URL changes while the server runs, and the allowlist must
	// change with it and not at the next restart.
	Hosts func() []string
}

// New gives the handler of the whole server.
//
// The device API and the admin UI get different guards, which is why they are two
// muxes. The device API takes no Host allowlist and no CSRF header: a device is
// not a browser, it carries a bearer token and no cookie, and its Host header is
// whatever the admin typed into its configuration. The browser side takes both
// guards (D46).
//
// Every path under /api/ answers JSON, the 404 and the 405 of the mux included.
// web/shared/api.js reads {"error": "..."} from each failure, and the text page of
// http.ServeMux would give it nothing to parse.
func New(d Deps) http.Handler {
	deviceMux := http.NewServeMux()
	d.Device.Routes(deviceMux)

	uiMux := http.NewServeMux()
	d.Admin.Routes(uiMux)
	staticRoutes(uiMux)

	var ui http.Handler = httpjson.NotFoundJSON(uiMux, "/api/")
	ui = httpguard.RequireHeader(ui)
	ui = httpguard.HostAllowlist(d.Hosts)(ui)

	root := http.NewServeMux()
	root.Handle("/api/v1/", httpjson.NotFoundJSON(deviceMux, "/api/"))
	root.Handle("/", ui)
	return root
}

// staticRoutes serves the admin UI and the shared assets from the binary.
//
// The UI engineer writes web/server-admin/ against the admin API. The files in the
// repository are the files that ship, and /shared/ is the same directory that the
// device UI uses, so the playlist editor cannot drift between the two
// (ARCHITECTURE section 8).
func staticRoutes(mux *http.ServeMux) {
	mux.Handle("GET /", web.NoListing(http.FileServerFS(web.ServerAdmin)))
	mux.Handle("GET /shared/", http.StripPrefix("/shared/", web.NoListing(http.FileServerFS(web.Shared))))
	mux.HandleFunc("GET /licenses", getLicenses)
}

// getLicenses gives the licence list of the third-party code (D33). The device
// serves the same file from the same embedded copy, so the two ends never show two
// different lists.
func getLicenses(w http.ResponseWriter, r *http.Request) {
	body, err := web.Licenses()
	if err != nil {
		http.Error(w, "the licence list is not in this build", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(body)
}
