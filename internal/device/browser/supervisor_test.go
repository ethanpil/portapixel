package browser

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/opslog"
)

// harness runs a Supervisor with a stub browser and a fast tick.
type harness struct {
	sup  *Supervisor
	stub *stub
	log  *opslog.Log
	done chan struct{}

	mu        sync.Mutex
	reachable map[string]bool
	reboots   []string
	graces    int
	active    Active
	nightly   string
	covered   bool
	drm       string
}

func newHarness(t *testing.T, opts func(*Options, *harness)) *harness {
	t.Helper()
	h := &harness{
		stub:      newStub(t),
		log:       testLog(t),
		done:      make(chan struct{}),
		reachable: map[string]bool{},
	}
	o := Options{
		Command:    CommandConfig{Override: h.stub.override},
		Log:        h.log,
		Tick:       5 * time.Millisecond,
		CDPTimeout: 100 * time.Millisecond,
		PlayerURL:  func(resume int) string { return playerURL(resume) },
		Active:     func() Active { h.mu.Lock(); defer h.mu.Unlock(); return h.active },
		Reachable: func(url string) bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			ok, seen := h.reachable[url]
			return !seen || ok
		},
		Reboot: func(reason string) {
			h.mu.Lock()
			h.reboots = append(h.reboots, reason)
			h.mu.Unlock()
		},
		Grace: func() {
			h.mu.Lock()
			h.graces++
			h.mu.Unlock()
		},
		NightlyRestart:  func() string { h.mu.Lock(); defer h.mu.Unlock(); return h.nightly },
		ScreenOffCovers: func(time.Time) bool { h.mu.Lock(); defer h.mu.Unlock(); return h.covered },
	}
	if opts != nil {
		opts(&o, h)
	}
	h.sup = New(o)
	go h.sup.Run(h.done)
	t.Cleanup(func() { close(h.done); time.Sleep(20 * time.Millisecond) })
	return h
}

func playerURL(resume int) string {
	if resume < 0 {
		return "http://127.0.0.1:8099/player?k=secret"
	}
	return "http://127.0.0.1:8099/player?k=secret&resume=" + itoa(resume)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func (h *harness) setActive(a Active) {
	h.mu.Lock()
	h.active = a
	h.mu.Unlock()
	h.sup.PlaylistChanged()
}

func (h *harness) setReachable(url string, ok bool) {
	h.mu.Lock()
	h.reachable[url] = ok
	h.mu.Unlock()
}

func (h *harness) events() string {
	var b strings.Builder
	for _, e := range h.log.Tail(200) {
		b.WriteString(e.Event + " " + e.Details + "\n")
	}
	return b.String()
}

func (h *harness) sawEvent(name string) bool {
	for _, e := range h.log.Tail(200) {
		if e.Event == name {
			return true
		}
	}
	return false
}

func TestSupervisorStartsOnThePlayer(t *testing.T) {
	h := newHarness(t, nil)
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })

	st := h.sup.State()
	if st.Rung != "relaunch" {
		t.Fatalf("rung = %q, want relaunch: there is no DevTools port in this test", st.Rung)
	}
	if !h.sup.Started() {
		t.Fatal("Started is false while the browser runs")
	}
	waitFor(t, "the player URL", func() bool {
		starts := h.stub.starts(t)
		return len(starts) == 1 && strings.Contains(starts[0], "/player?k=secret")
	})
	if !h.sawEvent("browser.start") {
		t.Errorf("no browser.start line:\n%s", h.events())
	}
}

// The supervisor must choose rung 1 when the DevTools port answers, and it must
// then navigate without a new browser process.
func TestSupervisorPicksTheCDPRung(t *testing.T) {
	stubBrowser := newCDPStub(t)
	h := newHarness(t, func(o *Options, h *harness) {
		o.Command = CommandConfig{Override: h.stub.override, DebugURL: stubBrowser.server.URL}
		o.CDPTimeout = 5 * time.Second
	})
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })
	if got := h.sup.State().Rung; got != "cdp" {
		t.Fatalf("rung = %q, want cdp", got)
	}

	if skip := h.sup.URLItem("https://dash.example.com/board", 0, 0, 0); skip {
		t.Fatal("a reachable URL was skipped")
	}
	waitFor(t, "the navigation", func() bool {
		return stubBrowser.current() == "https://dash.example.com/board"
	})
	waitFor(t, "the browser to record its start", func() bool { return len(h.stub.starts(t)) >= 1 })
	if got := h.stub.starts(t); len(got) != 1 {
		t.Fatalf("the CDP rung started %d browser processes, want one", len(got))
	}
}

