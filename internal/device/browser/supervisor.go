package browser

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
)

// The times of the supervisor. Together with the block in watchdog.go this is
// every threshold that the display stack has.
const (
	// defaultTick is how often the supervisor looks at the world.
	defaultTick = time.Second
	// livenessPoll is the time between two CurrentURL calls during a URL window.
	// Our JavaScript is not on the page then, so this is the only liveness test
	// (plan 3.2).
	livenessPoll = 10 * time.Second
	// graceTimeout is how long the player may take to reach a good moment for the
	// nightly restart. After it, the restart happens in the middle of an item
	// (plan 3.3).
	graceTimeout = 60 * time.Second
	// displayWaitMin and displayWaitMax are the backoff of the wait for a display
	// (D44). The wait never counts toward the restart ladder.
	displayWaitMin = 5 * time.Second
	displayWaitMax = 60 * time.Second
	// kioskRetryMin and kioskRetryMax are the backoff of the retry of a single
	// URL kiosk page that does not answer (D42).
	kioskRetryMin = 10 * time.Second
	kioskRetryMax = 5 * time.Minute
	// navTimeout is the time that one navigation call may take.
	navTimeout = 20 * time.Second
	// noteRepeat is how often the same note from the player may go in the ops
	// log. A broken item in a loop sends its note every 5 seconds, and a log that
	// holds one fault a thousand times holds nothing else (ARCHITECTURE 7a).
	noteRepeat = time.Hour
)

// The values of State.Browser. They are the words that /api/status uses.
const (
	StateStopped  = "stopped"
	StateStarting = "starting"
	StateRunning  = "running"
	StateWaiting  = "waiting-for-display"
	StateDisabled = "disabled"
)

// Active is the playlist that must play now.
type Active struct {
	// Playlist is the name of the playlist, for the ops log.
	Playlist string
	// KioskURL is set when the playlist is exactly one URL item. The daemon then
	// parks the browser on that page and the SPA does not run (D42).
	KioskURL string
	// RefreshSeconds loads the kiosk page again every N seconds.
	RefreshSeconds int
}

// DisplaySettings are the two display keys that the browser applies.
type DisplaySettings struct {
	Rotation  int
	VideoMode string
}

