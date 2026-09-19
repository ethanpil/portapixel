package httpd

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
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

// PlayerIndex is the file that the browser opens at /player.
const PlayerIndex = "index.html"

// adminUI serves the device admin UI at /.
func (d Deps) adminUI() http.Handler {
	return noListing(http.FileServerFS(web.DeviceAdmin))
}

// sharedAssets serves the shared stylesheet, fonts and modules at /shared/.
func (d Deps) sharedAssets() http.Handler {
	return http.StripPrefix("/shared/", noListing(http.FileServerFS(web.Shared)))
}

// playerIndex serves the player SPA at /player. The browser opens
// /player?k=<secret>, so this is the one route that must answer a file for a URL
// with no file name in it.
func (d Deps) playerIndex() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(web.Player, PlayerIndex)
		if err != nil {
			// The player is not built into this binary yet. Say so in words: a
			// black screen with a 404 on it teaches nobody anything.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("PortaPixel: the player is not in this build yet.\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(data)
	})
}

// playerAssets serves the files of the player at /player/.
func (d Deps) playerAssets() http.Handler {
	return http.StripPrefix("/player/", noListing(http.FileServerFS(web.Player)))
}

// serveMedia serves the files of the media root at /media/.
//
// http.ServeContent gives us Range requests, so a browser can seek in a video and
// a fleet client can continue a download (D24). There are no directory listings:
// the media partition is the user's own content, and the API is how a program
// looks at it.
func (d Deps) serveMedia(w http.ResponseWriter, r *http.Request) {
	rel, ok := cleanRelative(strings.TrimPrefix(r.URL.Path, "/media/"))
	if !ok {
		writeError(w, http.StatusBadRequest, "that is not a path inside the media directory")
		return
	}
	if !servableMedia(rel) {
		writeError(w, http.StatusNotFound, "there is no file with this name")
		return
	}
	full := filepath.Join(d.MediaRoot, filepath.FromSlash(rel))

	f, err := os.Open(full)
	if err != nil {
		writeError(w, http.StatusNotFound, "there is no file with this name")
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "there is no file with this name")
		return
	}
	http.ServeContent(w, r, path.Base(rel), info.ModTime(), f)
}

// servableMedia says which files under the media root /media/ may serve.
//
// The media root holds content, and it also holds portapixel.toml, which carries
// the admin password, the WiFi key and the fleet token. That file must never leave
// the device over HTTP, so no TOML file is served: the API is how a program reads
// a configuration or a playlist. The _update directory holds release bundles,
// which are for the updater and not for the network.
func servableMedia(rel string) bool {
	lower := strings.ToLower(rel)
	if strings.HasSuffix(lower, ".toml") {
		return false
	}
	if lower == "_update" || strings.HasPrefix(lower, "_update/") {
		return false
	}
	return true
}

// cleanRelative checks a path from a URL. It refuses an empty path, an absolute
// path and every path that goes up a directory: the daemon runs as root, so this
// is the door that must not open.
func cleanRelative(rel string) (string, bool) {
	if rel == "" || strings.Contains(rel, "\x00") {
		return "", false
	}
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) {
		return "", false
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	// A Windows drive letter or a stream name.
	if strings.Contains(cleaned, ":") {
		return "", false
	}
	return cleaned, true
}

// noListing answers 404 for a request that asks for a directory. An index of our
// own asset directories helps nobody and tells a reader what is in the binary.
//
// The one exception is the root of the admin UI, which must serve its index.html.
// A path that StripPrefix left empty is the root of that sub-tree, so it is a
// directory too.
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
