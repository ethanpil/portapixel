package player

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/fallback"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
)

// The times of the supervisor. Together with the block in watchdog.go this is
// every threshold of the player.
const (
	// defaultTick is how often the supervisor looks at the world.
	defaultTick = time.Second
	// pollEvery is the time between two state requests to mpv: the playlist
	// position, the time position, and for a video the dropped frames. Each
	// answer is also the heartbeat of the IPC.
	pollEvery = 2 * time.Second
	// graceTimeout is how long the nightly restart waits for an item boundary.
	// After it, the restart happens in the middle of an item (plan 3.3).
	graceTimeout = 60 * time.Second
	// displayProbeEvery is the time between two reads of the DRM connectors. It is
	// also the shortest step of the wait backoff. A hotplug is seen inside this
	// time, which D44 permits.
	displayProbeEvery = 5 * time.Second
	// displayWaitMax is the longest step of the wait for a display (D44).
	displayWaitMax = 60 * time.Second
	// launchRetryMin and launchRetryMax are the backoff of a start that fails, and
	// of an mpv that ends before it showed anything. A missing binary fails at each
	// try, and a try at each tick fills the ops log.
	launchRetryMin = 5 * time.Second
	launchRetryMax = 60 * time.Second
	// fallbackCheck is how often the data of the fallback screen is compared with
	// the picture on the screen. The clock changes each minute, so a new minute
	// shows inside this time.
	fallbackCheck = 5 * time.Second
	// shiftEvery moves the fallback screen a little, against burn-in. It is a
	// whole number of minutes, so the step changes with the clock on the screen.
	shiftEvery = 3 * time.Minute
	// failedRetry is the wait before the player tries a playlist again when no
	// item of it could play. The fallback screen shows in that time.
	failedRetry = 5 * time.Minute
	// noteRepeat is how often the same fault may go in the ops log. A broken item
	// in a loop fails at each pass, and a log that holds one fault a thousand
	// times holds nothing else (ARCHITECTURE 7a).
	noteRepeat = time.Hour
	// noteMemory is how many different notes the rate limit remembers.
	noteMemory = 32
	// maxPending is how many requests may wait for an answer. A frozen mpv
	// answers none, and the list must not grow for ever while the ladder is off.
	maxPending = 32
	// suspendWait is the longest that Suspend waits for mpv to end.
	suspendWait = 3 * stopGrace
)

// The values of State.Player. They are the words of player_state in
// /api/status.
const (
	StateStopped  = "stopped"
	StateStarting = "starting"
	StateRunning  = "running"
	StateWaiting  = "waiting-for-display"
	StateDisabled = "disabled"
)

// The item kinds of the manifest.
const (
	kindImage = "image"
	kindVideo = "video"
)

// The properties that the supervisor observes. The number is the ID of the
// observation, which mpv puts in each property-change event.
const (
	obsIdle  = 1 // idle-active: mpv has no file
	obsHwdec = 2 // hwdec-current: the decoder of the video
	obsFault = 3 // user-data/pptr/fault: a fault of transitions.lua
	obsMoved = 4 // user-data/pptr/moved: the result of a moving crossfade
)

// ErrBusy says that the command queue of the player is full. A caller that
// answers a person must report this: a command that says "done" and does
// nothing is worse than an error.
var ErrBusy = errors.New("the player is busy; ask again in a moment")

// DisplaySettings are the display keys that the player applies.
type DisplaySettings struct {
	Rotation    int
	VideoMode   string
	VideoOutput string // auto | gpu | drm
}

// State is what the supervisor reports to /api/status.
type State struct {
	Player string
	// VideoOutput is "gpu" or "drm" after the first start, else "".
	VideoOutput      string
	Hwdec            string
	DisplayConnected bool
	Suspended        bool
	Restarts         int
	NowPlaying       *manifest.NowPlaying
	// LastError is the reason that mpv is not running, when it is not.
	LastError string
}

// Options are the parameters of a Supervisor. Each one that touches the world
// outside this package is a function, so a test can give a fake.
type Options struct {
	Command CommandConfig
	Log     *opslog.Log
	// Now gives the time. The supervisor measures each duration with it, so a value
	// from time.Now is right: its monotonic reading hides a step of the system
	// clock. A device with no RTC gets such a step, of hours or days, at the first
	// sync of the clock, while the player already runs. Without the reading, that
	// step looks like a stall. A nil function uses time.Now.
	Now func() time.Time
	// Local gives t in the time zone of the device. The nightly restart time and the
	// clock of the fallback screen are local times. The supervisor never measures a
	// duration with its result, because it removes the monotonic reading. A nil
	// function leaves t as it is.
	Local func(t time.Time) time.Time
	// Manifest gives what must play now.
	Manifest func() library.PlayerManifest
	// Fallback gives the data of the fallback screen. The supervisor adds the
	// clock and the burn-in step.
	Fallback func() fallback.Info
	// Render draws the fallback screen. A nil function uses fallback.Render.
	Render func(info fallback.Info, w, h int) ([]byte, error)
	// Display gives the rotation, the output mode and the video output.
	Display func() DisplaySettings
	// Reboot reboots the device. It is the last rung of the watchdog ladder. A
	// nil function becomes an ops log line, so the ladder always ends somewhere.
	Reboot func(reason string)
	// NightlyRestart gives the time of the daily restart as "HH:MM", or "".
	NightlyRestart func() string
	// Motion gives playback.motion: "auto", "on" or "off". A nil function
	// gives "auto".
	Motion func() string
	// Watchdog gives the live thresholds of the ladder (D30). A nil function uses
	// DefaultWatchdog.
	Watchdog func() WatchdogSettings
	// ScreenOffCovers reports that the screen schedule has the screen off at t.
	// The nightly restart is then pointless and is skipped (plan 3.3).
	ScreenOffCovers func(t time.Time) bool
	// DRMRoot is where the display connectors are. "" uses /sys/class/drm.
	DRMRoot string
	// ModelPath is the file that names the board. "" uses ModelPath.
	ModelPath string
	// DisplayProbe is the time between two reads of the connectors. 0 uses
	// displayProbeEvery.
	DisplayProbe time.Duration
	// Tick is the period of the loop. 0 uses one second.
	Tick time.Duration
}

// command is one message to the loop. The loop owns mpv, so everything that
// changes it comes through this channel.
type command struct {
	kind   int
	reason string
	reply  chan error
}

const (
	cmdRestart = iota
	cmdSuspend
	cmdResume
)

// resumeAt says where a new mpv starts in the playlist.
type resumeAt int

const (
	resumeStart resumeAt = iota // the first item
	resumeSame                  // the item on the screen: a restart that a person asked for
	resumeNext                  // the item after it: the item on the screen can be the fault
)

