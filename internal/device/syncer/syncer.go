package syncer

import (
	"context"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/fleet"
	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/updater"
	"github.com/ethanpil/portapixel/internal/version"
)

// The API paths of the fleet server (ARCHITECTURE section 5).
const (
	EnrollPath    = "/api/v1/enroll"
	ManifestPath  = "/api/v1/manifest"
	HeartbeatPath = "/api/v1/heartbeat"
)

// The poll interval limits. The server may name a value and the TOML holds one;
// both are clamped here. Under 10 seconds a fleet of screens is a load test of
// its own server, and over an hour nobody would call the device managed.
const (
	MinPoll = 10 * time.Second
	MaxPoll = 3600 * time.Second
)

// jitterShare is how much the poll interval moves at random, up or down. A
// hundred screens that boot together must not poll in one second for ever.
const jitterShare = 0.10

// The waits of the pairing flows.
const (
	// claimPoll is how often a device that waits for approval asks again.
	claimPoll = 10 * time.Second
	// claimSlow is the wait after claimPatience. A screen can wait for approval
	// for days, and a request every 10 seconds for a week is noise.
	claimSlow      = 60 * time.Second
	claimPatience  = 10 * time.Minute
	standaloneWait = 30 * time.Second
	// reEnrollGap is the shortest time between two enroll attempts after the
	// server refused the device token. A revoked token must never make a loop.
	reEnrollGap = 5 * time.Minute
	// firstBackoff is the first wait after a network fault. It doubles up to the
	// poll interval.
	firstBackoff = 10 * time.Second
	// faultRepeat is how often the SAME sync fault may go in the ops log. The log is
	// a file on the flash card, so a server that is down for a week must not write a
	// line at every poll (D2).
	faultRepeat = time.Hour
	// settleWait is how long the loop waits before its first pass. The browser and
	// the HTTP server come up first.
	settleWait = 3 * time.Second
	// roundLimit is the longest that one pass may take. A large object download
	// needs minutes; a request that never ends must not hold the loop for ever.
	roundLimit = 30 * time.Minute
	// ackTimeout is how long the acknowledgement before a reboot may take.
	ackTimeout = 5 * time.Second
)

// jsonTimeout is the limit of one enroll, manifest or heartbeat request. It is a
// deadline of each request and not a limit of the HTTP client: the same client
// downloads the objects, and a client timeout covers the body read. A test lowers
// the value.
var jsonTimeout = 30 * time.Second

// maxImmediate is how many passes in a row may ask for no wait at all.
//
// A pass gives 0 when it made progress and the next step is due now: a device that
// paired polls at once. A state that does not move would make that a loop of
// requests with a state file write in each one, which is thousands of writes a
// minute on a flash card. After this many the loop waits MinPoll whatever the pass
// asked for.
const maxImmediate = 3