// Heartbeat is the body of POST /api/player/heartbeat (ARCHITECTURE 7a).
type Heartbeat struct {
	Playlist string  `json:"playlist"`
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`
	State    string  `json:"state"` // playing | fallback | handoff
	Frames   int64   `json:"frames"`
	Position float64 `json:"position"`
	// Note is one line about something that needs attention, for example an item
	// that the player skipped. A new note goes in the ops log.
	Note string `json:"note,omitempty"`
}

// State is what the supervisor reports to /api/status.
type State struct {
	Browser          string
	Rung             string
	DisplayConnected bool
	Suspended        bool
	Restarts         int
	NowPlaying       *manifest.NowPlaying
	// LastError is the reason that the browser is not running, when it is not.
	LastError string
}

// Options are the parameters of a Supervisor. Every one of them that touches the
// world outside this package is a function, so a test can give a fake.
type Options struct {
	Command CommandConfig
	Log     *opslog.Log
	// Now gives the local time of the device: the nightly restart time is a
	// local time.
	Now func() time.Time
	// PlayerURL gives the address of the player SPA. resume is the item to start
	// at, or a value below zero to start at the first item.
	PlayerURL func(resume int) string
	// Active gives the playlist that must play now.
	Active func() Active
	// Display gives the rotation and the output mode.
	Display func() DisplaySettings
	// Reachable probes a URL item. A nil function uses the HTTP probe.
	Reachable func(url string) bool
	// Reboot reboots the device. It is rung 2 of the watchdog ladder.
	Reboot func(reason string)
	// Grace asks the player for a good moment to restart. The player answers with
	// POST /api/player/ready, which the daemon passes to Ready.
	Grace func()
	// NightlyRestart gives the time of the daily restart as "HH:MM", or "".
	NightlyRestart func() string
	// ScreenOffCovers reports that the screen schedule has the screen off at t.
	// The nightly restart is then pointless and is skipped (plan 3.3).
	ScreenOffCovers func(t time.Time) bool
	// DRMRoot is where the display connectors are. "" uses /sys/class/drm.
	DRMRoot string
	// CDPTimeout is how long the first launch waits for the DevTools port before
	// it falls to rung 2. 0 uses 45 seconds, which a cold Chromium on a Pi Zero
	// needs. A test sets a short value.
	CDPTimeout time.Duration
	// Tick is the period of the loop. 0 uses one second.
	Tick time.Duration
}

// command is one message to the loop. The loop owns the browser, so everything
// that changes it comes through this channel and nothing needs a lock.
type command struct {
	kind   int
	url    string
	dwell  time.Duration
	period time.Duration
	resume int
	reason string
}

const (
	cmdRestart = iota
	cmdSuspend
	cmdResume
	cmdWindow
	cmdPlaylist
	cmdReady
	cmdDisplay
)

// urlWindow is an open URL item (ARCHITECTURE 7a).
type urlWindow struct {
	url    string
	dwell  time.Duration // zero means "stay on the page"
	period time.Duration // reload interval, zero means "do not reload"
	resume int           // the item that the player continues at

	started  time.Time
	reloaded time.Time
	polled   time.Time
}

// kioskPage is the single URL kiosk mode (D42).
type kioskPage struct {
	url    string
	period time.Duration

	reloaded time.Time
	polled   time.Time
	// fallback is true while the page does not answer and the SPA shows the
	// fallback screen. retryAt and backoff drive the retry.
	fallback bool
	retryAt  time.Time
	backoff  time.Duration
}

// Supervisor owns the browser. One goroutine, Run, changes it. Every exported
// method either reads a value under a lock or sends a message to that goroutine.
type Supervisor struct {
	opt  Options
	proc *launcher
	cmds chan command

	// Fields that the loop owns.
	nav        Navigator
	rungPicked bool
	window     *urlWindow
	kiosk      *kioskPage
	target     string
	expectExit bool
	everUp     bool
	waitDelay  time.Duration
	waitUntil  time.Time
	waitLogged bool
	graceUntil time.Time
	ready      bool
	lastDay    string

	// Fields under the lock. Run writes them, and the HTTP handlers read them.
	mu         sync.Mutex
	wd         watchdog
	state      string
	rung       string
	displayOK  bool
	suspended  bool
	restarts   int
	lastErr    string
	lastBeat   *Heartbeat
	beatSince  time.Time
	lastNote   string
	lastNoteAt time.Time
}

// New makes a Supervisor.
func New(opt Options) *Supervisor {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Reachable == nil {
		opt.Reachable = Reachable
	}
	if opt.Tick <= 0 {
		opt.Tick = defaultTick
	}
	if opt.Active == nil {
		opt.Active = func() Active { return Active{} }
	}
	if opt.Display == nil {
		opt.Display = func() DisplaySettings { return DisplaySettings{} }
	}
	if opt.PlayerURL == nil {
		opt.PlayerURL = func(int) string { return "http://127.0.0.1/player" }
	}
	s := &Supervisor{
		opt:       opt,
		proc:      newLauncher(opt.Command, opt.Log),
		cmds:      make(chan command, 8),
		state:     StateStopped,
		displayOK: true,
		waitDelay: displayWaitMin,
	}
	if opt.Command.Disabled() {
		s.state = StateDisabled
	}
	return s
}

// Run drives the browser until done is closed. It is the only goroutine that
// starts, stops or navigates the browser.
func (s *Supervisor) Run(done <-chan struct{}) {
	t := time.NewTicker(s.opt.Tick)
	defer t.Stop()
	defer s.shutdown()

	for {
		select {
		case <-done:
			return
		case c := <-s.cmds:
			s.handle(c)
		case <-t.C:
			s.step()
		}
	}
}

// Started reports if the browser reached a running state, or if the device is
// waiting for a display. The daemon writes its health marker then: a device with
// no display plugged in is healthy (plan section 15).
func (s *Supervisor) Started() bool {
	st := s.State()
	return st.Browser == StateRunning || st.Browser == StateWaiting || st.Browser == StateDisabled
}

// State gives the state of the browser for /api/status.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := State{
		Browser:          s.state,
		Rung:             s.rung,
		DisplayConnected: s.displayOK,
		Suspended:        s.suspended,
		Restarts:         s.restarts,
		LastError:        s.lastErr,
	}
	if b := s.lastBeat; b != nil {
		out.NowPlaying = &manifest.NowPlaying{
			Playlist: b.Playlist,
			Index:    b.Index,
			Item:     b.Name,
			Kind:     b.Kind,
			Since:    s.beatSince,
		}
	}
	return out
}

// Heartbeat takes one heartbeat from the player. It runs in the HTTP handler, so
// it must be quick: it records the beat and lets the loop act on it.
func (s *Supervisor) Heartbeat(hb Heartbeat) {
	now := s.opt.Now()

	s.mu.Lock()
	if s.lastBeat == nil || s.lastBeat.Playlist != hb.Playlist || s.lastBeat.Index != hb.Index {
		s.beatSince = now
	}
	copyOf := hb
	s.lastBeat = &copyOf
	reason := s.wd.heartbeat(now, hb.Frames)
	note := strings.TrimSpace(hb.Note)
	logNote := note != "" && (note != s.lastNote || now.Sub(s.lastNoteAt) >= noteRepeat)
	if logNote {
		s.lastNote = note
		s.lastNoteAt = now
	}
	s.mu.Unlock()

	// The player says what it cannot do; the ops log is where a person reads it.
	if logNote {
		s.log("player", note)
	}
	if reason != "" {
		s.Restart(reason)
	}
}

// URLItem takes over the browser for a URL item (ARCHITECTURE 7a). It gives
// true when the player must skip the item, which is what an unreachable page
// means (D19).
//
// dwellSeconds of zero keeps the browser on the page, which only happens in a
// playlist of one URL item.
func (s *Supervisor) URLItem(url string, dwellSeconds, refreshSeconds, resume int) bool {
	// Without a running browser there is nothing to navigate. The player must
	// not wait for a handover that cannot happen, so the item is skipped, the
	// same as an unreachable page.
	if st := s.State(); st.Browser != StateRunning {
		s.log("browser.url.skip", url+": the browser is "+st.Browser)
		return true
	}
	if !s.opt.Reachable(url) {
		s.log("browser.url.skip", url+" does not answer")
		return true
	}
	s.send(command{
		kind:   cmdWindow,
		url:    url,
		dwell:  time.Duration(dwellSeconds) * time.Second,
		period: time.Duration(refreshSeconds) * time.Second,
		resume: resume,
	})
	return false
}

// Restart asks for a browser restart. The watchdog and the remote command both
// use it.
func (s *Supervisor) Restart(reason string) {
	s.send(command{kind: cmdRestart, reason: reason})
}

// Suspend stops the browser. The power package calls it when the screen goes off:
// a browser with no screen only holds memory.
func (s *Supervisor) Suspend() { s.send(command{kind: cmdSuspend}) }

// Resume starts the browser again after Suspend.
func (s *Supervisor) Resume() { s.send(command{kind: cmdResume}) }

// PlaylistChanged tells the supervisor that the active playlist is different. The
// supervisor only acts when the kiosk mode starts or ends; a change between two
// normal playlists is the player's work, over the SSE stream.
func (s *Supervisor) PlaylistChanged() { s.send(command{kind: cmdPlaylist}) }

// Ready is the answer of the player to a grace request.
func (s *Supervisor) Ready() { s.send(command{kind: cmdReady}) }

// DisplayChanged tells the supervisor that the rotation or the output mode is
// different. The new values need a new browser process.
func (s *Supervisor) DisplayChanged() { s.send(command{kind: cmdDisplay}) }

// send puts a message in the queue. It never blocks: a full queue means that the
// loop is busy with a navigation, and one more restart message would only make
// it later.
func (s *Supervisor) send(c command) {
	select {
	case s.cmds <- c:
	default:
		s.log("browser.queue.full", fmt.Sprintf("the message %d was dropped", c.kind))
	}
}

// handle acts on one message. It runs in the loop goroutine.
func (s *Supervisor) handle(c command) {
	switch c.kind {
	case cmdRestart:
		s.restart(c.reason, true)
	case cmdSuspend:
		if !s.isSuspended() {
			s.setSuspended(true)
			s.stopBrowser()
			s.setState(StateStopped, "")
			s.log("browser.suspend", "the screen is off")
		}
	case cmdResume:
		if s.isSuspended() {
			s.setSuspended(false)
			s.log("browser.resume", "the screen is on")
			s.step()
		}
	case cmdWindow:
		s.openWindow(c)
	case cmdPlaylist:
		s.refreshTarget()
	case cmdReady:
		if !s.graceUntil.IsZero() {
			s.ready = true
		}
	case cmdDisplay:
		s.restart("the display settings changed", false)
	}
}

// step is one pass of the loop.
func (s *Supervisor) step() {
	if s.opt.Command.Disabled() {
		s.setState(StateDisabled, "")
		return
	}
	if s.isSuspended() {
		return
	}
	now := s.opt.Now()

	if !s.displayReady(now) {
		return
	}
	if s.nav == nil || !s.nav.Alive() {
		s.handleExit(now)
		return
	}

	s.serviceWindow(now)
	s.serviceKiosk(now)
	s.checkWatchdog(now)
	s.checkNightly(now)
}

// displayReady reports if a display is plugged in. No display is a wait state and
// never a watchdog step (D44).
func (s *Supervisor) displayReady(now time.Time) bool {
	connected := DisplayConnected(s.opt.DRMRoot)
	s.setDisplay(connected)
	if connected {
		if s.waitLogged {
			s.log("browser.display.found", "a display is connected; the browser starts")
			s.waitLogged = false
			s.waitDelay = displayWaitMin
			s.waitUntil = time.Time{}
		}
		return true
	}

	if s.nav != nil {
		s.stopBrowser()
	}
	if !s.waitLogged {
		s.log("browser.display.wait", "no display is connected; the device waits and does not count a restart")
		s.waitLogged = true
	}
	s.setState(StateWaiting, "no display is connected")
	if now.Before(s.waitUntil) {
		return false
	}
	s.waitUntil = now.Add(s.waitDelay)
	if s.waitDelay < displayWaitMax {
		s.waitDelay *= 2
		if s.waitDelay > displayWaitMax {
			s.waitDelay = displayWaitMax
		}
	}
	return false
}

// handleExit starts the browser, and counts a restart when the browser died on
// its own.
func (s *Supervisor) handleExit(now time.Time) {
	if s.everUp && !s.expectExit {
		reason := s.proc.exitReason()
		s.log("browser.exit", reason)
		if s.countRestart(now, reason) {
			return // the device reboots
		}
	}
	s.expectExit = false
	s.launch(now)
}

// launch starts the browser and picks the navigation rung.
//
// The rung is picked once, at the first launch, and then it stays for the life of
// the daemon (ARCHITECTURE section 7). A device that has no DevTools port must not
// wait 45 seconds at every restart to learn the same thing again.
func (s *Supervisor) launch(now time.Time) {
	url := s.desiredURL()
	s.target = url
	s.setState(StateStarting, "")

	wait := s.opt.CDPTimeout
	if wait <= 0 {
		wait = cdpStartTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait+navTimeout)
	defer cancel()

	if !s.rungPicked || s.rung == "cdp" {
		nav := newCDP(s.proc, s.opt.Command.Endpoint(), s.opt.CDPTimeout, s.opt.Log)
		if err := nav.Start(ctx, url); err == nil {
			s.useNav(nav, now, ctx)
			return
		} else if !s.rungPicked {
			s.log("browser.rung.cdp.fail", err.Error())
		} else {
			s.log("browser.rung.cdp.fail", err.Error()+"; this launch uses the relaunch rung")
		}
	}

	nav := newRelaunch(s.proc)
	if err := nav.Start(ctx, url); err != nil {
		s.setState(StateStopped, err.Error())
		s.log("browser.start.fail", err.Error())
		return
	}
	s.useNav(nav, now, ctx)
}

// useNav takes a navigator that started, and finishes the launch.
func (s *Supervisor) useNav(nav Navigator, now time.Time, ctx context.Context) {
	s.nav = nav
	s.everUp = true
	s.rungPicked = true
	s.setRung(nav.Name())
	s.setState(StateRunning, "")

	s.mu.Lock()
	s.wd.started(now)
	s.mu.Unlock()

	s.log("browser.start", "rung="+nav.Name()+" url="+s.target)
	s.applyOutput(ctx)
	// The kiosk state and the window state belong to the URL that we started on.
	s.refreshTarget()
}

// restart stops the browser and lets the next step start it again. counted says
// if this restart belongs to the watchdog ladder: a restart that a person or the
// nightly job asked for does not.
func (s *Supervisor) restart(reason string, counted bool) {
	now := s.opt.Now()
	s.graceUntil = time.Time{}
	s.ready = false
	s.log("browser.restart", reason)
	s.stopBrowser()
	s.setState(StateStarting, "")
	if counted && s.countRestart(now, reason) {
		return
	}
	s.step()
}

// countRestart records a restart and asks for a reboot at the fourth one in an
// hour. It gives true when it asked for the reboot.
func (s *Supervisor) countRestart(now time.Time, reason string) bool {
	s.mu.Lock()
	count, reboot := s.wd.restarted(now)
	s.restarts = count
	if reboot {
		s.wd.rebootDone()
	}
	s.mu.Unlock()

	if !reboot {
		return false
	}
	text := fmt.Sprintf("%d browser restarts in one hour; the last reason was: %s", count, reason)
	s.log("browser.reboot", text)
	if s.opt.Reboot != nil {
		s.opt.Reboot(text)
		return true
	}
	return false
}

// stopBrowser ends the browser and forgets the page state.
func (s *Supervisor) stopBrowser() {
	s.expectExit = true
	if s.nav != nil {
		s.nav.Stop()
		s.nav = nil
	} else {
		s.proc.stop()
	}
	s.window = nil
	s.kiosk = nil
	s.target = ""
}

// shutdown ends the browser when the daemon stops.
func (s *Supervisor) shutdown() {
	s.stopBrowser()
	s.setState(StateStopped, "")
}

// desiredURL gives the URL that the browser must show now.
func (s *Supervisor) desiredURL() string {
	if w := s.window; w != nil {
		return w.url
	}
	if active := s.opt.Active(); active.KioskURL != "" {
		if s.kiosk != nil && s.kiosk.fallback {
			return s.opt.PlayerURL(-1)
		}
		return active.KioskURL
	}
	return s.opt.PlayerURL(-1)
}

// refreshTarget puts the browser on the URL that it must show. It is how the
// kiosk mode starts and ends.
func (s *Supervisor) refreshTarget() {
	if s.nav == nil || s.isSuspended() {
		return
	}
	active := s.opt.Active()

	switch {
	case active.KioskURL == "":
		if s.kiosk != nil {
			s.kiosk = nil
			s.log("browser.kiosk.end", "the playlist is not a single URL any more")
			s.navigate(s.opt.PlayerURL(-1))
		}
	case s.kiosk == nil || s.kiosk.url != active.KioskURL:
		s.startKiosk(active)
	}
}

// startKiosk parks the browser on a single URL playlist (D42).
func (s *Supervisor) startKiosk(active Active) {
	s.window = nil
	s.kiosk = &kioskPage{
		url:     active.KioskURL,
		period:  time.Duration(active.RefreshSeconds) * time.Second,
		backoff: kioskRetryMin,
	}
	now := s.opt.Now()
	s.kiosk.reloaded = now
	s.kiosk.polled = now

	if !s.opt.Reachable(active.KioskURL) {
		s.enterKioskFallback(now, "the page does not answer")
		return
	}
	s.log("browser.kiosk.start", active.KioskURL)
	s.navigate(active.KioskURL)
}

// enterKioskFallback shows the fallback screen and starts the retry backoff.
func (s *Supervisor) enterKioskFallback(now time.Time, reason string) {
	s.kiosk.fallback = true
	s.kiosk.retryAt = now.Add(s.kiosk.backoff)
	s.log("browser.kiosk.fallback", s.kiosk.url+": "+reason)
	s.navigate(s.opt.PlayerURL(-1))
}

// serviceKiosk keeps a kiosk page fresh and alive.
func (s *Supervisor) serviceKiosk(now time.Time) {
	k := s.kiosk
	if k == nil || s.window != nil {
		return
	}

	if k.fallback {
		if now.Before(k.retryAt) {
			return
		}
		if !s.opt.Reachable(k.url) {
			k.backoff *= 2
			if k.backoff > kioskRetryMax {
				k.backoff = kioskRetryMax
			}
			k.retryAt = now.Add(k.backoff)
			return
		}
		k.fallback = false
		k.backoff = kioskRetryMin
		k.reloaded = now
		k.polled = now
		s.log("browser.kiosk.back", k.url+" answers again")
		s.navigate(k.url)
		return
	}

	if k.period > 0 && now.Sub(k.reloaded) >= k.period {
		k.reloaded = now
		if err := s.reload(); err != nil {
			s.restart("the kiosk page could not be loaded again: "+err.Error(), true)
			return
		}
	}
	if now.Sub(k.polled) >= livenessPoll {
		k.polled = now
		if err := s.poll(); err != nil {
			s.restart("the control rung does not answer during the kiosk page: "+err.Error(), true)
		}
	}
}

// openWindow starts a URL window.
func (s *Supervisor) openWindow(c command) {
	if s.nav == nil {
		return
	}
	now := s.opt.Now()
	s.window = &urlWindow{
		url:      c.url,
		dwell:    c.dwell,
		period:   c.period,
		resume:   c.resume,
		started:  now,
		reloaded: now,
		polled:   now,
	}
	s.log("browser.url.start", fmt.Sprintf("%s dwell=%s resume=%d", c.url, c.dwell, c.resume))
	s.navigate(c.url)
}

// serviceWindow runs an open URL window: the dwell time, the reloads and the
// liveness polls (ARCHITECTURE 7a).
func (s *Supervisor) serviceWindow(now time.Time) {
	w := s.window
	if w == nil {
		return
	}

	if w.dwell > 0 && now.Sub(w.started) >= w.dwell {
		s.window = nil
		s.log("browser.url.end", fmt.Sprintf("%s after %s", w.url, w.dwell))
		s.mu.Lock()
		s.wd.started(now) // the player answers again from here
		s.mu.Unlock()
		s.navigate(s.opt.PlayerURL(w.resume))
		return
	}
	if w.period > 0 && now.Sub(w.reloaded) >= w.period {
		w.reloaded = now
		if err := s.reload(); err != nil {
			s.restart("the URL item could not be loaded again: "+err.Error(), true)
			return
		}
	}
	if now.Sub(w.polled) >= livenessPoll {
		w.polled = now
		if err := s.poll(); err != nil {
			s.restart("the control rung does not answer during a URL item: "+err.Error(), true)
		}
	}
}

// checkWatchdog looks for silence from the player.
func (s *Supervisor) checkWatchdog(now time.Time) {
	inWindow := s.window != nil || (s.kiosk != nil && !s.kiosk.fallback)

	s.mu.Lock()
	reason := s.wd.idle(now, inWindow)
	s.mu.Unlock()

	if reason != "" {
		s.restart(reason, true)
	}
}

// checkNightly runs the daily browser restart (plan 3.3).
//
// It asks the player for a good moment first and forces the restart after the
// grace time. A screen-off schedule that covers the time skips it: the off and on
// cycle is the restart, and a second line in the ops log would only confuse.
func (s *Supervisor) checkNightly(now time.Time) {
	if !s.graceUntil.IsZero() {
		if s.ready || now.After(s.graceUntil) {
			how := "the player is at an item boundary"
			if !s.ready {
				how = "the grace time ended"
			}
			s.graceUntil = time.Time{}
			s.ready = false
			s.restart("the nightly restart: "+how, false)
		}
		return
	}

	if s.opt.NightlyRestart == nil {
		return
	}
	target, ok := clockMinutes(s.opt.NightlyRestart())
	if !ok {
		return
	}
	day := now.Format("2006-01-02")
	if s.lastDay == day {
		return
	}
	minute := now.Hour()*60 + now.Minute()
	// A two minute window, so that a busy loop or a short suspend cannot miss
	// the time.
	if minute < target || minute > target+1 {
		return
	}
	s.lastDay = day

	if s.opt.ScreenOffCovers != nil && s.opt.ScreenOffCovers(now) {
		s.log("browser.nightly.skip", "the screen schedule has the screen off at this time")
		return
	}
	s.log("browser.nightly.grace", "the player has "+graceTimeout.String()+" to reach an item boundary")
	s.graceUntil = now.Add(graceTimeout)
	s.ready = false
	if s.opt.Grace != nil {
		s.opt.Grace()
	}
}

// navigate sends the browser to url and restarts it when the rung fails.
func (s *Supervisor) navigate(url string) {
	if s.nav == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), navTimeout)
	defer cancel()

	s.target = url
	if err := s.nav.Navigate(ctx, url); err != nil {
		s.log("browser.navigate.fail", url+": "+err.Error())
		s.restart("the navigation to the next page failed", true)
	}
}

func (s *Supervisor) reload() error {
	ctx, cancel := context.WithTimeout(context.Background(), navTimeout)
	defer cancel()
	return s.nav.Reload(ctx)
}

// poll asks the browser where it is. An error means that the control rung is
// dead, which is one of the three reasons to restart the browser (plan 3.3).
func (s *Supervisor) poll() error {
	ctx, cancel := context.WithTimeout(context.Background(), navTimeout)
	defer cancel()
	url, err := s.nav.CurrentURL(ctx)
	if err != nil {
		return err
	}
	if url == "" {
		return fmt.Errorf("the browser reports no page")
	}
	return nil
}

func (s *Supervisor) display() DisplaySettings { return s.opt.Display() }

func (s *Supervisor) setState(state, reason string) {
	s.mu.Lock()
	s.state = state
	s.lastErr = reason
	s.mu.Unlock()
}

func (s *Supervisor) setRung(name string) {
	s.mu.Lock()
	s.rung = name
	s.mu.Unlock()
}

func (s *Supervisor) setDisplay(connected bool) {
	s.mu.Lock()
	s.displayOK = connected
	s.mu.Unlock()
}

func (s *Supervisor) setSuspended(v bool) {
	s.mu.Lock()
	s.suspended = v
	s.mu.Unlock()
}

// isSuspended reports if the screen is off.
func (s *Supervisor) isSuspended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.suspended
}

func (s *Supervisor) log(event, details string) {
	if s.opt.Log != nil {
		s.opt.Log.Log(event, details)
	}
}

// clockMinutes reads "HH:MM" into minutes after midnight.
func clockMinutes(value string) (int, bool) {
	if len(value) != 5 || value[2] != ':' {
		return 0, false
	}
	h := int(value[0]-'0')*10 + int(value[1]-'0')
	m := int(value[3]-'0')*10 + int(value[4]-'0')
	if value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' ||
		value[3] < '0' || value[3] > '9' || value[4] < '0' || value[4] > '9' ||
		h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
