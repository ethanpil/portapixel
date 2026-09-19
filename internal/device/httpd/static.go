package httpd

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"

	"github.com/ethanpil/portapixel/internal/playlist"
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
//
// os.OpenRoot holds every open inside the media root, links included. The media
// partition comes from removable storage, and every laptop can write to it. A link
// in it must never reach /etc/shadow or the state directory. The daemon runs as
// root, and this is the door that must not open.
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

	root, err := os.OpenRoot(d.MediaRoot)
	if err != nil {
		writeError(w, http.StatusNotFound, "there is no file with this name")
		return
	}
	defer root.Close()

	f, err := root.Open(rel)
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
// It is an allowlist. A denylist was wrong. The media root holds portapixel.toml,
// which carries the admin password, the WiFi key and the fleet token. An atomic
// write of that file stages it under a temporary name of its own. One name that
// the denylist did not know gives the secrets of the device to the network, with
// no session at all.
//
// So /media/ serves a picture and a video and nothing else. The player needs
// nothing else, and the API is how a program reads a configuration or a playlist.
// The one reserved directory that may serve is _fleet/media/, which holds the
// objects of the fleet server.
func servableMedia(rel string) bool {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if p == "" || strings.HasPrefix(p, ".") {
			return false // a dotfile, and a name that a person cannot see
		}
		if !strings.HasPrefix(p, "_") {
			continue
		}
		// A reserved directory. Only the objects of the fleet server may serve.
		if i == 0 && p == "_fleet" && len(parts) > 2 && parts[1] == "media" {
			continue
		}
		return false
	}
	switch playlist.Kind(playlist.Item{File: parts[len(parts)-1]}) {
	case playlist.KindImage, playlist.KindVideo:
		return true
	default:
		return false
	}
}

// cleanRelative checks a path from a URL and gives the path under the media root.
//
// The test is about path steps, not about characters. A file that a person
// sideloaded can hold a colon or a backslash in its name. Both are legal on the
// ext4 media directory of an on-box install. The scan then calls the file healthy
// while /media/ answers 400, so the file can never play and nothing says why.
// os.OpenRoot in serveMedia is what holds the request inside the root.
//
// On a Windows development machine the two characters still go. A colon there is
// a drive letter or a stream name, and a backslash is a path separator.
func cleanRelative(rel string) (string, bool) {
	if rel == "" || strings.Contains(rel, "\x00") {
		return "", false
	}
	if strings.HasPrefix(rel, "/") {
		return "", false
	}
	if runtime.GOOS == "windows" && (strings.Contains(rel, `\`) || strings.Contains(rel, ":")) {
		return "", false
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	for _, p := range strings.Split(cleaned, "/") {
		if p == ".." {
			return "", false
		}
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
