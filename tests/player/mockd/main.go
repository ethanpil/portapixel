// Command mockd is a small mock of portapixeld. It is a tool for work on the
// player SPA. It serves web/player and web/shared from disk, and it answers the
// player protocol of docs/ARCHITECTURE.md section 7a.
//
// Only the standard library is used. The daemon makes its test pictures itself,
// so the repository holds no test media. Video needs a file from outside:
// give --video PATH. Scenarios without video work without it.
//
// Start it from the root of the repository:
//
//	go run ./tests/player/mockd --addr 127.0.0.1:8088
//
// Then open:
//
//	http://127.0.0.1:8088/mock/                     the control page
//	http://127.0.0.1:8088/player?k=devsecret        the player
//
// The control page changes the scenario, the transition and the transition
// time, and it sends the three SSE events.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math/rand"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/device/browser"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/manifest"
)

/* ------------------------------------------------------------ wire format */

// The mock answers the same types that the daemon answers. A copy of the structs
// here drifted from the real ones. The tags of two fields were different, so the
// mock told the player something that the device never says.
type (
	item           = library.ManifestItem
	mockPlaylist   = library.ManifestPlaylist
	playerManifest = library.PlayerManifest
	heartbeat      = browser.Heartbeat
)

/* -------------------------------------------------------------- the server */

type mock struct {
	web    string
	secret string
	video  string

	mu         sync.Mutex
	scenario   string
	transition string
	ms         int
	lastFrames int64
	beats      int
	clients    map[chan string]bool
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8088", "listen address")
	web := flag.String("web", "", "path of the web directory (default: found from the working directory)")
	scenario := flag.String("scenario", "mixed", "start scenario, see /mock/")
	transition := flag.String("transition", "crossfade", "crossfade | push-left | push-right | push-up | push-down | cut")
	ms := flag.Int("ms", 700, "transition time in milliseconds")
	video := flag.String("video", "", "path of a video file for the video scenarios")
	secret := flag.String("k", "devsecret", "the player secret")
	flag.Parse()

	root := *web
	if root == "" {
		found, err := findWeb()
		if err != nil {
			log.Fatal(err)
		}
		root = found
	}
	if *video != "" {
		if _, err := os.Stat(*video); err != nil {
			log.Fatalf("--video: %v", err)
		}
	}

	// Windows takes the media type of a file from the registry, where .js can
	// be text/plain. A module script with the wrong type does not run.
	mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	mime.AddExtensionType(".css", "text/css; charset=utf-8")
	mime.AddExtensionType(".svg", "image/svg+xml")
	mime.AddExtensionType(".woff2", "font/woff2")

	m := &mock{
		web: root, secret: *secret, video: *video,
		scenario: *scenario, transition: *transition, ms: *ms,
		clients: map[chan string]bool{},
	}

	log.Printf("web root      %s", root)
	log.Printf("player        http://%s/player?k=%s", *addr, *secret)
	log.Printf("control page  http://%s/mock/", *addr)
	log.Printf("scenario      %s (%s, %d ms)", m.scenario, m.transition, m.ms)
	if err := http.ListenAndServe(*addr, m.routes()); err != nil {
		log.Fatal(err)
	}
}

func (m *mock) routes() http.Handler {
	mux := http.NewServeMux()

	// The pages and their assets.
	mux.HandleFunc("GET /player", func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		http.ServeFile(w, r, filepath.Join(m.web, "player", "index.html"))
	})
	mux.Handle("GET /player/", noStoreHandler(http.StripPrefix("/player/",
		http.FileServer(http.Dir(filepath.Join(m.web, "player"))))))
	mux.Handle("GET /shared/", noStoreHandler(http.StripPrefix("/shared/",
		http.FileServer(http.Dir(filepath.Join(m.web, "shared"))))))

	// The test media.
	mux.HandleFunc("GET /media/gen/{name}", m.genMedia)
	mux.HandleFunc("GET /media/video/{name}", m.videoMedia)
	mux.HandleFunc("GET /media/hang/{name}", m.hangMedia)
	mux.HandleFunc("GET /media/missing/{name}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	// The API.
	mux.HandleFunc("GET /api/status", m.status)
	mux.HandleFunc("GET /api/player/manifest", m.manifest)
	mux.HandleFunc("POST /api/player/heartbeat", m.heartbeat)
	mux.HandleFunc("POST /api/player/url-item", m.urlItem)
	mux.HandleFunc("POST /api/player/ready", m.ready)
	mux.HandleFunc("GET /api/player/events", m.events)
	mux.HandleFunc("GET /api/player/qr.svg", m.qr)

	// The control page.
	mux.HandleFunc("GET /mock/", m.controlPage)
	mux.HandleFunc("GET /mock/set", m.set)
	mux.HandleFunc("GET /mock/sse", m.push)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/mock/", http.StatusFound)
	})
	return mux
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

func noStoreHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		h.ServeHTTP(w, r)
	})
}

// findWeb looks for the web directory above the working directory.
func findWeb() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 8; i++ {
		try := filepath.Join(dir, "web")
		if _, err := os.Stat(filepath.Join(try, "player", "index.html")); err == nil {
			return try, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("web/player/index.html is not above %q; use --web", dir)
}

/* ------------------------------------------------------------- the secret */

// gate holds the rule of section 6: a player endpoint needs the boot secret in
// the header or in the query.
func (m *mock) gate(w http.ResponseWriter, r *http.Request) bool {
	k := r.Header.Get("X-PortaPixel-Player")
	if k == "" {
		k = r.URL.Query().Get("k")
	}
	if k != m.secret {
		log.Printf("REFUSED %s: the player secret is wrong or missing", r.URL.Path)
		http.Error(w, "the player secret is wrong", http.StatusForbidden)
		return false
	}
	// The real guard lets GET and HEAD through: neither changes anything
	// (httpguard.RequireHeader).
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-PortaPixel") != "1" {
		log.Printf("REFUSED %s: the X-PortaPixel header is missing", r.URL.Path)
		http.Error(w, "the X-PortaPixel header is missing", http.StatusForbidden)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	noStore(w)
	_ = json.NewEncoder(w).Encode(v)
}

/* ---------------------------------------------------------- the scenarios */

var scenarios = []struct{ name, about string }{
	{"mixed", "images, and a video when --video is given"},
	{"onefail", "one item of three is not there"},
	{"broken", "every item fails: two are absent, one never answers"},
	{"fallback", "no content, clock in order"},
	{"fallback-code", "no content, pairing code, warnings, no clock"},
	{"lowtier", "tier low, video to video, fade through black (D14)"},
	{"urlskip", "url items that the daemon skips, one of them last"},
	{"urlgo", "a url item that the daemon takes; the player stops"},
	{"single-image", "one image, no transition"},
	{"single-video", "one video, it loops"},
}

func (m *mock) build() playerManifest {
	m.mu.Lock()
	sc, tr, ms := m.scenario, m.transition, m.ms
	m.mu.Unlock()

	tier := "high"
	var list []item

	switch sc {
	case "fallback", "fallback-code":
		return playerManifest{Fallback: true, Tier: tier}

	case "onefail":
		list = []item{card(1, 5), missing(2), raster(3, 5)}

	case "broken":
		list = []item{missing(1), slow(2), missing(3)}

	case "lowtier":
		tier = "low"
		if m.video == "" {
			log.Print("lowtier needs --video; images are used instead")
			list = []item{card(1, 4), raster(2, 4)}
		} else {
			list = []item{m.clip(true, 8), m.clip(true, 8)}
		}

	case "urlskip":
		list = []item{card(1, 4), webPage(1, 20), webPage(2, 20), raster(2, 4), webPage(3, 20)}

	case "urlgo":
		list = []item{card(1, 4), webPage(9, 30)}

	case "single-image":
		list = []item{card(7, 10)}

	case "single-video":
		if m.video == "" {
			log.Print("single-video needs --video; one image is used instead")
			list = []item{card(7, 10)}
		} else {
			list = []item{m.clip(false, 0)}
		}

	default: // mixed
		list = []item{card(1, 6), raster(2, 4), card(3, 5)}
		if m.video != "" {
			list = append(list, m.clip(false, 0))
		}
		list = append(list, card(4, 4))
	}

	for i := range list {
		list[i].Index = i
	}
	return playerManifest{
		Tier: tier,
		Playlist: &mockPlaylist{
			Name: sc, Title: "Mock " + sc,
			Transition: tr, TransitionMS: ms,
			Items: list,
		},
	}
}

func card(n, dur int) item {
	return item{Kind: "image", Name: fmt.Sprintf("card-%d.svg", n),
		Src: fmt.Sprintf("/media/gen/%d.svg", n), Duration: dur}
}

func raster(n, dur int) item {
	return item{Kind: "image", Name: fmt.Sprintf("bands-%d.png", n),
		Src: fmt.Sprintf("/media/gen/%d.png", n), Duration: dur}
}

func missing(n int) item {
	return item{Kind: "image", Name: fmt.Sprintf("gone-%d.png", n),
		Src: fmt.Sprintf("/media/missing/%d.png", n), Duration: 5}
}

func slow(n int) item {
	return item{Kind: "image", Name: fmt.Sprintf("slow-%d.png", n),
		Src: fmt.Sprintf("/media/hang/%d.png", n), Duration: 5}
}

func (m *mock) clip(mute bool, max int) item {
	return item{Kind: "video", Name: filepath.Base(m.video),
		Src: "/media/video/clip.mp4", Mute: mute, MaxDuration: max}
}

func webPage(n, dur int) item {
	u := fmt.Sprintf("https://dash.example.com/board-%d", n)
	return item{Kind: "url", Name: u, URL: u, Duration: dur, RefreshSeconds: 300}
}

/* ------------------------------------------------------------- API handlers */

func (m *mock) manifest(w http.ResponseWriter, r *http.Request) {
	if !m.gate(w, r) {
		return
	}
	mf := m.build()
	n := 0
	if mf.Playlist != nil {
		n = len(mf.Playlist.Items)
	}
	log.Printf("manifest: fallback=%v tier=%s items=%d", mf.Fallback, mf.Tier, n)
	writeJSON(w, mf)
}

func (m *mock) heartbeat(w http.ResponseWriter, r *http.Request) {
	if !m.gate(w, r) {
		return
	}
	var hb heartbeat
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.beats++
	back := hb.Frames < m.lastFrames
	m.lastFrames = hb.Frames
	count := m.beats
	m.mu.Unlock()
	if back {
		log.Print("WARNING: the frame counter went back (a page reload does this)")
	}
	line := fmt.Sprintf("beat %d: state=%s index=%d kind=%s name=%s frames=%d",
		count, hb.State, hb.Index, hb.Kind, hb.Name, hb.Frames)
	if hb.Kind == "video" {
		line += fmt.Sprintf(" position=%.1f", hb.Position)
	}
	if hb.Note != "" {
		line += "  NOTE: " + hb.Note
	}
	log.Print(line)
	writeJSON(w, map[string]bool{"ok": true})
}

func (m *mock) urlItem(w http.ResponseWriter, r *http.Request) {
	if !m.gate(w, r) {
		return
	}
	var body struct{ Index int }
	_ = json.NewDecoder(r.Body).Decode(&body)
	m.mu.Lock()
	sc := m.scenario
	m.mu.Unlock()
	if sc == "urlgo" {
		log.Printf("url-item %d: the daemon takes the browser. The player must stop now.", body.Index)
		writeJSON(w, map[string]any{})
		return
	}
	log.Printf("url-item %d: skip (D19)", body.Index)
	writeJSON(w, map[string]bool{"skip": true})
}

func (m *mock) ready(w http.ResponseWriter, r *http.Request) {
	if !m.gate(w, r) {
		return
	}
	log.Print("ready: the player is on black and waits for the restart")
	writeJSON(w, map[string]bool{"ok": true})
}

func (m *mock) status(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	sc := m.scenario
	m.mu.Unlock()

	s := manifest.Status{
		DeviceID: "px-4f2a9c17", Name: "Lobby north", MDNSName: "lobby-north.local",
		IPs:     []string{"192.168.1.42", "fe80::7a3c:1bff:fe4d:22a1"},
		Version: "0.1.0-mock", ImageVersion: "0.1.0", Arch: "amd64", Tier: "high",
		UptimeSeconds: 96543, Load: 0.31, TempC: 47.5,
		RAMTotalBytes: 2 << 30, RAMFreeBytes: 1 << 30,
		MediaTotalBytes: 28 << 30, MediaFreeBytes: 21 << 30,
		BrowserState: "running", NavigationRung: "cdp",
		DisplayConnected: true, ScreenOn: true,
		LastSyncResult: "never", ClockSynced: true, Timezone: "America/New_York",
		Warnings: []string{"Change the web password."},
	}
	if sc == "fallback-code" {
		s.PairingCode = "K7M9QX"
		s.ClockSynced = false
		s.ConfigFromShadow = true
		s.Timezone = "Europe/Amsterdam"
		s.Warnings = []string{"Change the web password.", "Change the root password.", "Set the time zone."}
	}
	writeJSON(w, s)
}

/* --------------------------------------------------------------- SSE hub */

func (m *mock) events(w http.ResponseWriter, r *http.Request) {
	if !m.gate(w, r) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flush", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": hello\n\n")
	flusher.Flush()

	ch := make(chan string, 4)
	m.mu.Lock()
	m.clients[ch] = true
	m.mu.Unlock()
	log.Print("events: a player opened the stream")
	defer func() {
		m.mu.Lock()
		delete(m.clients, ch)
		m.mu.Unlock()
		log.Print("events: the stream closed")
	}()

	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case name := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: {}\n\n", name)
			flusher.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (m *mock) send(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.clients {
		select {
		case ch <- name:
		default:
		}
	}
	return len(m.clients)
}

/* --------------------------------------------------------- the control page */

func (m *mock) set(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	m.mu.Lock()
	if v := q.Get("s"); v != "" {
		m.scenario = v
	}
	if v := q.Get("t"); v != "" {
		m.transition = v
	}
	if v := q.Get("ms"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			m.ms = n
		}
	}
	sc, tr, ms := m.scenario, m.transition, m.ms
	m.mu.Unlock()
	log.Printf("set: scenario=%s transition=%s ms=%d", sc, tr, ms)
	if q.Get("push") != "0" {
		m.send("playlist")
	}
	writeJSON(w, map[string]any{"scenario": sc, "transition": tr, "ms": ms})
}

func (m *mock) push(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("e")
	if name != "playlist" && name != "grace" && name != "reload" {
		http.Error(w, "e must be playlist, grace or reload", http.StatusBadRequest)
		return
	}
	n := m.send(name)
	log.Printf("push: %s to %d player(s)", name, n)
	writeJSON(w, map[string]any{"event": name, "players": n})
}

func (m *mock) controlPage(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	sc, tr, ms, beats := m.scenario, m.transition, m.ms, m.beats
	m.mu.Unlock()

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>mockd</title>
<link rel="stylesheet" href="/shared/pp.css">
<style>body{padding:20px;font-family:var(--pp-font)}h2{margin:18px 0 6px}
a.pp-btn{margin:2px}code{font-family:var(--pp-mono)}</style></head><body>
<h1 class="pp-h1">mockd</h1>`)
	fmt.Fprintf(&b, `<p class="pp-lead">scenario <b>%s</b>, transition <b>%s</b>, %d ms, %d heartbeats.
<a class="pp-btn pp-btn--primary" href="/player?k=%s" target="_blank">open the player</a></p>`,
		sc, tr, ms, beats, m.secret)

	b.WriteString(`<h2 class="pp-h2">Scenario</h2><div class="pp-btns">`)
	for _, s := range scenarios {
		fmt.Fprintf(&b, `<a class="pp-btn" href="/mock/set?s=%s" title="%s">%s</a>`, s.name, s.about, s.name)
	}
	b.WriteString(`</div><h2 class="pp-h2">Transition</h2><div class="pp-btns">`)
	for _, t := range []string{"crossfade", "push-left", "push-right", "push-up", "push-down", "cut"} {
		fmt.Fprintf(&b, `<a class="pp-btn" href="/mock/set?t=%s">%s</a>`, t, t)
	}
	b.WriteString(`</div><h2 class="pp-h2">Transition time</h2><div class="pp-btns">`)
	for _, n := range []int{0, 300, 700, 1500} {
		fmt.Fprintf(&b, `<a class="pp-btn" href="/mock/set?ms=%d">%d ms</a>`, n, n)
	}
	b.WriteString(`</div><h2 class="pp-h2">Events</h2><div class="pp-btns">`)
	for _, e := range []string{"playlist", "grace", "reload"} {
		fmt.Fprintf(&b, `<a class="pp-btn" href="/mock/sse?e=%s">%s</a>`, e, e)
	}
	b.WriteString(`</div><p class="pp-help">A scenario change sends a <code>playlist</code> event, so the
player picks it up at the next item boundary. Add <code>&amp;push=0</code> to make a silent change.
The player log is on the console of this program.</p>`)
	b.WriteString(`</body></html>`)

	noStore(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

/* --------------------------------------------------------------- test media */

var palette = []string{"#1b4d3e", "#27406b", "#6b2a3d", "#6b5a2a", "#2a6b5f", "#46296b"}

func (m *mock) genMedia(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".svg"), ".png")
	n, err := strconv.Atoi(base)
	if err != nil {
		http.Error(w, "the name must be a number", http.StatusNotFound)
		return
	}
	noStore(w)
	if strings.HasSuffix(name, ".png") {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bandsPNG(n))
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(cardSVG(n)))
}

// cardSVG makes a 16:9 card with a big number on it.
func cardSVG(n int) string {
	c := palette[n%len(palette)]
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="1920" height="1080" viewBox="0 0 1920 1080">`+
		`<rect width="1920" height="1080" fill="%s"/>`+
		`<rect x="24" y="24" width="1872" height="1032" fill="none" stroke="#fff" stroke-opacity="0.35" stroke-width="10"/>`+
		`<text x="960" y="640" text-anchor="middle" font-family="sans-serif" font-size="440" font-weight="700" fill="#fff">%d</text>`+
		`<text x="960" y="790" text-anchor="middle" font-family="monospace" font-size="60" fill="#fff" fill-opacity="0.75">card-%d.svg</text>`+
		`</svg>`, c, n, n)
}