// resumePoint is the item that the next load of the same playlist starts at.
type resumePoint struct {
	name  string
	count int
	index int
}

// what names the purpose of a request, so the answer goes to the right place.
type what int

const (
	reqOther what = iota
	reqLoad
	reqPos
	reqRestartPos
	reqTimePos
	reqDropVO
	reqDropDec
	reqBaseVO
	reqBaseDec
)

// request is a request that waits for its answer.
type request struct {
	what what
	// gen is the list generation at the time of the request. mpv answers in
	// order, so an answer to a request of an older list is about that list.
	gen int
	at  time.Time
	// index is the manifest index of a loadfile request.
	index int
}

// loaded is the list that mpv has now.
type loaded struct {
	fallback bool
	playlist *library.ManifestPlaylist
	// order holds the manifest index of each entry of mpv, in the order of mpv.
	// A restart starts the list at the item that comes next, so mpv index 0 is
	// not always item 0, and an item that ended mpv is not in the list.
	order  []int
	single bool
	// tried says that mpv started a file of this list. Before that, an idle mpv
	// is an mpv that did not start yet, not a list that failed.
	tried bool
	// entries maps the playlist_entry_id of mpv to the manifest index.
	entries map[int64]int
	// opened is the manifest index of the file that mpv started last, or -1.
	// openShown says that this file showed its first frame.
	opened    int
	openShown bool
}

// fallbackScreen is the state of the fallback screen.
type fallbackScreen struct {
	checked time.Time
	info    fallback.Info
	// failed says that the last draw did not work. mpv then has no fallback
	// picture, and serviceFallback tries again.
	failed bool
}

// Supervisor owns mpv. One goroutine, Run, changes it. Each exported method
// either reads a value under a lock or sends a message to that goroutine.
type Supervisor struct {
	opt  Options
	proc *launcher
	cmds chan command
	// wake tells the loop that a change flag is set (see mark).
	wake chan struct{}
	// model is the board name (BoardModel). It does not change while the
	// daemon runs.
	model string

	// Fields that the loop owns.
	ipc       *ipcConn
	pending   map[int64]request
	dialSince time.Time
	lastPoll  time.Time
	list      *loaded
	gen       int
	cur       int // the manifest index on the screen, or -1
	itemStart time.Time
	lastPos   float64
	posKnown  bool
	lastMove  time.Time
	// failedAt is when no item of the playlist could play. The fallback screen
	// shows from then until the retry.
	failedAt   time.Time
	resumeFrom *resumePoint
	fb         fallbackScreen
	// crashed holds the paths of the files that ended mpv while it opened
	// them. The list of mpv leaves them out until the playlist changes, because
	// mpv loops the list and would open such a file again at each pass.
	crashed map[string]bool

	expectExit bool
	everUp     bool
	// shownSinceLaunch says that this mpv showed a picture. An mpv that ends
	// before that waits for the launch backoff.
	shownSinceLaunch bool
	launchDelay      time.Duration
	launchNext       time.Time
	// The display probe and its backoff.
	probedAt    time.Time
	probeNext   time.Time
	lastDisplay bool
	waitDelay   time.Duration
	waitLogged  bool
	// motionOff says that the guard switched the moving crossfade off. It stays
	// off until the daemon stops, also through a restart of mpv, or until
	// playback.motion changes. motionFor is the value that the guard belongs to.
	motionOff bool
	motionFor string
	// pendingRestart holds the reason of a restart that arrived while the screen
	// was off. The resume starts a new mpv, which is that restart.
	pendingRestart string
	graceUntil     time.Time
	lastDay        string
	badNightly     string

	// Fields under the lock. Run writes them, and the HTTP handlers read them.
	mu        sync.Mutex
	ladder    ladder
	state     string
	output    string
	hwdec     string
	displayOK bool
	suspended bool
	restarts  int
	lastErr   string
	playing   *manifest.NowPlaying
	// The dropped frames: the counters of mpv and their values at the start of
	// the item.
	vo, dec, baseVO, baseDec int
	// shown says that mpv showed content or the fallback screen one time.
	shown bool
	// heard is the last time at which mpv had answered every request.
	heard time.Time
	// notes is the rate limit of the notes, by event and key.
	notes map[string]noteState
	// playlistDue and displayDue are the changes that the loop did not take
	// yet. They are flags and not messages of the queue: a full queue dropped
	// such a message, and nothing asked again.
	playlistDue, displayDue bool
}

// New makes a Supervisor.
func New(opt Options) *Supervisor {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Tick <= 0 {
		opt.Tick = defaultTick
	}
	if opt.DisplayProbe <= 0 {
		opt.DisplayProbe = displayProbeEvery
	}
	if opt.Manifest == nil {
		opt.Manifest = func() library.PlayerManifest { return library.PlayerManifest{} }
	}
	if opt.Fallback == nil {
		opt.Fallback = func() fallback.Info { return fallback.Info{Name: "PortaPixel"} }
	}
	if opt.Render == nil {
		opt.Render = fallback.Render
	}
	if opt.Display == nil {
		opt.Display = func() DisplaySettings { return DisplaySettings{VideoOutput: OutputAuto} }
	}
	if opt.Watchdog == nil {
		opt.Watchdog = DefaultWatchdog
	}
	if opt.Motion == nil {
		opt.Motion = func() string { return MotionAuto }
	}
	if opt.ModelPath == "" {
		opt.ModelPath = ModelPath
	}
	s := &Supervisor{
		opt:         opt,
		model:       BoardModel(opt.ModelPath),
		proc:        newLauncher(opt.Command, opt.Log),
		cmds:        make(chan command, 8),
		wake:        make(chan struct{}, 1),
		cur:         -1,
		state:       StateStopped,
		displayOK:   true,
		lastDisplay: true,
		waitDelay:   opt.DisplayProbe,
		launchDelay: launchRetryMin,
		notes:       make(map[string]noteState),
		crashed:     make(map[string]bool),
	}
	if s.opt.Reboot == nil {
		// The ladder must always end somewhere. Without a reboot function the end
		// is a line that a person can read.
		s.opt.Reboot = func(reason string) {
			s.log("player.reboot.none", "no reboot function is wired: "+reason)
		}
	}
	if opt.Command.Disabled() {
		s.state = StateDisabled
	}
	return s
}

