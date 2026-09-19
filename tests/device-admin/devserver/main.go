// Command devserver serves the device admin UI from disk and sends everything
// else to a running portapixeld. It exists because the web assets go into the
// binary with go:embed, so without it every edit of a stylesheet needs a new
// build.
//
// It is a development tool. It is not in the image and it is not in any
// release: the daemon serves these files itself on a device.
//
// Start the real daemon first, on a port of its own:
//
//	go build -o "$TMP/ppd.exe" ./cmd/portapixeld
//	"$TMP/ppd.exe" provision --media "$TMP/pp/media" --state "$TMP/pp/state"
//	"$TMP/ppd.exe" run --media "$TMP/pp/media" --state "$TMP/pp/state" \
//	    --run "$TMP/pp/run" --releases "$TMP/pp/releases" \
//	    --listen 127.0.0.1:8098 --browser-cmd none
//
// Then start this one and open http://localhost:8099/ :
//
//	go run ./tests/device-admin/devserver --addr 127.0.0.1:8099 --daemon http://127.0.0.1:8098
package main

import (
	"flag"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// proxied are the URL spaces that belong to the daemon: the JSON API, the media
// files and the player. Everything else is a file of the admin UI.
var proxied = []string{"/api/", "/media/", "/player"}

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "the address to serve on")
	daemon := flag.String("daemon", "http://127.0.0.1:8098", "the address of the running portapixeld")
	root := flag.String("web", "web", "the web directory of the repository")
	withFake := flag.Bool("fake", false, "answer the routes that v0.1 has not got yet: pairing, updates and install-to-disk")
	flag.Parse()

	// A Windows machine reads the media types from the registry and can answer
	// "text/plain" for a JavaScript module, which a browser then refuses to run.
	// The daemon does the same thing for the same reason.
	for ext, kind := range map[string]string{
		".js":    "text/javascript; charset=utf-8",
		".css":   "text/css; charset=utf-8",
		".html":  "text/html; charset=utf-8",
		".json":  "application/json",
		".svg":   "image/svg+xml",
		".woff2": "font/woff2",
	} {
		mime.AddExtensionType(ext, kind)
	}

	target, err := url.Parse(*daemon)
	if err != nil {
		log.Fatalf("devserver: the daemon address is not a URL: %v", err)
	}
	admin := filepath.Join(*root, "device-admin")
	shared := filepath.Join(*root, "shared")
	for _, dir := range []string{admin, shared} {
		if _, err := os.Stat(dir); err != nil {
			log.Fatalf("devserver: cannot read %s: %v (start this from the root of the repository)", dir, err)
		}
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	// The install-to-disk stream and the player stream never end, so nothing may
	// be held in a buffer.
	proxy.FlushInterval = -1

	adminFiles := noStore(http.FileServer(http.Dir(admin)))
	sharedFiles := http.StripPrefix("/shared/", noStore(http.FileServer(http.Dir(shared))))

	var pretend *fake
	if *withFake {
		pretend = newFake()
		log.Printf("devserver: the fake pairing, update and install-to-disk routes are on")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if pretend != nil && fakePath(r.URL.Path) && pretend.handle(w, r, *daemon) {
			return
		}
		for _, prefix := range proxied {
			if strings.HasPrefix(r.URL.Path, prefix) {
				proxy.ServeHTTP(w, r)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/shared/") {
			sharedFiles.ServeHTTP(w, r)
			return
		}
		adminFiles.ServeHTTP(w, r)
	})

	log.Printf("devserver: %s serves %s and sends /api, /media and /player to %s", *addr, admin, target)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// noStore stops the browser from keeping a file that changed one second ago.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