// Options are the parameters of a Syncer. Everything that touches the world
// outside this package is a field, so a test gives a fake and needs no device.
type Options struct {
	// MediaRoot is the media root. The fleet store is <MediaRoot>/_fleet.
	MediaRoot string
	Log       *opslog.Log
	// Client makes the requests. A nil client gets the client of internal/fleet.
	Client *http.Client
	Now    func() time.Time
	// Jitter gives a number from 0 to 1. A nil function uses math/rand.
	Jitter func() float64

	// Identity is the identity of this device. HardwareID is the full value: this
	// package and the enroll request are the only places that carry it.
	Identity identity.Identity
	// Version is the release that runs. "" uses version.Version.
	Version string

	// Config gives the configuration that the daemon holds now.
	Config func() config.Config
	// State gives the device state. SaveState changes it and writes the file
	// under the lock of the daemon, which owns the state.
	SaveState func(change func(*identity.State)) error
	State     func() identity.State
	// SaveServer writes [server] url and token to portapixel.toml through the
	// normal configuration save path. Pairing and unpairing are the two explicit
	// saves that this package makes.
	SaveServer func(url, token string) error

	// Status builds the health report without the loopback-only fields. The
	// heartbeat carries the same struct that /api/status serves.
	Status func() manifest.Status

	// SetFleetRules hands the schedule of the server to the scheduler.
	// ClearFleetRules gives the rules of the TOML back.
	SetFleetRules   func(defaultPlaylist string, rules []manifest.Rule, screen *manifest.ScreenRule)
	ClearFleetRules func()
	// Rescan reads the media root again and tells the player one time.
	Rescan func()
	// Command runs a local device command: restart-browser, screen-on, screen-off,
	// rescan or reboot.
	Command func(name string) error
	// Update checks the release source of a paired device and applies what it
	// finds. The update command of the server calls it (D28).
	Update func(ctx context.Context) error
	// ResetUpdate forgets the release that the last check offered. Pairing and
	// unpairing both change which source a release may come from (D28).
	ResetUpdate func()

	// CachedSHA, FindSHA and NoteSHA are the hash cache of the library. CachedSHA
	// takes an absolute path.
	CachedSHA func(abs string, size, modNS int64) (string, bool)
	FindSHA   func(sha string) (string, bool)
	NoteSHA   func(abs, sha string)

	// FreeBytes gives the free space of a directory. "" uses fsutil.FreeBytes.
	FreeBytes func(dir string) (uint64, error)
	// Rename moves a directory in one step. A nil function uses os.Rename. A test
	// gives a function that fails, to prove that a half swap cannot happen.
	Rename func(oldPath, newPath string) error
}

// Syncer is the fleet client. It is safe for use by more than one goroutine.
type Syncer struct {
	opt Options

	// pairMu holds one pairing flow at a time: Pair, Unpair and a claim round each
	// read the state, talk to the server and write the state back. Two of them at
	// once could write the token of one server beside the address of another.
	pairMu sync.Mutex

	mu sync.Mutex
	// The report fields of /api/status and of the heartbeat.
	lastSync   time.Time
	lastResult string
	syncError  string
	serverName string
	// pollSeconds is the value of the last manifest, or 0 when the server sent
	// none.
	pollSeconds int
	// release is the release that the server approved, or nil (D28).
	release *manifest.ReleaseRef
	// applied is true after this process handed a fleet schedule to the
	// scheduler. Until then a manifest that did not change must still be applied,
	// because the scheduler of a new process holds no rules.
	applied bool
	// contentKey and rulesKey are the two halves of the manifest that the card
	// holds now. They are cached here so that a poll does not marshal the stored
	// manifest again every time.
	contentKey string
	rulesKey   string
	// gen counts the pairings. A round that started under one pairing must not
	// write anything after an Unpair: the write would bring the fleet state of a
	// server that this device left back to life.
	gen uint64
	// The last fault that went in the ops log, and when. A fault that holds gets one
	// line an hour and not one line at every poll (see failed).
	faultEvent string
	faultText  string
	faultAt    time.Time
	// backoff is the wait after a network fault. It doubles up to the poll
	// interval.
	backoff time.Duration
	// revoked is true after the server refused the device token. revokedToken is
	// the TOML token of that moment: a person who types another token clears the
	// state.
	revoked      bool
	revokedToken string
	// nextEnroll is the earliest time of the next enroll attempt after a refused
	// token.
	nextEnroll time.Time
}

// New makes a Syncer. It touches no file and opens no connection.
func New(opt Options) *Syncer {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Jitter == nil {
		opt.Jitter = rand.Float64
	}
	if opt.Client == nil {
		opt.Client = fleet.NewClient()
	}
	if opt.Version == "" {
		opt.Version = version.Version
	}
	if opt.FreeBytes == nil {
		opt.FreeBytes = fsutil.FreeBytes
	}
	if opt.Rename == nil {
		opt.Rename = os.Rename
	}
	if opt.CachedSHA == nil {
		opt.CachedSHA = func(string, int64, int64) (string, bool) { return "", false }
	}
	if opt.FindSHA == nil {
		opt.FindSHA = func(string) (string, bool) { return "", false }
	}
	if opt.NoteSHA == nil {
		opt.NoteSHA = func(string, string) {}
	}
	return &Syncer{opt: opt, lastResult: "never"}
}