// Run drives mpv until done is closed. It is the only goroutine that starts,
// stops or talks to mpv.
func (s *Supervisor) Run(done <-chan struct{}) {
	defer s.reportPanic()
	t := time.NewTicker(s.opt.Tick)
	defer t.Stop()
	defer s.shutdown()

	for {
		var msgs <-chan message
		if s.ipc != nil {
			msgs = s.ipc.msgs
		}
		select {
		case <-done:
			return
		case c := <-s.cmds:
			s.handle(c)
		case <-s.wake:
			s.takeChanges()
		case m, ok := <-msgs:
			if !ok {
				// mpv closed the socket. It ends, or it is broken: the exit rule or
				// the silence rule takes it from here.
				s.dropIPC()
				s.dialSince = s.opt.Now()
				continue
			}
			s.onMessage(m, s.opt.Now())
		case <-t.C:
			s.step()
		}
	}
}

// reportPanic writes a panic of the loop into the ops log and then lets it go on.
// The ops log is on ext4, so a person reads the line after the restart of the
// daemon.
func (s *Supervisor) reportPanic() {
	r := recover()
	if r == nil {
		return
	}
	lines := strings.Split(string(debug.Stack()), "\n")
	if len(lines) > 12 {
		lines = lines[:12]
	}
	s.log("player.panic", fmt.Sprintf("%v; %s", r, strings.Join(lines, " | ")))
	panic(r)
}

// Started reports if the device is up for the health marker of an update: mpv
// showed content or the fallback screen, or the device waits for a display, or
// the screen is off. A device with no display plugged in, and a device in its
// night hours, are both healthy (plan section 15).
func (s *Supervisor) Started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shown || s.suspended || s.state == StateWaiting || s.state == StateDisabled
}

// State gives the state of the player for /api/status.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := State{
		Player:           s.state,
		VideoOutput:      s.output,
		DisplayConnected: s.displayOK,
		Suspended:        s.suspended,
		Restarts:         s.restarts,
		LastError:        s.lastErr,
	}
	if s.playing != nil {
		np := *s.playing
		np.DroppedFrames = dropped(s.vo, s.baseVO) + dropped(s.dec, s.baseDec)
		out.NowPlaying = &np
		// "" when no video plays. mpv decodes an image with a video decoder and
		// reports "no" for it, which is not a fact about the video.
		if np.Kind == kindVideo {
			out.Hwdec = s.hwdec
		}
	}
	return out
}

// heardAt gives the last time at which mpv had answered every request.
func (s *Supervisor) heardAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heard
}

// dropped gives the frames that a counter of mpv counted since the start of the
// item. A counter that is lower than its start value started again with the
// file.
func dropped(now, base int) int {
	if now < base {
		return now
	}
	return now - base
}

// Restart asks for a restart of mpv. The remote command uses it. The new mpv
// starts on the item that is on the screen.
func (s *Supervisor) Restart(reason string) error {
	return s.queue(command{kind: cmdRestart, reason: reason})
}

// Suspend stops mpv. The power package calls it when the screen goes off: a
// player with no screen only holds memory, and the DPMS call needs the DRM
// device that mpv holds. It returns when mpv has ended.
func (s *Supervisor) Suspend() error {
	if s.opt.Command.Disabled() {
		s.setSuspended(true)
		return nil
	}
	reply := make(chan error, 1)
	if !s.send(command{kind: cmdSuspend, reply: reply}) {
		return ErrBusy
	}
	select {
	case err := <-reply:
		return err
	case <-time.After(suspendWait):
		return errors.New("the player did not stop in " + suspendWait.String())
	}
}

// Resume starts mpv again after Suspend. The start happens in the loop, after
// this call returns.
func (s *Supervisor) Resume() error {
	if s.opt.Command.Disabled() {
		s.setSuspended(false)
		return nil
	}
	return s.queue(command{kind: cmdResume})
}

// PlaylistChanged tells the supervisor that what must play can be different: a
// schedule change, a library change or a playback setting. The supervisor
// replaces the list of mpv when the manifest is different.
func (s *Supervisor) PlaylistChanged() { s.mark(&s.playlistDue) }

// DisplayChanged tells the supervisor that a setting of the mpv command line is
// different: the rotation, the output mode, the video output or the sound card.
func (s *Supervisor) DisplayChanged() { s.mark(&s.displayDue) }

// mark sets a change flag and wakes the loop. It never blocks, and it never
// loses a change: the loop reads the flags, and two changes before that are one
// change.
func (s *Supervisor) mark(flag *bool) {
	s.mu.Lock()
	*flag = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default: // the loop has a wake already
	}
}

// takeChanges acts on the change flags. It runs in the loop goroutine. A display
// change restarts mpv, and the new mpv loads the manifest when it connects.
func (s *Supervisor) takeChanges() {
	s.mu.Lock()
	playlist, display := s.playlistDue, s.displayDue
	s.playlistDue, s.displayDue = false, false
	s.mu.Unlock()
	if display {
		s.restart("the display settings changed", false, resumeSame)
	}
	if playlist {
		s.playlistChanged(s.opt.Now())
	}
}

// queue puts a message in the queue and reports a full queue as ErrBusy.
func (s *Supervisor) queue(c command) error {
	if !s.send(c) {
		return ErrBusy
	}
	return nil
}

// send puts a message in the queue. It never blocks. It gives false when the
// message was dropped.
func (s *Supervisor) send(c command) bool {
	select {
	case s.cmds <- c:
		return true
	default:
		s.log("player.queue.full", fmt.Sprintf("the message %d was dropped", c.kind))
		return false
	}
}

// handle acts on one message. It runs in the loop goroutine.
func (s *Supervisor) handle(c command) {
	switch c.kind {
	case cmdRestart:
		s.restart(c.reason, false, resumeSame)
	case cmdSuspend:
		if !s.isSuspended() {
			s.setSuspended(true)
			s.stopPlayer()
			s.setState(StateStopped, "")
			s.log("player.suspend", "the screen is off")
		}
		c.reply <- nil
	case cmdResume:
		if s.isSuspended() {
			s.setSuspended(false)
			s.log("player.resume", "the screen is on")
			if s.pendingRestart != "" {
				s.log("player.restart", s.pendingRestart+"; the screen is on again")
				s.pendingRestart = ""
			}
			s.step()
		}
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

	if s.everUp && !s.expectExit && !s.proc.alive() {
		// mpv ended on its own. A display that went away can be the cause, and the
		// last look at the connectors can be old. Look again before the ending
		// counts as a fault: no display is a wait (D44).
		s.probeNext = time.Time{}
	}
	if !s.displayReady(now) {
		return
	}
	if !s.proc.alive() {
		s.handleExit(now)
		return
	}
	if s.ipc == nil {
		s.connect(now)
	}
	s.checkWatchdog(now)
	if s.ipc == nil || !s.proc.alive() {
		return
	}
	s.poll(now)
	s.serviceFallback(now)
	s.checkNightly(now)
}

// displayReady reports if a display is plugged in. No display is a wait state
// and never a watchdog step (D44).
//
// The read of /sys happens on a cadence, not at every tick. While no display is
// connected the cadence doubles up to displayWaitMax, so a television that stays
// in standby for a month costs one read a minute.
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
			s.log("player.display.found", "a display is connected; the player starts")
			s.waitLogged = false
		}
		s.waitDelay = s.opt.DisplayProbe
		return true
	}

	s.probeNext = now.Add(s.waitDelay)
	s.waitDelay = min(2*s.waitDelay, displayWaitMax)
	// Also when mpv ended already: the ending is part of the wait, not a fault.
	s.stopPlayer()
	if !s.waitLogged {
		s.log("player.display.wait", "no display is connected; the device waits and does not count a restart")
		s.waitLogged = true
	}
	s.setState(StateWaiting, "no display is connected")
	return false
}