func TestSupervisorRestartsAfterACrash(t *testing.T) {
	h := newHarness(t, nil)
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })

	// Kill the stub. The supervisor must start it again and count the restart.
	h.sup.proc.stop()
	h.sup.expectExit = false // the loop must read this as a crash

	waitFor(t, "the second start", func() bool { return len(h.stub.starts(t)) >= 2 })
	waitFor(t, "the restart count", func() bool { return h.sup.State().Restarts >= 1 })
}

func TestSupervisorURLItem(t *testing.T) {
	h := newHarness(t, nil)
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })

	if skip := h.sup.URLItem("https://dash.example.com/board", 1, 0, 3); skip {
		t.Fatal("a reachable URL was skipped")
	}
	waitFor(t, "the navigation to the URL item", func() bool {
		starts := h.stub.starts(t)
		return len(starts) >= 2 && starts[1] == "https://dash.example.com/board"
	})
	// After the dwell time the browser goes back to the player at the next item.
	waitFor(t, "the return to the player", func() bool {
		starts := h.stub.starts(t)
		return len(starts) >= 3 && strings.Contains(starts[2], "resume=3")
	})
	if !h.sawEvent("browser.url.start") || !h.sawEvent("browser.url.end") {
		t.Errorf("the ops log has no URL window lines:\n%s", h.events())
	}
}

func TestSupervisorURLItemSkipsAnUnreachablePage(t *testing.T) {
	h := newHarness(t, nil)
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })

	h.setReachable("https://gone.example.com/", false)
	if skip := h.sup.URLItem("https://gone.example.com/", 10, 0, 2); !skip {
		t.Fatal("an unreachable URL was not skipped")
	}
	if !h.sawEvent("browser.url.skip") {
		t.Errorf("no browser.url.skip line:\n%s", h.events())
	}
	// The browser stays on the player.
	if got := h.stub.starts(t); len(got) != 1 {
		t.Fatalf("starts = %v, want one", got)
	}
}

func TestSupervisorKioskMode(t *testing.T) {
	h := newHarness(t, nil)
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })

	h.setActive(Active{Playlist: "board", KioskURL: "https://dash.example.com/board"})
	waitFor(t, "the kiosk page", func() bool {
		starts := h.stub.starts(t)
		return len(starts) >= 2 && starts[len(starts)-1] == "https://dash.example.com/board"
	})
	if !h.sawEvent("browser.kiosk.start") {
		t.Errorf("no browser.kiosk.start line:\n%s", h.events())
	}

	// A playlist that is not a single URL ends the kiosk mode.
	h.setActive(Active{Playlist: "default"})
	waitFor(t, "the return to the player", func() bool {
		starts := h.stub.starts(t)
		return strings.Contains(starts[len(starts)-1], "/player?k=secret")
	})
	if !h.sawEvent("browser.kiosk.end") {
		t.Errorf("no browser.kiosk.end line:\n%s", h.events())
	}
}

func TestSupervisorKioskFallbackWhenThePageIsDown(t *testing.T) {
	h := newHarness(t, nil)
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })

	h.setReachable("https://dash.example.com/board", false)
	h.setActive(Active{Playlist: "board", KioskURL: "https://dash.example.com/board"})
	waitFor(t, "the fallback screen", func() bool { return h.sawEvent("browser.kiosk.fallback") })

	// The browser shows the SPA, not the dead page.
	starts := h.stub.starts(t)
	if last := starts[len(starts)-1]; !strings.Contains(last, "/player") {
		t.Fatalf("the browser is on %q, want the player", last)
	}
}

