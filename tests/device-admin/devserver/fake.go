package main

// The fake routes. They stand in for what a Windows development machine cannot
// do for itself.
//
// /api/pair is in the daemon now (v0.3). The fake keeps its own copy, so that the
// three pairing flows can be driven with no fleet server on the network. The JSON
// is the shape of internal/device/syncer.PairState.
//
// PUT /api/config also comes here while the fake is "paired" (D48): a
// development machine runs no fleet client, so the real daemon can never
// refuse a managed field on its own. Every other state passes the call to the
// daemon.
//
// The update, disks and install-to-disk routes ARE in the daemon (v0.2). The
// fake keeps its own copies of them so that the UI can be driven on a machine
// with no second disk and with no signing key: the real routes then answer "this
// build cannot install updates" and an empty disk list, which is right and is
// also untestable.
//
// It also adds the root password warning to /api/status, because a Windows
// development machine has no /etc/shadow and can never raise that nag itself.
//
// Nothing here is a specification. It answers the shapes that the daemon
// answers, and no more.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/syncer"
)

// fakeServerName is the fleet server that the fake pretends to be, in the
// pairing state and in every managed refusal it answers (D48).
const fakeServerName = "Ridgeline Signage"

type fake struct {
	mu sync.Mutex
	// pair is "unpaired", "pending" or "paired".
	pair      string
	serverURL string
	code      string
	// syncError stands in for a sync that did not fit, so the banner can be seen.
	syncError string
	// install is the phase list that the event stream walks through.
	installing bool
}

func newFake() *fake { return &fake{pair: "unpaired"} }

// handle answers one of the fake routes. It gives false when the path is not
// one of them, and the caller then sends the request to the daemon.
func (f *fake) handle(w http.ResponseWriter, r *http.Request, daemon string) bool {
	switch {
	case r.URL.Path == "/api/status" && r.Method == http.MethodGet:
		f.status(w, r, daemon)
	case r.URL.Path == "/api/pair" && r.Method == http.MethodGet:
		f.mu.Lock()
		out := f.pairState()
		f.mu.Unlock()
		writeJSON(w, out)
	case r.URL.Path == "/api/pair" && r.Method == http.MethodPost:
		f.startPairing(w, r)
	case r.URL.Path == "/api/pair" && r.Method == http.MethodDelete:
		f.mu.Lock()
		f.pair, f.serverURL, f.code = "unpaired", "", ""
		f.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	case r.URL.Path == "/api/config" && r.Method == http.MethodPut:
		return f.putConfig(w, r, daemon)
	case r.URL.Path == "/api/update/check":
		writeJSON(w, map[string]any{
			"current": "dev", "available": "1.5.1", "source": "github",
			"notes": "Faster startup, and a stall is fixed when a web page item times out. Two days old, so try it on one screen first.",
		})
	case r.URL.Path == "/api/update/apply":
		writeJSON(w, map[string]any{"ok": true})
	case r.URL.Path == "/api/disks":
		writeJSON(w, map[string]any{"disks": []map[string]any{
			{"device": "/dev/sda", "model": "Samsung SSD 860 EVO", "size_bytes": 250059350016, "removable": false, "too_small": false},
			{"device": "/dev/sdb", "model": "Generic Flash Disk", "size_bytes": 4005527552, "removable": true, "too_small": true},
		}})
	case r.URL.Path == "/api/install-to-disk":
		f.startInstall(w, r)
	case r.URL.Path == "/api/install-to-disk/events":
		f.installEvents(w, r)
	default:
		return false
	}
	return true
}

// status takes the report of the daemon and adds what a development machine
// cannot report for itself.
func (f *fake) status(w http.ResponseWriter, r *http.Request, daemon string) {
	res, err := http.Get(daemon + "/api/status")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)

	var out map[string]any
	if json.Unmarshal(body, &out) != nil {
		w.Write(body)
		return
	}

	// status.warnings is a list of {code, message} (internal/manifest/status.go).
	warnings, _ := out["warnings"].([]any)
	out["warnings"] = append(warnings, map[string]any{
		"code":    "default-root-password",
		"message": "The root password is still the default one. Change it on the Settings page.",
	})

	// A development machine runs with --browser-cmd none, so nothing ever
	// plays. Pretend that the first playlist is on the screen and that its
	// items follow each other, so the preview and the countdown have work.
	if out["now_playing"] == nil {
		const dwell = 15
		item := (time.Now().Unix() / dwell) % 4
		out["now_playing"] = map[string]any{
			"playlist": "default", "index": item, "kind": "image",
			"item":  "the item of the moment",
			"since": time.Now().Truncate(dwell * time.Second).Format(time.RFC3339),
		}
		out["browser_state"] = "running"
	}

	f.mu.Lock()
	switch f.pair {
	case "pending":
		out["pairing_code"] = f.code
	case "paired":
		out["paired"] = true
		out["server_url"] = f.serverURL
		out["last_sync"] = time.Now().Add(-40 * time.Second).Format(time.RFC3339)
		out["last_sync_result"] = "ok"
	}
	f.mu.Unlock()

	writeJSON(w, out)
}