// handleExit starts mpv, and counts a restart when mpv ended on its own.
func (s *Supervisor) handleExit(now time.Time) {
	if s.everUp && !s.expectExit {
		// One exit is one step on the ladder, so it is handled one time.
		s.expectExit = true
		reason := s.proc.exitReason()
		s.log("player.exit", reason)
		// No mpv runs until the next launch, which can wait for the backoff.
		// The state said "running" for that time.
		s.setState(StateStopped, reason)
		s.drainOpens()
		s.markCrashed()
		s.resumeFrom = s.resumePoint(resumeNext)
		s.dropIPC()
		s.clearList()
		if !s.shownSinceLaunch {
			// mpv ended before it showed anything: a bad option, a busy DRM device.
			// The next try waits, so one fault does not fill the log in seconds.
			s.delayLaunch(now)
		}
		if s.countRestart(now, reason) {
			return // the device reboots
		}
	}
	if now.Before(s.launchNext) {
		return
	}
	s.launch(now)
}

// drainOpens reads the events that mpv wrote before it ended and that the loop
// did not take yet. It keeps only the start and the first frame of a file:
// the exit rule needs to know which file mpv opened last. The rest is about an
// mpv that has ended.
func (s *Supervisor) drainOpens() {
	if s.ipc == nil {
		return
	}
	// mpv has ended, so the socket gives its last lines and then its end.
	limit := time.After(100 * time.Millisecond)
	for {
		select {
		case m, ok := <-s.ipc.msgs:
			if !ok {
				return
			}
			switch m.Event {
			case "start-file":
				s.onStartFile(m)
			case "playback-restart":
				if s.list != nil {
					s.list.openShown = true
				}
			}
		case <-limit:
			return
		}
	}
}

// markCrashed records the file that ended mpv while it opened it: mpv started
// the file and ended before its first frame. mpv loops the list, so it would
// open the file again at each pass, and each exit is a step on the ladder. The
// next lists leave the file out until the playlist changes.
func (s *Supervisor) markCrashed() {
	l := s.list
	if l == nil || l.fallback || l.opened < 0 || l.openShown {
		return
	}
	it := l.playlist.Items[l.opened]
	s.crashed[it.Path] = true
	s.log("player.item.crash", fmt.Sprintf("the item %d (%s) of %s ended mpv while it opened; the player leaves it out "+
		"until the playlist changes", l.opened, it.Name, l.playlist.Name))
}

// launch starts mpv. The IPC connect and the playlist come in the next passes of
// the loop: the loop never waits for mpv.
func (s *Supervisor) launch(now time.Time) {
	set := s.opt.Display()
	output := ResolveOutput(set.VideoOutput, s.opt.DRMRoot)
	s.mu.Lock()
	s.output = output
	s.mu.Unlock()
	s.setState(StateStarting, "")

	err := s.proc.start(Launch{Output: output, Rotation: set.Rotation, VideoMode: set.VideoMode, Model: s.model})
	if err != nil {
		s.setState(StateStopped, err.Error())
		s.log("player.start.fail", fmt.Sprintf("%s; the next try is in %s", err, s.launchDelay))
		s.delayLaunch(now)
		return
	}
	s.everUp = true
	s.expectExit = false
	s.shownSinceLaunch = false
	s.dialSince = now
	s.log("player.start", fmt.Sprintf("pid=%d output=%s", s.proc.pid(), output))
}

// delayLaunch sets the time of the next start and doubles the backoff.
func (s *Supervisor) delayLaunch(now time.Time) {
	s.launchNext = now.Add(s.launchDelay)
	s.launchDelay = min(2*s.launchDelay, launchRetryMax)
}

// connect opens the IPC socket, asks for the events that the supervisor needs,
// and gives mpv the list. A socket that is not there yet is no fault: mpv makes
// it about one second after the start, and the watchdog counts the wait.
func (s *Supervisor) connect(now time.Time) {
	c, err := dialIPC(s.opt.Command.SocketPath())
	if err != nil {
		return
	}
	s.ipc = c
	s.pending = make(map[int64]request)
	s.lastPoll = now
	s.request(now, reqOther, 0, "observe_property", obsIdle, "idle-active")
	s.request(now, reqOther, 0, "observe_property", obsHwdec, "hwdec-current")
	s.request(now, reqOther, 0, "observe_property", obsFault, "user-data/pptr/fault")
	s.request(now, reqOther, 0, "observe_property", obsMoved, "user-data/pptr/moved")
	s.sendMotion(now)
	s.setState(StateRunning, "")
	s.loadContent(now)
}

// sendMotion tells transitions.lua if it may make a moving crossfade: the
// value of playback.motion for this board, unless the guard switched it off.
// A new value of the setting clears the guard. mpv answers the requests in
// order, so the value is there before the first file of the list starts.
func (s *Supervisor) sendMotion(now time.Time) {
	setting := s.opt.Motion()
	if setting != s.motionFor {
		s.motionFor, s.motionOff = setting, false
	}
	value := "no"
	if !s.motionOff && ResolveMotion(setting, runtime.GOARCH, s.model) {
		value = "yes"
	}
	s.request(now, reqOther, 0, "set_property", "user-data/pptr/motion", value)
}

// onMoved takes the result of a moving crossfade. With playback.motion "auto",
// the guard switches the moving crossfade off for the rest of the boot when the
// device is too slow for it: the crossfade then keeps the last frame of the
// video still. "on" is the choice of the owner: the guard writes the fault and
// changes nothing.
func (s *Supervisor) onMoved(data json.RawMessage, now time.Time) {
	m, ok := parseMoved(data)
	if !ok || s.motionOff {
		return
	}
	reason := tooSlow(m)
	if reason == "" {
		return
	}
	if s.motionFor == MotionOn {
		s.note("player.motion.slow", MotionOn, reason+"; playback.motion is on, so the moving crossfade stays on", now)
		return
	}
	s.motionOff = true
	s.log("player.motion.off", reason+"; the crossfade keeps the last frame of a video still until the daemon starts again")
	s.sendMotion(now)
}

