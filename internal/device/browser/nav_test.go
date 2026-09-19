package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/ethanpil/portapixel/internal/opslog"
)

func testLog(t *testing.T) *opslog.Log {
	t.Helper()
	return opslog.New(filepath.Join(t.TempDir(), "ops.log"))
}

// ---------------------------------------------------------------- rung 2: relaunch

func TestRelaunchRung(t *testing.T) {
	s := newStub(t)
	proc := newLauncher(CommandConfig{Override: s.override}, testLog(t))
	nav := newRelaunch(proc)
	t.Cleanup(func() { nav.Stop() })

	ctx := context.Background()
	if err := nav.Start(ctx, "http://first/"); err != nil {
		t.Fatal(err)
	}
	if nav.Name() != "relaunch" {
		t.Errorf("Name = %q", nav.Name())
	}
	waitFor(t, "the first start", func() bool { return len(s.starts(t)) == 1 })
	if !nav.Alive() {
		t.Fatal("the stub browser is not alive")
	}
	if url, err := nav.CurrentURL(ctx); err != nil || url != "http://first/" {
		t.Fatalf("CurrentURL = %q, %v", url, err)
	}

	// Start with the same URL must not start a second process.
	if err := nav.Start(ctx, "http://first/"); err != nil {
		t.Fatal(err)
	}
	if got := s.starts(t); len(got) != 1 {
		t.Fatalf("starts = %v, want one", got)
	}

	// A navigation is a new process with the new URL.
	if err := nav.Navigate(ctx, "http://second/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second start", func() bool { return len(s.starts(t)) == 2 })
	if got := s.starts(t); got[1] != "http://second/" {
		t.Fatalf("starts = %v", got)
	}
	if url, _ := nav.CurrentURL(ctx); url != "http://second/" {
		t.Fatalf("CurrentURL = %q", url)
	}

	// A reload is the same URL again.
	if err := nav.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the reload", func() bool { return len(s.starts(t)) == 3 })
	if got := s.starts(t); got[2] != "http://second/" {
		t.Fatalf("starts = %v", got)
	}

	if err := nav.Stop(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the browser to end", func() bool { return !nav.Alive() })
	if _, err := nav.CurrentURL(ctx); err == nil {
		t.Fatal("CurrentURL answered after the browser ended")
	}
}

func TestRelaunchReloadWithNoPage(t *testing.T) {
	proc := newLauncher(CommandConfig{Override: newStub(t).override}, testLog(t))
	nav := newRelaunch(proc)
	if err := nav.Reload(context.Background()); err == nil {
		t.Fatal("Reload answered with no page")
	}
}

// ------------------------------------------------------------------- rung 1: CDP

// cdpStub is a browser that speaks enough of the DevTools Protocol.
type cdpStub struct {
	server *httptest.Server

	mu       sync.Mutex
	url      string
	reloads  int
	sockets  int
	noTarget bool
	// drop closes the socket after the next answer, which is what a renderer
	// crash does.
	drop bool
}

func newCDPStub(t *testing.T) *cdpStub {
	t.Helper()
	s := &cdpStub{url: "about:blank"}

	mux := http.NewServeMux()
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		empty := s.noTarget
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if empty {
			w.Write([]byte(`[]`))
			return
		}
		ws := "ws" + strings.TrimPrefix(s.server.URL, "http") + "/devtools/page/1"
		json.NewEncoder(w).Encode([]map[string]string{
			{"type": "service_worker", "url": "sw.js", "webSocketDebuggerUrl": "ws://ignore/me"},
			{"type": "page", "url": s.current(), "webSocketDebuggerUrl": ws},
		})
	})
	mux.Handle("/devtools/page/1", websocket.Handler(s.serve))

	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *cdpStub) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

func (s *cdpStub) serve(conn *websocket.Conn) {
	s.mu.Lock()
	s.sockets++
	s.mu.Unlock()
	defer conn.Close()

	for {
		var req struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := websocket.JSON.Receive(conn, &req); err != nil {
			return
		}
		reply := map[string]any{"id": req.ID, "result": map[string]any{}}
		s.mu.Lock()
		switch req.Method {
		case "Page.navigate":
			if url, ok := req.Params["url"].(string); ok {
				s.url = url
			}
		case "Page.reload":
			s.reloads++
		case "Runtime.evaluate":
			reply["result"] = map[string]any{"result": map[string]any{"type": "string", "value": s.url}}
		default:
			reply = map[string]any{"id": req.ID, "error": map[string]any{"message": "unknown method"}}
		}
		drop := s.drop
		s.drop = false
		s.mu.Unlock()

		// Send an event first: the client must skip a message with no id.
		websocket.JSON.Send(conn, map[string]any{"method": "Page.frameNavigated", "params": map[string]any{}})
		if err := websocket.JSON.Send(conn, reply); err != nil {
			return
		}
		if drop {
			return
		}
	}
}

