package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
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
	// displayProbeEvery is the time between two reads of the DRM connectors. It is
	// also the shortest step of the wait backoff. The loop runs for months, and a
	// read of /sys at every tick is a read that nobody needs. A hotplug is seen
	// inside this time, which D44 permits.
	displayProbeEvery = 5 * time.Second
	// displayWaitMax is the longest step of the wait for a display (D44). The wait
	// never counts toward the restart ladder.
	displayWaitMax = 60 * time.Second
	// launchRetryMin and launchRetryMax are the backoff of a launch that fails. A
	// missing binary or a full tmpfs fails at every try. A try at every tick fills
	// the ops log and trips the reboot rung in four seconds.
	launchRetryMin = 5 * time.Second
	launchRetryMax = 60 * time.Second
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
	// noteMemory is how many different notes the rate limit remembers. Two broken
	// items that take turns are the case that one remembered note cannot hold.
	noteMemory = 32
)

// The values of State.Browser. They are the words that /api/status uses.
const (
	StateStopped  = "stopped"
	StateStarting = "starting"
	StateRunning  = "running"
	StateWaiting  = "waiting-for-display"
	StateDisabled = "disabled"
)

// ErrBusy says that the command queue of the browser is full. The loop is inside
// a navigation, and one more message would only make it later. A caller that
// answers a person must report this: a command that says "done" and does nothing
// is worse than an error.
var ErrBusy = errors.New("the browser is busy; ask again in a moment")

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
	// Reboot reboots the device. It is rung 2 of the watchdog ladder. A nil
	// function becomes an ops log line, so the ladder always ends somewhere.
	Reboot func(reason string)
	// Grace asks the player for a good moment to restart. The player answers with
	// POST /api/player/ready, which the daemon passes to Ready.
	Grace func()
	// NightlyRestart gives the time of the daily restart as "HH:MM", or "".
	NightlyRestart func() string
	// Watchdog gives the live thresholds of the ladder, from the [watchdog] table
	// of portapixel.toml (D30). The loop asks at each check, so a save takes effect
	// at once. A nil function uses DefaultWatchdog.
	Watchdog func() WatchdogSettings
	// ScreenOffCovers reports that the screen schedule has the screen off at t.
	// The nightly restart is then pointless and is skipped (plan 3.3).
	ScreenOffCovers func(t time.Time) bool
	// DRMRoot is where the display connectors are. "" uses /sys/class/drm.
	DRMRoot string
	// DisplayProbe is the time between two reads of the connectors. 0 uses
	// displayProbeEvery. A test sets a short value.
	DisplayProbe time.Duration
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
	window     *urlWindow
	kiosk      *kioskPage
	target     string
	expectExit bool
	everUp     bool
	// launchFailed says that the last launch did not start a browser. The retry
	// then waits: one continuous fault is one step on the ladder, not one step
	// for each tick.
	launchFailed bool
	launchDelay  time.Duration
	launchNext   time.Time
	// The display probe and its backoff.
	probedAt    time.Time
	probeNext   time.Time
	lastDisplay bool
	waitDelay   time.Duration
	waitLogged  bool
	// pendingRestart holds the reason of a restart that arrived while the screen
	// was off. The resume starts a new browser, which is that restart.
	pendingRestart string
	graceUntil     time.Time
	ready          bool
	lastDay        string
	badNightly     string

	// Fields under the lock. Run writes them, and the HTTP handlers read them.
	mu        sync.Mutex
	wd        watchdog
	state     string
	rung      string
	displayOK bool
	suspended bool
	restarts  int
	lastErr   string
	lastBeat  *Heartbeat
	beatSince time.Time
	// notes remembers when each player note last went in the ops log.
	notes map[string]time.Time
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
	if opt.DisplayProbe <= 0 {
		opt.DisplayProbe = displayProbeEvery
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
	if opt.Watchdog == nil {
		opt.Watchdog = DefaultWatchdog
	}
	s := &Supervisor{
		opt:         opt,
		proc:        newLauncher(opt.Command, opt.Log),
		cmds:        make(chan command, 8),
		state:       StateStopped,
		displayOK:   true,
		lastDisplay: true,
		waitDelay:   opt.DisplayProbe,
		launchDelay: launchRetryMin,
		notes:       make(map[string]time.Time),
	}
	if s.opt.Reboot == nil {
		// The ladder must always end somewhere. Without a reboot function the end
		// is a line that a person can read.
		s.opt.Reboot = func(reason string) {
			s.log("browser.reboot.none", "no reboot function is wired: "+reason)
		}
	}
	if opt.Command.Disabled() {
		s.state = StateDisabled
	}
	return s
}