// request sends one command to mpv and records it, so that the answer goes to
// the right place and the silence rule can see an answer that does not come. It
// gives false when mpv did not take the command.
func (s *Supervisor) request(now time.Time, w what, index int, args ...any) bool {
	if s.ipc == nil {
		return false
	}
	r := request{what: w, gen: s.gen, at: now, index: index}
	id, err := s.ipc.send(args...)
	if err != nil {
		// mpv does not read its socket. The request stays without an answer, so
		// the silence rule sees it.
		id = -s.ipc.next
	}
	s.pending[id] = r
	return err == nil
}

// oldestRequest gives the time of the oldest request that has no answer.
func (s *Supervisor) oldestRequest() (time.Time, bool) {
	var oldest time.Time
	for _, r := range s.pending {
		if oldest.IsZero() || r.at.Before(oldest) {
			oldest = r.at
		}
	}
	return oldest, !oldest.IsZero()
}

// poll asks mpv for its state. The answers move now_playing, the dropped frames
// and the stall rule; the requests themselves are the IPC heartbeat.
func (s *Supervisor) poll(now time.Time) {
	if now.Sub(s.lastPoll) < pollEvery || len(s.pending) >= maxPending {
		return
	}
	s.lastPoll = now
	s.request(now, reqPos, 0, "get_property", "playlist-pos")
	it, ok := s.current()
	if !ok {
		return
	}
	// Also for an image: an animated GIF, PNG or WebP plays as a video in mpv,
	// and its position moves (see checkWatchdog).
	s.request(now, reqTimePos, 0, "get_property", "time-pos")
	if it.Kind == kindVideo {
		s.request(now, reqDropVO, 0, "get_property", "frame-drop-count")
		s.request(now, reqDropDec, 0, "get_property", "decoder-frame-drop-count")
	}
}

// onMessage takes one message from mpv.
func (s *Supervisor) onMessage(m message, now time.Time) {
	if m.Event == "" {
		s.onReply(m, now)
		return
	}
	switch m.Event {
	case "property-change":
		s.onProperty(m, now)
	case "start-file":
		s.onStartFile(m)
	case "playback-restart":
		s.onPlaybackRestart(now)
	case "end-file":
		s.onEndFile(m, now)
	}
}

// onStartFile takes the start of a file. The file has no frame on the screen
// yet: it can fail, or end mpv, while mpv opens it.
func (s *Supervisor) onStartFile(m message) {
	if s.list == nil {
		return
	}
	s.list.tried = true
	if idx, ok := s.list.entries[m.EntryID]; ok && !s.list.fallback {
		s.list.opened, s.list.openShown = idx, false
	}
}

// onReply takes the answer to a request.
func (s *Supervisor) onReply(m message, now time.Time) {
	r, ok := s.pending[m.RequestID]
	if !ok {
		return
	}
	delete(s.pending, m.RequestID)
	// After the work of the answer, so that a reader of heard sees its result.
	defer func() {
		if len(s.pending) == 0 {
			s.mu.Lock()
			s.heard = now
			s.mu.Unlock()
		}
	}()
	if r.gen != s.gen || s.list == nil {
		return // an answer about a list that mpv no longer has
	}
	success := m.Error == "success"
	switch r.what {
	case reqLoad:
		if !success {
			s.note("player.load.fail", s.listKey(), "mpv refused a file: "+m.Error, now)
			return
		}
		var d struct {
			EntryID int64 `json:"playlist_entry_id"`
		}
		if json.Unmarshal(m.Data, &d) == nil && d.EntryID > 0 {
			s.list.entries[d.EntryID] = r.index
		}
	case reqPos, reqRestartPos:
		var pos int
		if success && json.Unmarshal(m.Data, &pos) == nil {
			s.setIndex(pos, now, r.what == reqRestartPos)
		}
	case reqTimePos:
		var v float64
		known := success && json.Unmarshal(m.Data, &v) == nil
		s.onTimePos(v, known, now)
	case reqDropVO, reqDropDec, reqBaseVO, reqBaseDec:
		var n int
		if success && json.Unmarshal(m.Data, &n) == nil {
			s.setDrops(r.what, n)
		}
	}
}

// setDrops records a value of a dropped frame counter.
func (s *Supervisor) setDrops(w what, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch w {
	case reqDropVO:
		s.vo = n
	case reqDropDec:
		s.dec = n
	case reqBaseVO:
		s.vo, s.baseVO = n, n
	case reqBaseDec:
		s.dec, s.baseDec = n, n
	}
}

// onProperty takes a change of an observed property.
func (s *Supervisor) onProperty(m message, now time.Time) {
	switch m.ID {
	case obsIdle:
		var idle bool
		if json.Unmarshal(m.Data, &idle) == nil && idle {
			s.onIdle(now)
		}
	case obsHwdec:
		var v string // null, when no video plays, gives ""
		json.Unmarshal(m.Data, &v)
		s.mu.Lock()
		s.hwdec = v
		s.mu.Unlock()
	case obsFault:
		// A table with a count and the text: the count makes the same fault
		// again a new value, and mpv sends an event only for a new value.
		var f struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(m.Data, &f) == nil && f.Text != "" {
			s.note("player.transition.fault", s.listKey(), f.Text, now)
		}
	case obsMoved:
		s.onMoved(m.Data, now)
	}
}

// onIdle takes an mpv that has no file. With loop-playlist=inf that happens only
// when no file of the list could play: mpv stops a loop in which every file
// fails. The fallback screen then shows, and the player tries the list again
// later.
func (s *Supervisor) onIdle(now time.Time) {
	if s.list == nil || !s.list.tried {
		return
	}
	if s.list.fallback {
		s.note("player.fallback.fail", "", "mpv could not show the fallback screen; the last lines of mpv: "+
			s.proc.lastOutput(), now)
		return
	}
	s.note("player.playlist.fail", s.listKey(), fmt.Sprintf("no item of the playlist %s could play; the fallback screen shows, "+
		"and the player tries again in %s", s.list.playlist.Name, failedRetry), now)
	s.showFallback(now, "no item of the playlist could play")
	s.failedAt = now
}

// onPlaybackRestart takes the first frame of a file. It is the moment that an
// item is on the screen.
func (s *Supervisor) onPlaybackRestart(now time.Time) {
	s.shownSinceLaunch = true
	s.launchDelay = launchRetryMin
	if s.list != nil {
		s.list.openShown = true
	}
	s.mu.Lock()
	s.shown = true
	s.mu.Unlock()

	s.request(now, reqRestartPos, 0, "get_property", "playlist-pos")
	s.request(now, reqBaseVO, 0, "get_property", "frame-drop-count")
	s.request(now, reqBaseDec, 0, "get_property", "decoder-frame-drop-count")
}