func TestCDPRung(t *testing.T) {
	browser := newCDPStub(t)
	s := newStub(t)
	proc := newLauncher(CommandConfig{Override: s.override}, testLog(t))
	nav := newCDP(proc, browser.server.URL, 2*time.Second)
	t.Cleanup(func() { nav.Stop() })

	ctx := context.Background()
	if err := nav.Start(ctx, "http://127.0.0.1/player"); err != nil {
		t.Fatal(err)
	}
	if nav.Name() != "cdp" {
		t.Errorf("Name = %q", nav.Name())
	}
	if !nav.Alive() {
		t.Fatal("the browser process is not alive")
	}

	if err := nav.Navigate(ctx, "https://dash.example.com/board"); err != nil {
		t.Fatal(err)
	}
	if got := browser.current(); got != "https://dash.example.com/board" {
		t.Fatalf("the stub is on %q", got)
	}
	// One process only: the CDP rung navigates without a restart.
	waitFor(t, "the browser to record its start", func() bool { return len(s.starts(t)) == 1 })
	if got := s.starts(t); len(got) != 1 {
		t.Fatalf("starts = %v, want one", got)
	}

	if url, err := nav.CurrentURL(ctx); err != nil || url != "https://dash.example.com/board" {
		t.Fatalf("CurrentURL = %q, %v", url, err)
	}
	if err := nav.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	browser.mu.Lock()
	reloads := browser.reloads
	browser.mu.Unlock()
	if reloads != 1 {
		t.Fatalf("reloads = %d", reloads)
	}
}

func TestCDPReconnectsAfterALostSocket(t *testing.T) {
	browser := newCDPStub(t)
	proc := newLauncher(CommandConfig{Override: newStub(t).override}, testLog(t))
	nav := newCDP(proc, browser.server.URL, 2*time.Second)
	t.Cleanup(func() { nav.Stop() })

	ctx := context.Background()
	if err := nav.Start(ctx, "http://127.0.0.1/player"); err != nil {
		t.Fatal(err)
	}
	// The next answer closes the socket.
	browser.mu.Lock()
	browser.drop = true
	browser.mu.Unlock()
	if err := nav.Navigate(ctx, "http://one/"); err != nil {
		t.Fatal(err)
	}
	// This call must open a new socket by itself.
	if err := nav.Navigate(ctx, "http://two/"); err != nil {
		t.Fatalf("the navigator did not reconnect: %v", err)
	}
	if got := browser.current(); got != "http://two/" {
		t.Fatalf("the stub is on %q", got)
	}
	browser.mu.Lock()
	sockets := browser.sockets
	browser.mu.Unlock()
	if sockets < 2 {
		t.Fatalf("sockets = %d, want two or more", sockets)
	}
}

func TestCDPStartFailsWithNoPageTarget(t *testing.T) {
	browser := newCDPStub(t)
	browser.mu.Lock()
	browser.noTarget = true
	browser.mu.Unlock()

	proc := newLauncher(CommandConfig{Override: newStub(t).override}, testLog(t))
	nav := newCDP(proc, browser.server.URL, 2*time.Second)
	t.Cleanup(func() { nav.Stop() })

	// A short deadline: the real timeout is 45 seconds, which no test may take.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := nav.Start(ctx, "http://127.0.0.1/player"); err == nil {
		t.Fatal("Start passed with no page target")
	}
	// The process runs: the supervisor uses it for the relaunch rung.
	if !nav.Alive() {
		t.Fatal("the failed CDP start left no browser process")
	}
}

func TestCDPCallFailsWhenTheBrowserIsGone(t *testing.T) {
	browser := newCDPStub(t)
	proc := newLauncher(CommandConfig{Override: newStub(t).override}, testLog(t))
	nav := newCDP(proc, browser.server.URL, 2*time.Second)
	if err := nav.Start(context.Background(), "http://x/"); err != nil {
		t.Fatal(err)
	}
	browser.server.Close()
	if err := nav.Navigate(context.Background(), "http://y/"); err == nil {
		t.Fatal("Navigate passed with no browser")
	}
	nav.Stop()
}

// ------------------------------------------------------------------- probe

func TestReachable(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ok.Close()
	// A server that refuses HEAD, which many do.
	noHead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Write([]byte("hello"))
	}))
	defer noHead.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	if !Reachable(ok.URL) {
		t.Error("a good page is not reachable")
	}
	if !Reachable(noHead.URL) {
		t.Error("a page that refuses HEAD is not reachable")
	}
	if Reachable(broken.URL) {
		t.Error("a page that answers 500 is reachable")
	}
	if Reachable("http://127.0.0.1:1/") {
		t.Error("a closed port is reachable")
	}
	if Reachable("not a url") {
		t.Error("a bad URL is reachable")
	}
}