// bandsPNG makes a 960x540 raster picture. It holds no text, because the
// standard library has no font. The white squares count the item, so a person
// can tell the pictures apart.
func bandsPNG(n int) []byte {
	const w, h = 960, 540
	pic := image.NewNRGBA(image.Rect(0, 0, w, h))
	r0, g0, b0 := hex(palette[(n+3)%len(palette)])
	for y := 0; y < h; y++ {
		shade := 0.6 + 0.4*float64(y)/float64(h)
		c := color.NRGBA{uint8(float64(r0) * shade), uint8(float64(g0) * shade), uint8(float64(b0) * shade), 255}
		draw.Draw(pic, image.Rect(0, y, w, y+1), &image.Uniform{c}, image.Point{}, draw.Src)
	}
	white := &image.Uniform{color.NRGBA{255, 255, 255, 255}}
	for i := 0; i <= n && i < 12; i++ {
		x := 60 + i*70
		draw.Draw(pic, image.Rect(x, 380, x+50, 460), white, image.Point{}, draw.Src)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, pic)
	return buf.Bytes()
}

func hex(s string) (int, int, int) {
	v, _ := strconv.ParseInt(strings.TrimPrefix(s, "#"), 16, 64)
	return int(v >> 16 & 255), int(v >> 8 & 255), int(v & 255)
}