// pairState builds the whole PairState, the way the daemon does. GET and POST
// answer the same object, so the two must come from one place: a POST that
// answered only {status, pairing_code} hid the insecure-address warning and the
// sync error from the UI, and the UI then looked right against the fake and wrong
// against the daemon. Call it with the lock held.
func (f *fake) pairState() map[string]any {
	out := map[string]any{
		"status":     f.pair,
		"server_url": f.serverURL,
	}
	if f.pair != "unpaired" {
		out["server_name"] = fakeServerName
	}
	if f.code != "" {
		out["pairing_code"] = f.code
	}
	if f.pair == "paired" {
		out["last_sync"] = time.Now().Add(-40 * time.Second).Format(time.RFC3339)
		// The admin UI disables exactly these controls and nothing else (D48). The
		// real list comes from the syncer, so the fake cannot drift from it.
		out["managed_fields"] = syncer.ManagedFields()
	}
	if f.syncError != "" {
		out["sync_error"] = f.syncError
	}
	// The daemon raises this for an http:// address that is not loopback and not
	// on a private network (ARCHITECTURE 6b). The fake answers the same rule, so
	// that the warning banner can be driven with no server at all.
	if insecureURL(f.serverURL) {
		out["insecure"] = true
	}
	return out
}

// insecureURL is the rule of syncer.InsecureURL, in the words that a fake needs:
// http:// on a host that is not loopback and not a private address.
func insecureURL(raw string) bool {
	if !strings.HasPrefix(strings.ToLower(raw), "http://") {
		return false
	}
	host := strings.TrimPrefix(strings.TrimPrefix(raw, "http://"), "HTTP://")
	if i := strings.IndexAny(host, "/:?#"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	switch {
	case host == "localhost" || strings.HasSuffix(host, ".local"):
		return false
	case strings.HasPrefix(host, "127.") || strings.HasPrefix(host, "10."):
		return false
	case strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "169.254."):
		return false
	case host == "[::1]" || host == "::1":
		return false
	}
	for n := 16; n <= 31; n++ {
		if strings.HasPrefix(host, fmt.Sprintf("172.%d.", n)) {
			return false
		}
	}
	return true
}

func (f *fake) startPairing(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pair == "paired" {
		// A device with a pairing must be unpaired first (D25): the token of the
		// old server must never go to a new one.
		writeError(w, http.StatusConflict, "this device is paired with "+fakeServerName+"; unpair it first")
		return
	}
	f.serverURL = body.URL
	f.syncError = ""
	// The daemon refuses the mask: it is what GET /api/config shows for a saved
	// secret, and it is never a token. The UI must not send it, so the fake says
	// so loudly if it ever does.
	if body.Token == "********" {
		writeError(w, http.StatusBadRequest, "the token is the mask of a saved secret, not a token")
		return
	}
	if body.Token != "" {
		f.pair, f.code = "paired", ""
		writeJSON(w, f.pairState())
		return
	}
	f.pair, f.code = "pending", "K7M2QP"
	// A server admin approves the code after a while. Twelve seconds is long
	// enough to look at the waiting state and short enough to wait for.
	go func() {
		time.Sleep(12 * time.Second)
		f.mu.Lock()
		if f.pair == "pending" {
			f.pair, f.code = "paired", ""
		}
		f.mu.Unlock()
	}()
	writeJSON(w, f.pairState())
}