func TestSupervisorSuspendAndResume(t *testing.T) {
	h := newHarness(t, nil)
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })

	h.sup.Suspend()
	waitFor(t, "the browser to stop", func() bool {
		st := h.sup.State()
		return st.Browser == StateStopped && st.Suspended
	})
	before := len(h.stub.starts(t))
	// The supervisor must not start the browser again while the screen is off.
	time.Sleep(50 * time.Millisecond)
	if got := len(h.stub.starts(t)); got != before {
		t.Fatalf("the browser started again during the suspend: %d starts", got)
	}

	h.sup.Resume()
	waitFor(t, "the browser to run again", func() bool { return h.sup.State().Browser == StateRunning })
	waitFor(t, "the new browser process", func() bool { return len(h.stub.starts(t)) > before })
}

func TestSupervisorWaitsForADisplayAndNeverEscalates(t *testing.T) {
	h := newHarness(t, func(o *Options, h *harness) {
		drm := t.TempDir()
		writeStatus(t, drm, "card0-HDMI-A-1", "disconnected")
		o.DRMRoot = drm
		h.drm = drm
	})

	waitFor(t, "the wait state", func() bool { return h.sup.State().Browser == StateWaiting })
	if got := h.stub.starts(t); len(got) != 0 {
		t.Fatalf("the browser started with no display: %v", got)
	}
	if !h.sup.Started() {
		t.Fatal("a device that waits for a display is not healthy; it must be")
	}
	// The wait must never count a restart, however long it lasts.
	time.Sleep(100 * time.Millisecond)
	if st := h.sup.State(); st.Restarts != 0 {
		t.Fatalf("the wait counted %d restarts", st.Restarts)
	}
	if len(h.reboots) != 0 {
		t.Fatalf("the wait asked for a reboot: %v", h.reboots)
	}
	// One log line, not one for each try.
	lines := 0
	for _, e := range h.log.Tail(200) {
		if e.Event == "browser.display.wait" {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("browser.display.wait appears %d times", lines)
	}

	// Plug a display in.
	writeStatus(t, h.drm, "card0-HDMI-A-1", "connected")
	waitFor(t, "the browser to start", func() bool { return h.sup.State().Browser == StateRunning })
	if !h.sawEvent("browser.display.found") {
		t.Errorf("no browser.display.found line:\n%s", h.events())
	}
}

func TestSupervisorHeartbeatFeedsNowPlaying(t *testing.T) {
	h := newHarness(t, nil)
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })

	h.sup.Heartbeat(Heartbeat{Playlist: "default", Index: 1, Name: "promo.mp4", Kind: "video", State: "playing", Frames: 100})
	st := h.sup.State()
	if st.NowPlaying == nil || st.NowPlaying.Item != "promo.mp4" || st.NowPlaying.Index != 1 {
		t.Fatalf("NowPlaying = %+v", st.NowPlaying)
	}
	since := st.NowPlaying.Since
	// The same item must not move the start time.
	h.sup.Heartbeat(Heartbeat{Playlist: "default", Index: 1, Name: "promo.mp4", Kind: "video", Frames: 200})
	if got := h.sup.State().NowPlaying.Since; !got.Equal(since) {
		t.Errorf("the start time of the item moved")
	}
}

func TestSupervisorFrameStallRestartsTheBrowser(t *testing.T) {
	h := newHarness(t, nil)
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })
	before := len(h.stub.starts(t))

	for i := 0; i < frameStalls+1; i++ {
		h.sup.Heartbeat(Heartbeat{Playlist: "default", Frames: 500})
	}
	waitFor(t, "the restart", func() bool { return len(h.stub.starts(t)) > before })
	if !h.sawEvent("browser.restart") {
		t.Errorf("no browser.restart line:\n%s", h.events())
	}
}

func TestSupervisorNightlyRestart(t *testing.T) {
	now := time.Date(2026, 9, 18, 3, 30, 0, 0, time.UTC)
	h := newHarness(t, func(o *Options, h *harness) {
		o.Now = func() time.Time { return now }
		h.nightly = "03:30"
	})
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })
	before := len(h.stub.starts(t))

	waitFor(t, "the grace request", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.graces == 1
	})
	// The player answers, so the restart happens at once.
	h.sup.Ready()
	waitFor(t, "the restart", func() bool { return len(h.stub.starts(t)) > before })
	if !h.sawEvent("browser.nightly.grace") {
		t.Errorf("no browser.nightly.grace line:\n%s", h.events())
	}

	// It must run once a day only.
	time.Sleep(50 * time.Millisecond)
	h.mu.Lock()
	graces := h.graces
	h.mu.Unlock()
	if graces != 1 {
		t.Fatalf("the nightly restart ran %d times in one minute", graces)
	}
}