// onEndFile takes the end of a file: a fault of an item, or the item boundary
// that the nightly restart waits for.
func (s *Supervisor) onEndFile(m message, now time.Time) {
	switch m.Reason {
	case "error":
		name := "a file"
		if s.list != nil && !s.list.fallback {
			if idx, ok := s.list.entries[m.EntryID]; ok {
				name = fmt.Sprintf("the item %d (%s) of %s", idx, s.list.playlist.Items[idx].Name, s.list.playlist.Name)
			}
		}
		s.note("player.item.fail", s.listKey(), name+" could not play: "+m.FileError, now)
	case "eof":
		if !s.graceUntil.IsZero() {
			s.graceUntil = time.Time{}
			s.restart("the nightly restart at an item boundary", false, resumeNext)
		}
	}
}

// setIndex takes the playlist position of mpv. restart says that a file just
// showed its first frame, so the timers of the item start again even when the
// item is the same (one image alone, or a video in a loop).
func (s *Supervisor) setIndex(pos int, now time.Time, restart bool) {
	if s.list == nil || s.list.fallback || pos < 0 || pos >= len(s.list.order) {
		return
	}
	items := s.list.playlist.Items
	idx := s.list.order[pos]
	if idx != s.cur {
		s.cur = idx
		it := items[idx]
		s.mu.Lock()
		s.playing = &manifest.NowPlaying{
			Playlist: s.list.playlist.Name,
			Index:    idx,
			Item:     it.Name,
			Kind:     it.Kind,
			Since:    s.local(now),
		}
		s.mu.Unlock()
		restart = true
	}
	if restart {
		s.itemStart = now
		s.lastMove = now
		s.posKnown = false
	}
}

// current gives the item on the screen.
func (s *Supervisor) current() (library.ManifestItem, bool) {
	if s.list == nil || s.list.fallback || s.cur < 0 {
		return library.ManifestItem{}, false
	}
	return s.list.playlist.Items[s.cur], true
}

// playlistChanged replaces the list of mpv when the manifest is different. The
// same manifest changes nothing: a rescan of the library must not start the
// playlist again.
func (s *Supervisor) playlistChanged(now time.Time) {
	if s.ipc == nil {
		return // the connect loads the manifest
	}
	// A playback setting can be playback.motion.
	s.sendMotion(now)
	m := s.opt.Manifest()
	content := hasContent(m)
	switch {
	case !content && s.list != nil && s.list.fallback && s.failedAt.IsZero():
		return // the fallback screen shows already
	case content && s.list != nil && !s.list.fallback && reflect.DeepEqual(*m.Playlist, *s.list.playlist):
		// This list plays already. A draw of the fallback screen that failed in
		// the meantime left the list as it was; its retry must not replace the
		// content later. A list with no item that could play keeps the retry.
		if s.failedAt.IsZero() {
			s.fb.failed = false
		}
		return
	}
	s.loadManifest(m, now)
}

// hasContent reports if the manifest has an item to show. With none, the
// fallback screen shows.
func hasContent(m library.PlayerManifest) bool {
	return m.Playlist != nil && len(m.Playlist.Items) > 0
}

// loadContent gives mpv what must play now.
func (s *Supervisor) loadContent(now time.Time) {
	s.loadManifest(s.opt.Manifest(), now)
}

// loadManifest gives mpv a manifest: the playlist, or the fallback screen.
func (s *Supervisor) loadManifest(m library.PlayerManifest, now time.Time) {
	s.failedAt = time.Time{}
	// The resume point is for the first load after a restart and for no other. A
	// fallback screen uses it up as well: the same playlist, hours later, starts at
	// its first item.
	resume := s.resumeFrom
	s.resumeFrom = nil
	if !hasContent(m) {
		s.showFallback(now, "there is no playable content")
		return
	}
	p := m.Playlist
	n := len(p.Items)
	start := 0
	if resume != nil && resume.name == p.Name && resume.count == n {
		start = resume.index
	} else {
		// Another playlist, or a change of this one: each file gets a new try.
		clear(s.crashed)
	}
	// The list starts at the resume item, and mpv loops it, so the order of the
	// loop stays the order of the playlist. A file that ended mpv is left out.
	var order []int
	for j := range n {
		if idx := (start + j) % n; !s.crashed[p.Items[idx].Path] {
			order = append(order, idx)
		}
	}
	if len(order) == 0 {
		s.showFallback(now, "each item of the playlist ended mpv")
		return
	}
	single := len(order) == 1
	s.fb.failed = false // the content takes the place of the fallback screen
	s.newList(&loaded{playlist: p, order: order, single: single})
	// Ken Burns runs on vo=gpu only. On vo=drm, mpv scales the picture in
	// software at each step of the zoom. That took a quarter of a fast core at 10
	// steps in a second in the lab, and a Pi Zero 2 W has far less. The copy of the
	// screen for the next transition also does not show the zoom there.
	kenBurns := p.KenBurns && s.output == OutputGPU
	if p.KenBurns && !kenBurns {
		s.note("player.kenburns.off", p.Name, p.Name+": Ken Burns is off, because the video output is "+
			s.output+" and not "+OutputGPU, now)
	}
	// mpv loads the first file of a replace at once and the others behind it.
	for j, idx := range order {
		mode := "append"
		if j == 0 {
			mode = "replace"
		}
		next := p.Items[order[(j+1)%len(order)]]
		if !s.request(now, reqLoad, idx, "loadfile", p.Items[idx].Path, mode, -1, fileOptions(p.Items[idx], next, kenBurns, single, s.model)) {
			// The connection is broken, and each next write fails. The silence rule
			// restarts mpv.
			break
		}
	}
	// Each item carries its own transition, so the line names none.
	s.note("player.playlist", p.Name, fmt.Sprintf("%s: %d items", p.Name, len(order)), now)
}