// putConfig answers PUT /api/config while the fake is "paired". A Windows
// development machine runs no fleet client, so the real daemon can never
// refuse a managed field on its own; the fake stands in for it, so the D48
// boundary can be seen with no fleet server on the network. It gives false for
// every other state, and the daemon answers the call as usual.
func (f *fake) putConfig(w http.ResponseWriter, r *http.Request, daemon string) bool {
	f.mu.Lock()
	paired := f.pair == "paired"
	f.mu.Unlock()
	if !paired {
		return false
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return true
	}
	var incoming config.Config
	if json.Unmarshal(body, &incoming) != nil {
		writeError(w, http.StatusBadRequest, "the body is not the configuration shape")
		return true
	}

	// GET /api/config needs the session that the person is signed in with, or
	// the daemon answers 401 and every field of cur reads as zero, which then
	// looks like every field changed.
	req, err := http.NewRequest(http.MethodGet, daemon+"/api/config", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return true
	}
	if cookie := r.Header.Get("Cookie"); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return true
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
		return true
	}
	var view struct {
		Config config.Config `json:"config"`
	}
	json.NewDecoder(res.Body).Decode(&view)
	cur := view.Config

	changed := managedFieldChanged(cur, incoming)
	if changed == "" {
		// Nothing that the fleet server owns is different. Reading the body above
		// drained it, so the daemon needs it back before it can save whatever else
		// the person changed.
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		return false
	}

	fields := make([]map[string]string, 0, len(syncer.ManagedFields()))
	for _, name := range syncer.ManagedFields() {
		fields = append(fields, map[string]string{"field": name, "message": "the fleet server manages this"})
	}
	writeJSONStatus(w, http.StatusForbidden, map[string]any{
		"error":  "managed by " + fakeServerName,
		"fields": fields,
	})
	return true
}

// managedFieldChanged gives the first managed field of D48 that differs
// between the running configuration and the one in the request body, or "".
func managedFieldChanged(cur, incoming config.Config) string {
	switch {
	case cur.Playback.DefaultPlaylist != incoming.Playback.DefaultPlaylist:
		return "playback.default_playlist"
	case !scheduleEqual(cur.Schedule, incoming.Schedule):
		return "schedule"
	case cur.Display.OnTime != incoming.Display.OnTime:
		return "display.on_time"
	case cur.Display.OffTime != incoming.Display.OffTime:
		return "display.off_time"
	case !stringsEqual(cur.Display.PowerDays, incoming.Display.PowerDays):
		return "display.power_days"
	}
	return ""
}

func scheduleEqual(a, b []config.Rule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Playlist != b[i].Playlist || a[i].Start != b[i].Start || a[i].End != b[i].End {
			return false
		}
		if !stringsEqual(a[i].Days, b[i].Days) {
			return false
		}
	}
	return true
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// startInstall checks the confirmation the same way that the daemon does: the
// device path, character for character (D54).
func (f *fake) startInstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Device  string `json:"device"`
		Confirm string `json:"confirm"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.Confirm != body.Device || body.Device == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{
			"error": "type the name of the disk, " + body.Device + ", to confirm",
		})
		return
	}
	f.mu.Lock()
	f.installing = true
	f.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

// installEvents is the progress stream of an install onto a disk.
func (f *fake) installEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming here", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	phases := []struct {
		name    string
		message string
	}{
		{"reading the disk", "checking that /dev/sda is not the disk we boot from"},
		{"copying the boot partition", "16 MB"},
		{"copying the system partition", "1.2 GB"},
		{"copying the media partition", "38 MB"},
		{"growing the media partition", "to the end of the disk"},
		{"writing the boot loader", ""},
	}
	for i, phase := range phases {
		percent := (i + 1) * 100 / len(phases)
		event(w, "progress", map[string]any{"phase": phase.name, "percent": percent, "message": phase.message})
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1500 * time.Millisecond):
		}
	}
	event(w, "done", map[string]any{
		"ok": true,
		"instruction": "Power the machine off. Take the USB stick out. " +
			"Start the machine again and let it boot from the disk.",
	})
	flusher.Flush()

	f.mu.Lock()
	f.installing = false
	f.mu.Unlock()
	<-r.Context().Done()
}

func event(w io.Writer, name string, data any) {
	body, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, body)
}

func writeJSON(w http.ResponseWriter, body any) {
	data, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

// writeError answers the {error} shape of the daemon.
func writeError(w http.ResponseWriter, code int, message string) {
	data, _ := json.Marshal(map[string]string{"error": message})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(data)
}

// writeJSONStatus is writeJSON with a status other than 200.
func writeJSONStatus(w http.ResponseWriter, code int, body any) {
	data, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(data)
}

// fakePaths says which routes the fake owns. The daemon answers everything else.
func fakePath(path string) bool {
	for _, p := range []string{"/api/status", "/api/pair", "/api/config", "/api/update/", "/api/disks", "/api/install-to-disk"} {
		if path == strings.TrimSuffix(p, "/") || strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}
