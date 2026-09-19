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
//
// --fake-release-key makes the Versions page usable on a development build.
// internal/version.PublicKey is a constant and it is empty until release 1, so
// the server answers has_key:false and the page says "this build cannot mirror
// releases" and nothing else. With the flag the proxy turns that one field to
// true on its way back, and the approval controls, the fleet bars and the
// release rows can all be driven. Nothing else changes: an approval still goes
// to the real server, and the mirror still refuses to verify.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"io"
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
	fakeKey := flag.Bool("fake-release-key", false,
		"answer has_key:true on the release routes, so the Versions page can be driven on a development build")
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
	if *fakeKey {
		proxy.ModifyResponse = fakeReleaseKey
	}

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

// fakeReleaseKey turns has_key and has_release_key to true on their way back to
// the browser. It touches nothing else, so every number and every release row is
// still the answer of the real server.
//
// It exists because internal/version.PublicKey is a constant: -ldflags -X cannot
// set it, so a development build cannot be given a key without a change to the
// Go code. This is the smallest way to see the rest of the Versions page.
func fakeReleaseKey(res *http.Response) error {
	path := res.Request.URL.Path
	if !strings.HasPrefix(path, "/api/admin/releases") && path != "/api/admin/health" {
		return nil
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		return nil
	}
	body, err := readBody(res)
	if err != nil {
		return err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil // not an object: leave it as it came
	}
	if _, ok := out["has_key"]; ok {
		out["has_key"] = true
	}
	if _, ok := out["has_release_key"]; ok {
		out["has_release_key"] = true
	}
	next, err := json.Marshal(out)
	if err != nil {
		return err
	}
	res.Body = io.NopCloser(bytes.NewReader(next))
	res.Header.Del("Content-Encoding")
	res.Header.Set("Content-Length", itoa(len(next)))
	res.ContentLength = int64(len(next))
	return nil
}

// readBody reads the whole answer, gzip included.
func readBody(res *http.Response) ([]byte, error) {
	var r io.Reader = res.Body
	if res.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(res.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}
	body, err := io.ReadAll(r)
	res.Body.Close()
	return body, err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// noStore stops the browser from keeping a file that changed one second ago.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