// fileOptions gives the per-file options of one item (ARCHITECTURE 7a). mpv sets
// them when the file starts and puts the global values back when it ends.
//
// next is the item after this one in the list. The script works when this item
// ends, so it takes the transition INTO the next item from the options of this
// item. The list loops, so the last item gets the transition into the first.
//
// kenBurns says that the playlist asks for it and that the output can show it.
// The script then zooms and pans the image while it shows, for the duration.
func fileOptions(it, next library.ManifestItem, kenBurns, single bool, model string) map[string]string {
	kb := 0
	if kenBurns && it.Kind == kindImage && !single {
		kb = it.Duration
	}
	opts := map[string]string{"script-opts": scriptOpts(next.Transition, next.TransitionMS, kb)}
	switch it.Kind {
	case kindImage:
		// One image alone stays on the screen. Nothing needs to change, and a
		// reload of the same picture would only draw a transition into itself.
		duration := strconv.Itoa(it.Duration)
		if single {
			duration = "inf"
		}
		opts["image-display-duration"] = duration
	case kindVideo:
		opts["mute"] = "no"
		if it.Mute {
			opts["mute"] = "yes"
		}
		if it.MaxDuration > 0 {
			opts["end"] = strconv.Itoa(it.MaxDuration)
		}
		if o := DecoderOptions(model); o != "" {
			opts["vd-lavc-o"] = o
		}
		if single {
			// One video alone loops inside its file, with no transition.
			opts["loop-file"] = "inf"
		}
	}
	return opts
}

// scriptOpts gives the options of transitions.lua. The script is the only user
// of script-opts, because a per-file value replaces the whole list. kb is the
// time in seconds that an image shows with Ken Burns, or 0 for no Ken Burns.
func scriptOpts(kind string, ms, kb int) string {
	opts := "pptr-kind=" + kind + ",pptr-ms=" + strconv.Itoa(ms)
	if kb > 0 {
		opts += ",pptr-kb=" + strconv.Itoa(kb)
	}
	return opts
}

// newList starts a new list generation. The answers to the requests of the old
// list do not change the new one.
func (s *Supervisor) newList(l *loaded) {
	s.gen++
	l.entries = make(map[int64]int)
	l.opened = -1
	s.list = l
	s.cur = -1
	s.posKnown = false
	s.mu.Lock()
	s.playing = nil
	s.vo, s.dec, s.baseVO, s.baseDec = 0, 0, 0, 0
	s.mu.Unlock()
}

// clearList forgets the list. mpv has none: it ended or it stops.
//
// The grace of the nightly restart ends too. It waits for an item boundary of
// this mpv, and the next mpv is a new start. A screen-off in the grace minute
// kept the deadline, and the deadline then stopped the new mpv just after the
// screen came on again.
func (s *Supervisor) clearList() {
	s.graceUntil = time.Time{}
	s.list = nil
	s.cur = -1
	s.mu.Lock()
	s.playing = nil
	s.hwdec = ""
	s.mu.Unlock()
}

// showFallback shows the fallback screen (D18).
func (s *Supervisor) showFallback(now time.Time, why string) {
	s.fb = fallbackScreen{}
	if s.drawFallback(now, s.fallbackInfo(now)) {
		s.note("player.fallback", why, "the fallback screen shows: "+why, now)
	}
}

// drawFallback renders the fallback screen into the run directory and gives it
// to mpv. The write is atomic, so mpv never reads half a picture.
//
// The picture holds the clock, so the loop draws it again at each new minute,
// and when the data on it changes (serviceFallback). Its transition is a cut: a
// new minute must not dip to black.
//
// info is the data of the picture. The caller reads it: the check of
// serviceFallback has it already, and a second read is a second report of the
// daemon.
func (s *Supervisor) drawFallback(now time.Time, info fallback.Info) bool {
	set := s.opt.Display()
	w, h := displaySize(s.opt.DRMRoot, set.VideoMode, set.Rotation)
	data, err := s.opt.Render(info, w, h)
	if err == nil {
		err = fsutil.WriteFileAtomic(s.opt.Command.FallbackPath(), data, 0o644)
	}
	if err != nil {
		s.note("player.fallback.fail", "", "the fallback screen could not be drawn: "+err.Error(), now)
		// mpv has no picture to show, and no event will come to say so. The loop
		// asks again after fallbackCheck.
		s.fb.failed = true
		s.fb.checked = now
		return false
	}
	s.fb.failed = false
	s.fb.info = info
	s.fb.checked = now
	s.newList(&loaded{fallback: true})
	s.request(now, reqLoad, -1, "loadfile", s.opt.Command.FallbackPath(), "replace", -1, map[string]string{
		"image-display-duration": "inf",
		"script-opts":            scriptOpts("cut", 0, 0),
	})
	return true
}

// fallbackInfo gives the data of the fallback screen at now.
func (s *Supervisor) fallbackInfo(now time.Time) fallback.Info {
	info := s.opt.Fallback()
	info.Now = s.local(now).Truncate(time.Minute)
	// The step comes from the minute on the screen. It counted from the time
	// that the fallback screen came, so it changed between two minutes and cost
	// 20 more renders in each hour.
	info.Shift = int(info.Now.Unix() / int64(shiftEvery/time.Second))
	return info
}

// serviceFallback keeps the fallback screen current, and tries the playlist
// again after a failure.
func (s *Supervisor) serviceFallback(now time.Time) {
	if s.fb.failed && now.Sub(s.fb.checked) >= fallbackCheck {
		s.drawFallback(now, s.fallbackInfo(now))
	}
	if s.list == nil || !s.list.fallback {
		return
	}
	if !s.failedAt.IsZero() && now.Sub(s.failedAt) >= failedRetry {
		s.loadContent(now)
		return
	}
	if now.Sub(s.fb.checked) < fallbackCheck {
		return
	}
	s.fb.checked = now
	if next := s.fallbackInfo(now); !sameInfo(next, s.fb.info) {
		s.drawFallback(now, next)
	}
}

// sameInfo compares two sets of fallback data.
func sameInfo(a, b fallback.Info) bool {
	return a.Name == b.Name && a.DeviceID == b.DeviceID && a.URL == b.URL &&
		slices.Equal(a.IPs, b.IPs) && a.PairingCode == b.PairingCode && a.Warning == b.Warning &&
		a.Now.Equal(b.Now) && a.Shift == b.Shift
}

// restart stops mpv and lets the next tick start it again. counted says if this
// restart belongs to the watchdog ladder: a restart that a person, a setting or
// the nightly job asked for does not.
//
// It never starts mpv itself. The tick does that. A restart that started mpv
// again from inside itself can come back here through the ladder.
func (s *Supervisor) restart(reason string, counted bool, at resumeAt) {
	now := s.opt.Now()
	s.graceUntil = time.Time{}

	if s.isSuspended() {
		// No mpv runs. The resume starts a new one, which is the restart.
		s.pendingRestart = reason
		s.log("player.restart.hold", reason+"; the screen is off, so the restart waits for it")
		return
	}
	s.log("player.restart", reason)
	s.resumeFrom = s.resumePoint(at)
	s.stopPlayer()
	s.setState(StateStarting, "")
	// A restart is a fresh try, so the launch backoff starts again.
	s.launchNext = time.Time{}
	s.launchDelay = launchRetryMin
	if counted {
		s.countRestart(now, reason)
	}
}