func (m *mock) videoMedia(w http.ResponseWriter, r *http.Request) {
	if m.video == "" {
		http.Error(w, "no video: start mockd with --video PATH", http.StatusNotFound)
		return
	}
	noStore(w)
	http.ServeFile(w, r, m.video) // ServeFile answers a Range request
}

// hangMedia never answers. It is the load timeout test.
func (m *mock) hangMedia(w http.ResponseWriter, r *http.Request) {
	log.Printf("hang: %s waits for the player to give up", r.URL.Path)
	select {
	case <-r.Context().Done():
	case <-time.After(60 * time.Second):
	}
}

// qr draws a picture that looks like a QR code. The real daemon uses
// go-qrcode. This one only needs to fill the same space.
func (m *mock) qr(w http.ResponseWriter, r *http.Request) {
	if !m.gate(w, r) {
		return
	}
	const mods = 25
	rng := rand.New(rand.NewSource(7))
	var b strings.Builder
	size := mods + 4
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, size*8, size*8, size, size)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/>`, size, size)
	corner := func(x, y int) bool {
		return (x < 7 && y < 7) || (x >= mods-7 && y < 7) || (x < 7 && y >= mods-7)
	}
	for y := 0; y < mods; y++ {
		for x := 0; x < mods; x++ {
			if corner(x, y) || rng.Intn(2) == 0 {
				continue
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="1" height="1" fill="#000"/>`, x+2, y+2)
		}
	}
	for _, p := range [][2]int{{0, 0}, {mods - 7, 0}, {0, mods - 7}} {
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="7" height="7" fill="#000"/>`, p[0]+2, p[1]+2)
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="5" height="5" fill="#fff"/>`, p[0]+3, p[1]+3)
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="3" height="3" fill="#000"/>`, p[0]+4, p[1]+4)
	}
	b.WriteString(`</svg>`)
	noStore(w)
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(b.String()))
}
