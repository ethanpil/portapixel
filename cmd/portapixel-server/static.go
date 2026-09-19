package main

import (
	"mime"
	"net/http"
	"strings"

	"github.com/ethanpil/portapixel/web"
)

// The web assets have no build step, so the media types come from the file
// extensions. A Windows development machine reads them from the registry and can
// answer "text/plain" for a JavaScript module, which a browser then refuses to
// run. These lines make the answer the same on every machine.
func init() {
	for ext, kind := range map[string]string{
		".js":    "text/javascript; charset=utf-8",
		".mjs":   "text/javascript; charset=utf-8",
		".css":   "text/css; charset=utf-8",
		".html":  "text/html; charset=utf-8",
		".json":  "application/json",
		".svg":   "image/svg+xml",
		".woff2": "font/woff2",
	} {
		mime.AddExtensionType(ext, kind)
	}
}

// staticRoutes serves the admin UI and the shared assets from the binary.
//
// The UI engineer writes web/server-admin/ against the admin API. The files in
// the repository are the files that ship, and /shared/ is the same directory that
// the device UI uses, so the playlist editor cannot drift between the two
// (ARCHITECTURE section 8).
func (s *server) staticRoutes(mux *http.ServeMux) {
	mux.Handle("GET /", noListing(http.FileServerFS(web.ServerAdmin)))
	mux.Handle("GET /shared/", http.StripPrefix("/shared/", noListing(http.FileServerFS(web.Shared))))
}

// noListing answers 404 for a request that asks for a directory. An index of our
// own asset directories helps nobody and tells a reader what is in the binary.
//
// The root of the admin UI is the exception: it must serve its index.html.
func noListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
		case r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/"):
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