// resumePoint gives the item that the next mpv starts at.
//
// With no list, mpv ended before it had one (it did not connect), so the point
// of the restart before it still waits for its load.
//
// resumeNext starts after the file that mpv opened last, and not after the item
// on the screen: a file that ends mpv while it opens has shown nothing. With no
// item seen at all, the first entry of the list is the file that mpv opened.
func (s *Supervisor) resumePoint(at resumeAt) *resumePoint {
	if s.list == nil {
		return s.resumeFrom
	}
	if at == resumeStart || s.list.fallback {
		return nil
	}
	n := len(s.list.playlist.Items)
	index := s.cur
	switch {
	case at == resumeNext && s.list.opened >= 0:
		index = s.list.opened
	case index < 0:
		index = s.list.order[0]
	}
	if at == resumeNext {
		index = (index + 1) % n
	}
	return &resumePoint{name: s.list.playlist.Name, count: n, index: index}
}

// countRestart records a restart and asks for a reboot at the limit of the
// [watchdog] table. It gives true when it asked for the reboot.
func (s *Supervisor) countRestart(now time.Time, reason string) bool {
	set := s.opt.Watchdog()

	s.mu.Lock()
	count, reboot := s.ladder.restarted(now, set)
	s.restarts = count
	if reboot {
		// The window starts again, so State never reports more restarts than the
		// window holds.
		s.ladder.rebootDone()
		s.restarts = 0
	}
	s.mu.Unlock()

	if !reboot {
		return false
	}
	text := fmt.Sprintf("%d player restarts in %s; the last reason was: %s", count, set.RestartWindow, reason)
	s.log("player.reboot", text)
	s.opt.Reboot(text)
	return true
}

// stopPlayer ends mpv and forgets its state. It returns when the process has
// ended.
func (s *Supervisor) stopPlayer() {
	s.expectExit = true
	s.dropIPC()
	s.proc.stop()
	s.clearList()
}

// dropIPC closes the IPC connection.
func (s *Supervisor) dropIPC() {
	if s.ipc != nil {
		s.ipc.close()
		s.ipc = nil
	}
	s.pending = nil
}

// shutdown ends mpv when the daemon stops.
func (s *Supervisor) shutdown() {
	s.stopPlayer()
	s.setState(StateStopped, "")
}

// checkNightly runs the daily restart of mpv (plan 3.3).
//
// It waits for the end of the item on the screen and forces the restart after
// the grace time. A screen-off schedule that covers the time skips it: the off
// and on cycle is the restart.
func (s *Supervisor) checkNightly(now time.Time) {
	if !s.graceUntil.IsZero() {
		if now.After(s.graceUntil) {
			s.graceUntil = time.Time{}
			s.restart("the nightly restart: the grace time ended", false, resumeNext)
		}
		return
	}
	if s.opt.NightlyRestart == nil {
		return
	}
	value := s.opt.NightlyRestart()
	target, ok := config.ParseClock(value)
	if !ok {
		// A value that is not a time would make the nightly restart go away with no
		// word about it. Say it once for each different value.
		if value != "" && value != s.badNightly {
			s.badNightly = value
			s.log("player.nightly.bad", value+" is not a time in the form HH:MM; there is no nightly restart")
		}
		return
	}
	s.badNightly = ""
	local := s.local(now)
	day := local.Format("2006-01-02")
	if s.lastDay == day {
		return
	}
	minute := local.Hour()*60 + local.Minute()
	// A two minute window, so that a busy loop or a short suspend cannot miss the
	// time.
	if minute < target || minute > target+1 {
		return
	}
	s.lastDay = day

	if s.opt.ScreenOffCovers != nil && s.opt.ScreenOffCovers(local) {
		s.log("player.nightly.skip", "the screen schedule has the screen off at this time")
		return
	}
	if s.list == nil || s.list.fallback || s.list.single {
		// This list has no item boundary to wait for.
		s.restart("the nightly restart", false, resumeSame)
		return
	}
	s.log("player.nightly.grace", "the player has "+graceTimeout.String()+" to reach an item boundary")
	s.graceUntil = now.Add(graceTimeout)
}

// local gives t in the time zone of the device.
func (s *Supervisor) local(t time.Time) time.Time {
	if s.opt.Local == nil {
		return t
	}
	return s.opt.Local(t)
}

func (s *Supervisor) setState(state, reason string) {
	s.mu.Lock()
	s.state = state
	s.lastErr = reason
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

// note writes a line that can come again and again, for example a broken item
// in a loop. The same event and key go in the ops log at most once per
// noteRepeat, and the line says how many times the note came in between. The
// ops log is a file on the flash card, and a device in its steady state writes
// nothing to the flash (D2).
//
// key has few values: the name of a playlist, or a word. The details are never
// part of the key. They name an item, or they hold an error with the name of a
// temporary file, so with them each line was a new note. More notes than
// noteMemory then pushed each other out, and each one went in the log again.
func (s *Supervisor) note(event, key, details string, now time.Time) {
	slog.Debug(event, "details", details)
	s.mu.Lock()
	first, missed := s.rememberNote(event+" "+key, now)
	s.mu.Unlock()
	if !first {
		return
	}
	if missed > 0 {
		details += fmt.Sprintf(" (and %d more times since the last line of this kind)", missed)
	}
	s.log(event, details)
}

// listKey names the list of mpv for a note: the playlist, or "" for the
// fallback screen and for no list.
func (s *Supervisor) listKey() string {
	if s.list == nil || s.list.fallback {
		return ""
	}
	return s.list.playlist.Name
}

// noteState is what the rate limit keeps of one note.
type noteState struct {
	at     time.Time // the last time that it went in the ops log
	missed int       // the times that it came after that and did not go in
}

// rememberNote reports if a note may go in the ops log now, and the times that
// it came since its last line. It records the note. The caller holds the lock.
//
// Each note has its own time, because two faults that take turns would beat a
// memory of one note and write a line at each pass.
func (s *Supervisor) rememberNote(key string, now time.Time) (bool, int) {
	n, seen := s.notes[key]
	if seen && now.Sub(n.at) < noteRepeat {
		n.missed++
		s.notes[key] = n
		return false, 0
	}
	if !seen && len(s.notes) >= noteMemory {
		oldest, at := "", time.Time{}
		for k, v := range s.notes {
			if at.IsZero() || v.at.Before(at) {
				oldest, at = k, v.at
			}
		}
		delete(s.notes, oldest)
	}
	s.notes[key] = noteState{at: now}
	return true, n.missed
}