func TestSupervisorNightlyRestartSkippedByTheScreenSchedule(t *testing.T) {
	now := time.Date(2026, 9, 18, 3, 30, 0, 0, time.UTC)
	h := newHarness(t, func(o *Options, h *harness) {
		o.Now = func() time.Time { return now }
		h.nightly = "03:30"
		h.covered = true
	})
	waitFor(t, "the skip line", func() bool { return h.sawEvent("browser.nightly.skip") })

	h.mu.Lock()
	graces := h.graces
	h.mu.Unlock()
	if graces != 0 {
		t.Fatalf("the player was asked for a grace moment %d times", graces)
	}
}

func TestSupervisorDisabledBrowser(t *testing.T) {
	h := newHarness(t, func(o *Options, h *harness) {
		o.Command = CommandConfig{Override: DisableCommand}
	})
	time.Sleep(50 * time.Millisecond)
	if st := h.sup.State(); st.Browser != StateDisabled {
		t.Fatalf("state = %q, want %q", st.Browser, StateDisabled)
	}
	if !h.sup.Started() {
		t.Fatal("a device with the browser switched off must still be healthy")
	}
	if got := h.stub.starts(t); len(got) != 0 {
		t.Fatalf("a switched off browser started: %v", got)
	}
}

func TestSupervisorRebootsAfterFourRestarts(t *testing.T) {
	h := newHarness(t, nil)
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })

	for i := 0; i < restartsBeforeReboot; i++ {
		h.sup.Restart("a test restart")
		waitFor(t, "the restart to finish", func() bool { return h.sup.State().Browser == StateRunning })
	}
	waitFor(t, "the reboot", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.reboots) == 1
	})
	if !h.sawEvent("browser.reboot") {
		t.Errorf("no browser.reboot line:\n%s", h.events())
	}
}

func TestSupervisorLogsPlayerNotes(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, func(o *Options, h *harness) {
		o.Now = func() time.Time { return now }
	})
	waitFor(t, "the browser to run", func() bool { return h.sup.State().Browser == StateRunning })

	count := func() int {
		n := 0
		for _, e := range h.log.Tail(200) {
			if e.Event == "player" {
				n++
			}
		}
		return n
	}

	const note = "item 3 (promo.mp4) skipped: load timeout"
	for i := 0; i < 5; i++ {
		h.sup.Heartbeat(Heartbeat{Playlist: "default", Frames: int64(100 + i), Note: note})
	}
	if got := count(); got != 1 {
		t.Fatalf("the same note went in the ops log %d times, want once", got)
	}

	// Another note is another line.
	h.sup.Heartbeat(Heartbeat{Playlist: "default", Frames: 200, Note: "the manifest did not load"})
	if got := count(); got != 2 {
		t.Fatalf("player lines = %d, want two", got)
	}
	// A heartbeat with no note writes nothing.
	h.sup.Heartbeat(Heartbeat{Playlist: "default", Frames: 201})
	if got := count(); got != 2 {
		t.Fatalf("player lines = %d after a heartbeat with no note", got)
	}
	// The first note again, inside the hour: still one line.
	h.sup.Heartbeat(Heartbeat{Playlist: "default", Frames: 202, Note: note})
	if got := count(); got != 3 {
		// A note that is different from the LAST note is new, so this is line 3.
		t.Fatalf("player lines = %d, want three", got)
	}
}

func TestSupervisorURLItemSkippedWithNoBrowser(t *testing.T) {
	h := newHarness(t, func(o *Options, h *harness) {
		o.Command = CommandConfig{Override: DisableCommand}
	})
	time.Sleep(30 * time.Millisecond)
	if skip := h.sup.URLItem("https://dash.example.com/board", 10, 0, 1); !skip {
		t.Fatal("a URL item was taken while the browser is switched off; the player would wait for ever")
	}
}