// Report is what the health report needs from the fleet client.
type Report struct {
	Paired     bool
	ServerURL  string
	ServerName string
	LastSync   time.Time
	LastResult string
	SyncError  string
}

// Report gives the fleet fields of /api/status. The daemon hands them to the
// health package, which asks no package for anything (D46 keeps the status call
// cheap).
func (s *Syncer) Report() Report {
	// The address is read before the lock. serverURL asks the daemon for its
	// configuration, and the daemon takes a lock of its own for that: two locks in
	// two orders is a deadlock waiting for the right moment.
	st := s.opt.State()
	address := s.serverURL(st)

	s.mu.Lock()
	defer s.mu.Unlock()
	return Report{
		Paired:     st.Paired(),
		ServerURL:  address,
		ServerName: s.serverName,
		LastSync:   s.lastSync,
		LastResult: s.lastResult,
		SyncError:  s.syncError,
	}
}

// Managed gives the name of the server that manages this device. paired is false
// for a standalone device, and then the local admin owns everything (D48).
func (s *Syncer) Managed() (name string, paired bool) {
	if !s.opt.State().Paired() {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.serverName != "" {
		return s.serverName, true
	}
	return "the fleet server", true
}

// UpdateSource gives the release source of a paired device (D28). ok is false for
// a standalone device, and then the caller asks GitHub.
//
// While a device is paired the GitHub source is off. A paired device installs the
// release that its server approved and nothing else.
//
// ServerURL is the address of the pairing and never a value of the manifest. The
// updater measures the release address against it and sends the device token to that
// host only.
func (s *Syncer) UpdateSource() (updater.Source, bool) {
	st := s.opt.State()
	if !st.Paired() {
		return updater.Source{}, false
	}
	server := s.serverURL(st)

	s.mu.Lock()
	release := s.release
	s.mu.Unlock()

	src := updater.Source{ServerURL: server, Bearer: st.DeviceToken}
	if release != nil {
		src.BaseURL = release.BaseURL
		src.Version = release.Version
	}
	return src, true
}

// serverURL gives the address that this device talks to. The caller must not hold
// the lock: this reads the configuration of the daemon.
func (s *Syncer) serverURL(st identity.State) string {
	if st.ServerURL != "" {
		return st.ServerURL
	}
	base, err := CheckServerURL(s.opt.Config().Server.URL)
	if err != nil {
		return ""
	}
	return base
}

// Restore hands the schedule of the last manifest to the scheduler. The daemon
// calls it at start, before anything serves.
//
// Without it a paired device would use the rules of the TOML from the start of the
// daemon until its first poll answers, and a device with no network would use them
// for ever (plan section 13: the scheduler uses only the fleet rules while the
// device is paired).
func (s *Syncer) Restore() {
	// A staging or a trash directory that a power cut left behind. It is invisible
	// to the library and to the free space arithmetic, and a trash directory holds a
	// whole set of playlists.
	//
	// This runs BEFORE the pairing test. A device that unpaired after a power cut in
	// the middle of a swap never reached this line, so a .trash- directory with a
	// whole set of playlists in it stayed on the card for ever.
	s.sweepLeftovers()

	st := s.opt.State()
	if !st.Paired() || st.Fleet == nil {
		return
	}
	s.mu.Lock()
	s.serverName = st.Fleet.ServerName
	s.pollSeconds = st.Fleet.PollSeconds
	s.release = st.Fleet.Release
	s.contentKey = contentKey(st.Fleet)
	s.rulesKey = rulesKey(st.Fleet)
	s.applied = true
	s.mu.Unlock()

	if s.opt.SetFleetRules != nil {
		s.opt.SetFleetRules(st.Fleet.DefaultPlaylist, st.Fleet.Schedule, st.Fleet.Screen)
	}
	s.log("sync.restore", "the fleet schedule of the last manifest is live again")
}

// Run drives the pairing and the poll loop until done is closed.
func (s *Syncer) Run(done <-chan struct{}) {
	if !wait(done, settleWait) {
		return
	}
	immediate := 0
	for {
		ctx, cancel := fleet.ContextUntil(done, roundLimit)
		asked := s.Once(ctx)
		cancel()

		next := nextWait(asked, &immediate)
		if next != asked {
			s.log("sync.loop.guard", "the fleet client asked for no wait too many times in a row; it waits for the poll interval")
		}
		if !wait(done, next) {
			return
		}
	}
}

// nextWait is the loop guard. A pass that asks for no wait made progress and the
// next step is due now: a device that paired polls at once. A run of them is a state
// that does not move, and that must not become a loop of requests with a state file
// write in each one.
func nextWait(asked time.Duration, immediate *int) time.Duration {
	if asked > 0 {
		*immediate = 0
		return asked
	}
	*immediate++
	if *immediate > maxImmediate {
		*immediate = 0
		return MinPoll
	}
	return asked
}

// Once does one pass and gives the wait before the next one. Run calls it; a test
// calls it directly, so no test needs a real clock.
func (s *Syncer) Once(ctx context.Context) time.Duration {
	cfg := s.opt.Config()
	st := s.opt.State()
	now := s.opt.Now()

	// A person who types another token clears the refusal of the old one.
	s.mu.Lock()
	if s.revoked && s.revokedToken != cfg.Server.Token {
		s.revoked = false
		s.nextEnroll = time.Time{}
	}
	revoked, nextEnroll := s.revoked, s.nextEnroll
	s.mu.Unlock()

	base, urlErr := CheckServerURL(cfg.Server.URL)

	switch {
	case st.Paired():
		return s.pollRound(ctx, base, st)

	case st.ClaimSecret != "":
		// A request that waits for approval. The claim secret is in the state
		// file, so the wait survives a reboot.
		if st.ServerURL != "" {
			return s.claimRound(ctx, st)
		}
		return standaloneWait

	case cfg.Server.URL == "":
		return standaloneWait

	case urlErr != nil:
		s.setError("the server address in portapixel.toml is not usable: " + urlErr.Error())
		return standaloneWait

	case revoked && cfg.Server.Token == "":
		// The server refused the device token and the TOML holds no token to try.
		// The device stays configured and unpaired, and a person repairs it.
		return standaloneWait

	case now.Before(nextEnroll):
		return nextEnroll.Sub(now)

	default:
		_, err := s.Pair(ctx, base, cfg.Server.Token, false)
		switch {
		case Revoked(err):
			// The token of the TOML is not one that this server accepts.
			return s.refuseToken()
		case err != nil:
			return s.failed("sync.enroll.fail", err)
		}
		// A device that paired polls at once. A device that waits for approval
		// asks again on the claim interval.
		if s.opt.State().Paired() {
			return 0
		}
		return claimPoll
	}
}

// failed records a fault and gives the wait before the next attempt. The wait
// doubles and stops at the poll interval: a device with no network must not ask
// every second, and it must not stop asking either.
func (s *Syncer) failed(event string, err error) time.Duration {
	text := err.Error()
	// The ops log gets the FIRST line of a fault and then one line an hour while the
	// same fault holds.
	//
	// A paired device whose server is unreachable used to write one line at every
	// poll, for ever: about 1400 lines a day onto the flash card, and the trim then
	// rewrites the whole file. A device in its steady state writes nothing (D2), and
	// a log that holds one fault a thousand times holds nothing else. The live
	// sentence is in /api/status through Status.SyncError, so a person loses nothing.
	if s.noteFault(event, text) {
		s.log(event, text)
	}
	s.setError(text)

	limit := s.interval()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backoff <= 0 {
		s.backoff = firstBackoff
	} else {
		s.backoff *= 2
	}
	if s.backoff > limit {
		s.backoff = limit
	}
	return s.backoff
}

// interval gives the poll interval: the value of the last manifest when the server
// sent one, else the value of the TOML. It is clamped and it carries the jitter.
func (s *Syncer) interval() time.Duration {
	s.mu.Lock()
	fromServer := s.pollSeconds
	s.mu.Unlock()

	seconds := fromServer
	if seconds <= 0 {
		seconds = s.opt.Config().Server.PollSeconds
	}
	out := time.Duration(seconds) * time.Second
	if out < MinPoll {
		out = MinPoll
	}
	if out > MaxPoll {
		out = MaxPoll
	}
	// The jitter is symmetric: a share of the interval up or down.
	share := (s.opt.Jitter()*2 - 1) * jitterShare
	return out + time.Duration(float64(out)*share)
}

// setError records the sentence that /api/status and the next heartbeat carry.
func (s *Syncer) setError(text string) {
	s.mu.Lock()
	s.syncError = text
	s.lastResult = "error"
	s.mu.Unlock()
}

// setOK records a pass that worked.
func (s *Syncer) setOK() {
	now := s.opt.Now()
	s.mu.Lock()
	said := s.faultText != ""
	s.faultText = ""
	s.faultEvent = ""
	s.faultAt = time.Time{}
	s.lastSync = now
	s.lastResult = "ok"
	s.syncError = ""
	s.backoff = 0
	s.mu.Unlock()
	// One line when a fault that reached the log is over. Without it the log would
	// end at the fault and a person could not tell a screen that recovered from a
	// screen that is still down.
	if said {
		s.log("sync.ok", "the server answers again")
	}
}

// noteFault reports if this fault may go in the ops log now, and records it. A new
// event or a new sentence is always written; the same fault again waits for
// faultRepeat.
func (s *Syncer) noteFault(event, text string) bool {
	now := s.opt.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	same := s.faultEvent == event && s.faultText == text
	if same && now.Sub(s.faultAt) < faultRepeat {
		return false
	}
	s.faultEvent, s.faultText, s.faultAt = event, text, now
	return true
}

// generation gives the number of the pairing that runs now.
func (s *Syncer) generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// stale reports if the pairing changed since a round started. Every write of a
// round asks first: an Unpair that landed in the middle must not be undone by the
// round that it interrupted.
func (s *Syncer) stale(gen uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen != gen
}

// clearFleet forgets everything that belongs to a pairing, in one place.
//
// Unpair, a token that the server revoked and a refused token all end the same
// state, and each one used to reset its own list of fields. A field that one of them
// forgot was a stale value with a new pairing: the release of the old server beside
// the token of the new one.
func (s *Syncer) clearFleet() {
	s.mu.Lock()
	s.gen++
	s.serverName = ""
	s.pollSeconds = 0
	s.release = nil
	s.applied = false
	s.contentKey = ""
	s.rulesKey = ""
	s.syncError = ""
	s.lastResult = "never"
	s.lastSync = time.Time{}
	s.backoff = 0
	s.revoked = false
	s.revokedToken = ""
	s.nextEnroll = time.Time{}
	s.mu.Unlock()

	// The offer of the last release check belongs to the source that made it. A
	// GitHub release must not stay installable after a pairing, and the mirror of an
	// old server must not stay installable after an unpairing (D28).
	if s.opt.ResetUpdate != nil {
		s.opt.ResetUpdate()
	}
}

func (s *Syncer) log(event, details string) {
	if s.opt.Log != nil {
		s.opt.Log.Log(event, details)
	}
}

// wait sleeps for d and reports false when done closed first.
func wait(done <-chan struct{}, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-done:
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return false
	case <-t.C:
		return true
	}
}
