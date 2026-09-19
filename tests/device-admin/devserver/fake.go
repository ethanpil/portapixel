package main

// The fake routes. They stand in for the parts of the API that v0.1 answers
// with "not implemented yet", so that the admin UI can be driven through the
// pairing flow, an update and an install onto a disk without waiting for the
// daemon to grow them.
//
// It also adds the root password warning to /api/status, because a Windows
// development machine has no /etc/shadow and can never raise that nag itself.
//
// Nothing here is a specification. It answers the shapes that the route table
// in docs names, and no more.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type fake struct {
	mu sync.Mutex
	// pair is "unpaired", "pending" or "paired".
	pair      string
	serverURL string
	code      string
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
		defer f.mu.Unlock()
		writeJSON(w, map[string]any{
			"status": f.pair, "server_url": f.serverURL,
			"server_name": "Ridgeline Signage", "pairing_code": f.code,
		})
	case r.URL.Path == "/api/pair" && r.Method == http.MethodPost:
		f.startPairing(w, r)
	case r.URL.Path == "/api/pair" && r.Method == http.MethodDelete:
		f.mu.Lock()
		f.pair, f.serverURL, f.code = "unpaired", "", ""
		f.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
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
		f.mu.Lock()
		f.installing = true
		f.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
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

	warnings, _ := out["warnings"].([]any)
	out["warnings"] = append(warnings, "The root password is still the default one. Change it on the Settings page.")

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

func (f *fake) startPairing(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.serverURL = body.URL
	if body.Token != "" {
		f.pair, f.code = "paired", ""
		writeJSON(w, map[string]any{"status": "paired"})
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
	writeJSON(w, map[string]any{"status": "pending", "pairing_code": f.code})
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
	event(w, "done", map[string]any{"ok": true})
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

// fakePaths says which routes the fake owns. The daemon answers everything else.
func fakePath(path string) bool {
	for _, p := range []string{"/api/status", "/api/pair", "/api/update/", "/api/disks", "/api/install-to-disk"} {
		if path == strings.TrimSuffix(p, "/") || strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}
