// Command devserver serves the control server admin UI from disk and sends
// everything else to a running portapixel-server. It exists because the web
// assets go into the binary with go:embed, so without it every edit of a
// stylesheet needs a new build.
//
// It is a development tool. It is not in any release: the fleet server serves
// these files itself.
//
// Start the real server first, on a port of its own:
//
//	go build -o "$TMP/pps.exe" ./cmd/portapixel-server
//	"$TMP/pps.exe" run --data "$TMP/ppsdata" --listen 127.0.0.1:8095
//
// The first run prints the admin password one time. Then start this one and
// open http://localhost:8096/ :
//
//	go run ./tests/server-admin/devserver --addr 127.0.0.1:8096 --server http://127.0.0.1:8095
//
// The Host allowlist of the real server takes "localhost" and "127.0.0.1" with
// any port, so the proxy does not have to rewrite the Host header.
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

// proxied are the URL spaces that belong to the server: the admin API and the
// device API. Everything else is a file of the admin UI.
var proxied = []string{"/api/"}

func main() {
	addr := flag.String("addr", "127.0.0.1:8096", "the address to serve on")
	server := flag.String("server", "http://127.0.0.1:8095", "the address of the running portapixel-server")
	root := flag.String("web", "web", "the web directory of the repository")
	flag.Parse()

	// A Windows machine reads the media types from the registry and can answer
	// "text/plain" for a JavaScript module, which a browser then refuses to run.
	// The server binary does the same thing for the same reason.
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

	target, err := url.Parse(*server)
	if err != nil {
		log.Fatalf("devserver: the server address is not a URL: %v", err)
	}
	admin := filepath.Join(*root, "server-admin")
	shared := filepath.Join(*root, "shared")
	for _, dir := range []string{admin, shared} {
		if _, err := os.Stat(dir); err != nil {
			log.Fatalf("devserver: cannot read %s: %v (start this from the root of the repository)", dir, err)
		}
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	// An upload of a large file must not be held in a buffer.
	proxy.FlushInterval = -1

	adminFiles := noStore(http.FileServer(http.Dir(admin)))
	sharedFiles := http.StripPrefix("/shared/", noStore(http.FileServer(http.Dir(shared))))

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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

	log.Printf("devserver: %s serves %s and sends /api to %s", *addr, admin, target)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// noStore stops the browser from keeping a file that changed one second ago.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