// Run drives the browser until done is closed. It is the only goroutine that
// starts, stops or navigates the browser.
func (s *Supervisor) Run(done <-chan struct{}) {
	defer s.reportPanic()
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

// reportPanic writes a panic of the loop into the ops log and then lets it go on.
//
// A device that dies must say why. The ops log is on ext4, so a person reads the
// line after the reboot. The panic then ends the process with a code that is not
// zero, and the OpenRC supervisor starts the daemon again.
func (s *Supervisor) reportPanic() {
	r := recover()
	if r == nil {
		return
	}
	s.log("browser.panic", fmt.Sprintf("%v; %s", r, firstFrames(debug.Stack())))
	panic(r)
}

// firstFrames keeps the top of a stack trace. The ops log trims at 1000 lines,
// so a full trace would push out the lines that say what happened before.
func firstFrames(stack []byte) string {
	lines := strings.Split(string(stack), "\n")
	if len(lines) > 12 {
		lines = lines[:12]
	}
	return strings.Join(lines, " | ")
}

// Started reports if the browser reached a running state, or if the device is
// waiting for a display, or if the screen is off. The daemon writes its health
// marker then: a device with no display plugged in, and a device in its night
// hours, are both healthy (plan section 15).
func (s *Supervisor) Started() bool {
	st := s.State()
	if st.Suspended {
		return true
	}
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
	reason := s.wd.heartbeat(now, hb.Frames, s.opt.Watchdog())
	note := strings.TrimSpace(hb.Note)
	logNote := note != "" && s.rememberNote(note, now)
	s.mu.Unlock()

	// The player says what it cannot do; the ops log is where a person reads it.
	if logNote {
		s.log("player", note)
	}
	if reason != "" {
		s.Restart(reason)
	}
}

// rememberNote reports if a note may go in the ops log now, and records it. The
// caller holds the lock.
//
// Every note has its own time, because two broken items that take turns would
// beat a memory of one note and write a line at every heartbeat.
func (s *Supervisor) rememberNote(note string, now time.Time) bool {
	if last, seen := s.notes[note]; seen && now.Sub(last) < noteRepeat {
		return false
	}
	if len(s.notes) >= noteMemory {
		oldest, at := "", time.Time{}
		for k, v := range s.notes {
			if at.IsZero() || v.Before(at) {
				oldest, at = k, v
			}
		}
		delete(s.notes, oldest)
	}
	s.notes[note] = now
	return true
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
	if !s.send(command{
		kind:   cmdWindow,
		url:    url,
		dwell:  time.Duration(dwellSeconds) * time.Second,
		period: time.Duration(refreshSeconds) * time.Second,
		resume: resume,
	}) {
		// The message was dropped. An answer of "I took the browser" here stops
		// the player for a handover that never comes. The screen then holds one
		// item until the watchdog restarts the browser.
		s.log("browser.url.skip", url+": the browser queue is full")
		return true
	}
	return false
}

// Restart asks for a browser restart. The watchdog and the remote command both
// use it.
func (s *Supervisor) Restart(reason string) error {
	return s.queue(command{kind: cmdRestart, reason: reason})
}

// Suspend stops the browser. The power package calls it when the screen goes off:
// a browser with no screen only holds memory.
func (s *Supervisor) Suspend() error { return s.queue(command{kind: cmdSuspend}) }

// Resume starts the browser again after Suspend.
func (s *Supervisor) Resume() error { return s.queue(command{kind: cmdResume}) }

// PlaylistChanged tells the supervisor that the active playlist is different. The
// supervisor only acts when the kiosk mode starts or ends; a change between two
// normal playlists is the player's work, over the SSE stream.
func (s *Supervisor) PlaylistChanged() { s.send(command{kind: cmdPlaylist}) }

// Ready is the answer of the player to a grace request.
func (s *Supervisor) Ready() { s.send(command{kind: cmdReady}) }

// DisplayChanged tells the supervisor that the rotation or the output mode is
// different. The new values need a new browser process.
func (s *Supervisor) DisplayChanged() { s.send(command{kind: cmdDisplay}) }

// queue puts a message in the queue and reports a full queue as ErrBusy. The API
// handlers use it: a person who asks for a restart must learn that it did not
// happen.
func (s *Supervisor) queue(c command) error {
	if !s.send(c) {
		return ErrBusy
	}
	return nil
}

// send puts a message in the queue. It never blocks: a full queue means that the
// loop is busy with a navigation. It gives false when the message was dropped.
func (s *Supervisor) send(c command) bool {
	select {
	case s.cmds <- c:
		return true
	default:
		s.log("browser.queue.full", fmt.Sprintf("the message %d was dropped", c.kind))
		return false
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
			if s.pendingRestart != "" {
				s.log("browser.restart", s.pendingRestart+"; the screen is on again")
				s.pendingRestart = ""
			}
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
//
// The read of /sys happens on a cadence, not at every tick. While a display is
// connected the cadence is Options.DisplayProbe. While none is connected the
// cadence doubles up to displayWaitMax, so a television that stays in standby for
// a month costs one read a minute.
func (s *Supervisor) displayReady(now time.Time) bool {
	if !s.probedAt.IsZero() && now.Before(s.probeNext) {
		return s.lastDisplay
	}
	s.probedAt = now
	connected := DisplayConnected(s.opt.DRMRoot)
	s.lastDisplay = connected
	s.setDisplay(connected)

	if connected {
		s.probeNext = now.Add(s.opt.DisplayProbe)
		if s.waitLogged {
			s.log("browser.display.found", "a display is connected; the browser starts")
			s.waitLogged = false
		}
		s.waitDelay = s.opt.DisplayProbe
		return true
	}

	s.probeNext = now.Add(s.waitDelay)
	if s.waitDelay < displayWaitMax {
		s.waitDelay *= 2
		if s.waitDelay > displayWaitMax {
			s.waitDelay = displayWaitMax
		}
	}
	if s.nav != nil {
		s.stopBrowser()
	}
	if !s.waitLogged {
		s.log("browser.display.wait", "no display is connected; the device waits and does not count a restart")
		s.waitLogged = true
	}
	s.setState(StateWaiting, "no display is connected")
	return false
}

// handleExit starts the browser, and counts a restart when the browser died on
// its own.
func (s *Supervisor) handleExit(now time.Time) {
	// A launch that failed left no browser and no navigator. The fault is the
	// launch, not a browser that died, so the retry waits and counts nothing: one
	// continuous fault is one step on the ladder.
	if s.launchFailed {
		if now.Before(s.launchNext) {
			return
		}
		s.launch(now)
		return
	}
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
//
// THIS CALL HOLDS THE LOOP, AND THAT IS DELIBERATE. The bound is
// CDPTimeout + navTimeout, which is 45 s + 20 s = 65 s on the FIRST launch of a
// device with no DevTools port, and a small fraction of a second after that. One
// goroutine owns the browser, so nothing needs a lock, and a launch that ran beside
// the loop would need a second state machine for "a launch is in flight".
//
// What the wait costs, measured against each caller:
//
//   - The player. It is not up yet, so there is nothing to hold.
//   - The watchdog. checkWatchdog runs after the launch in the same pass, and
//     restart() starts the silence timer again, so the wait counts no step of the
//     reboot ladder.
//   - A URL item. URLItem answers "skip" at once while the browser is not running
//     (State is not StateRunning), so the player never waits: it plays the next item
//     and comes back to this one on the next pass (D19).
//   - A command from a person or from the fleet queue. send() answers ErrBusy when
//     the queue is full, and the API turns that into 503 with a sentence. A command
//     that says "done" and does nothing is worse than an error.
//   - The screen schedule. power.apply keeps its old state when the browser refuses,
//     so the next tick of the power loop tries the transition again.
//
// A restructure would have to keep all five of those answers. It is not worth the
// second state machine for a wait that happens one time per boot.
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

	rung := s.currentRung()
	if rung == "" || rung == RungCDP {
		nav := newCDP(s.proc, s.opt.Command.Endpoint(), s.opt.CDPTimeout)
		if err := nav.Start(ctx, url); err == nil {
			s.useNav(nav, now)
			return
		} else if rung == "" {
			s.log("browser.rung.cdp.fail", err.Error())
		} else {
			s.log("browser.rung.cdp.fail", err.Error()+"; this launch uses the relaunch rung")
		}
	}

	nav := newRelaunch(s.proc)
	if err := nav.Start(ctx, url); err != nil {
		s.launchFailed = true
		s.launchNext = now.Add(s.launchDelay)
		s.setState(StateStopped, err.Error())
		s.log("browser.start.fail", fmt.Sprintf("%s; the next try is in %s", err.Error(), s.launchDelay))
		if s.launchDelay < launchRetryMax {
			s.launchDelay *= 2
			if s.launchDelay > launchRetryMax {
				s.launchDelay = launchRetryMax
			}
		}
		return
	}
	s.useNav(nav, now)
}

// useNav takes a navigator that started, and finishes the launch.
func (s *Supervisor) useNav(nav Navigator, now time.Time) {
	s.nav = nav
	s.everUp = true
	s.launchFailed = false
	s.launchDelay = launchRetryMin
	s.setRung(nav.Name())
	s.setState(StateRunning, "")

	s.mu.Lock()
	s.wd.started(now)
	s.mu.Unlock()

	s.log("browser.start", "rung="+nav.Name()+" url="+RedactURL(s.target))
	// The rotation waits for the Wayland socket, which cage makes some time after
	// it forks. That wait belongs in its own goroutine: a tick that waits is a
	// tick that does not watch the browser.
	go s.applyDisplay()
	// The kiosk state and the window state belong to the URL that we started on.
	s.refreshTarget()
}

// restart stops the browser and lets the next tick start it again. counted says
// if this restart belongs to the watchdog ladder: a restart that a person or the
// nightly job asked for does not.
//
// It never calls step() itself. The tick does that. A restart that started the
// browser again from inside itself can come back here through the ladder. A reboot
// function that does nothing then makes a recursion with no end.
func (s *Supervisor) restart(reason string, counted bool) {
	now := s.opt.Now()
	s.graceUntil = time.Time{}
	s.ready = false

	if s.isSuspended() {
		// No browser runs. The resume starts a new one, which is the restart.
		s.pendingRestart = reason
		s.log("browser.restart.hold", reason+"; the screen is off, so the restart waits for it")
		return
	}

	s.log("browser.restart", reason)
	s.stopBrowser()
	s.setState(StateStarting, "")
	// A restart is a fresh try, so the launch backoff starts again.
	s.launchFailed = false
	s.launchDelay = launchRetryMin
	// The silence timer starts again as well. stopBrowser cleared the URL window
	// and the kiosk page, so checkWatchdog in the SAME pass of step now sees no
	// window and the old lastBeat of the time before that window. It read that as
	// silence and restarted a second time, so ONE fault of the control rung took
	// two steps of the reboot ladder and the device rebooted after two faults and
	// not after four.
	s.mu.Lock()
	s.wd.started(now)
	s.mu.Unlock()
	if counted {
		s.countRestart(now, reason)
	}
}

// countRestart records a restart and asks for a reboot at the limit of the
// [watchdog] table. It gives true when it asked for the reboot.
func (s *Supervisor) countRestart(now time.Time, reason string) bool {
	set := s.opt.Watchdog()

	s.mu.Lock()
	count, reboot := s.wd.restarted(now, set)
	s.restarts = count
	if reboot {
		// The window starts again, so State never reports more restarts than the
		// window holds.
		s.wd.rebootDone()
		s.restarts = 0
	}
	s.mu.Unlock()

	if !reboot {
		return false
	}
	text := fmt.Sprintf("%d browser restarts in %s; the last reason was: %s", count, set.RestartWindow, reason)
	s.log("browser.reboot", text)
	s.opt.Reboot(text)
	return true
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
			s.handBackToPlayer(-1)
		}
	case s.kiosk == nil || s.kiosk.url != active.KioskURL:
		s.startKiosk(active)
	}
}

// handBackToPlayer puts the browser on the player SPA.
//
// Every path that gives the page back to the player goes through here. The
// watchdog must count the silence from the moment that the player can answer. A
// path that forgot the reset read hours of old silence. It then restarted the
// browser at the next tick, and that is a counted step on the ladder.
func (s *Supervisor) handBackToPlayer(resume int) {
	s.mu.Lock()
	s.wd.started(s.opt.Now())
	s.mu.Unlock()
	s.navigate(s.opt.PlayerURL(resume))
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
	s.handBackToPlayer(-1)
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
	s.logRepeat("browser.url.start", c.url, fmt.Sprintf("%s dwell=%s resume=%d", c.url, c.dwell, c.resume))
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
		s.logRepeat("browser.url.end", w.url, fmt.Sprintf("%s after %s", w.url, w.dwell))
		s.handBackToPlayer(w.resume)
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
	set := s.opt.Watchdog()

	s.mu.Lock()
	reason := s.wd.idle(now, inWindow, set)
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
	value := s.opt.NightlyRestart()
	target, ok := config.ParseClock(value)
	if !ok {
		// A value that is not a time would make the nightly restart go away with
		// no word about it. Say it once for each different value.
		if value != "" && value != s.badNightly {
			s.badNightly = value
			s.log("browser.nightly.bad", value+" is not a time in the form HH:MM; there is no nightly restart")
		}
		return
	}
	s.badNightly = ""
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
		s.log("browser.navigate.fail", RedactURL(url)+": "+err.Error())
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

// currentRung gives the rung that the daemon chose, or "" before the first
// launch.
func (s *Supervisor) currentRung() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rung
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

// logRepeat writes a line that a normal loop of the playlist produces again and
// again. It writes at most one line per URL per noteRepeat.
//
// Why: the ops log is a file on the flash card. A playlist with one URL item wrote
// browser.url.start and browser.url.end at every pass, which is about 1800 lines a
// day, and the trim then rewrites the whole file twice a day. A device in its
// steady state writes nothing to the flash (D2). The slog line always goes out, and
// that one lands in RAM (D35).
func (s *Supervisor) logRepeat(event, key, details string) {
	slog.Debug(event, "details", details)
	s.mu.Lock()
	first := s.rememberNote(event+" "+key, s.opt.Now())
	s.mu.Unlock()
	if first {
		s.log(event, details)
	}
}

// RedactURL hides the boot secret of a player URL.
//
// The ops log is a file that a person reads over SSH and copies into a support
// message, and /var/log holds the same line. The secret in ?k= gates the whole
// player API (D46), so it never goes in a log.
func RedactURL(url string) string {
	for _, sep := range []string{"?k=", "&k="} {
		at := strings.Index(url, sep)
		if at < 0 {
			continue
		}
		start := at + len(sep)
		if end := strings.IndexByte(url[start:], '&'); end >= 0 {
			url = url[:start] + "REDACTED" + url[start+end:]
		} else {
			url = url[:start] + "REDACTED"
		}
	}
	return url
}
