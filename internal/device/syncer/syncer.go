package syncer

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/identity"
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
	// settleWait is how long the loop waits before its first pass. The browser and
	// the HTTP server come up first.
	settleWait = 3 * time.Second
	// roundLimit is the longest that one pass may take. A large object download
	// needs minutes; a request that never ends must not hold the loop for ever.
	roundLimit = 30 * time.Minute
	// ackTimeout is how long the acknowledgement before a reboot may take.
	ackTimeout = 5 * time.Second
	// httpTimeout is the limit of one enroll, manifest or heartbeat request.
	// Object downloads have their own context and are not in it.
	httpTimeout = 30 * time.Second
)

// Options are the parameters of a Syncer. Everything that touches the world
// outside this package is a field, so a test gives a fake and needs no device.
type Options struct {
	// MediaRoot is the media root. The fleet store is <MediaRoot>/_fleet.
	MediaRoot string
	Log       *opslog.Log
	// Client makes the requests. A nil client gets one with a timeout.
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
	State     func() identity.State
	SaveState func(change func(*identity.State)) error
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

	// CachedSHA, FindSHA and NoteSHA are the hash cache of the library.
	CachedSHA func(rel string, size, modNS int64) (string, bool)
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
		opt.Client = &http.Client{Timeout: httpTimeout}
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
	st := s.opt.State()
	s.mu.Lock()
	defer s.mu.Unlock()
	return Report{
		Paired:     st.Paired(),
		ServerURL:  s.serverURL(st),
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
func (s *Syncer) UpdateSource() (updater.Source, bool) {
	st := s.opt.State()
	if !st.Paired() {
		return updater.Source{}, false
	}
	s.mu.Lock()
	release := s.release
	server := s.serverURL(st)
	s.mu.Unlock()

	src := updater.Source{ServerURL: server, Bearer: st.DeviceToken}
	if release != nil {
		src.BaseURL = release.BaseURL
		src.Version = release.Version
	}
	return src, true
}

// serverURL gives the address that this device talks to. The caller holds the
// lock or does not need it: both values are read-only here.
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
	st := s.opt.State()
	if !st.Paired() || st.Fleet == nil {
		return
	}
	s.mu.Lock()
	s.serverName = st.Fleet.ServerName
	s.pollSeconds = st.Fleet.PollSeconds
	s.release = st.Fleet.Release
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
	for {
		ctx, cancel := contextUntil(done, roundLimit)
		next := s.Once(ctx)
		cancel()
		if !wait(done, next) {
			return
		}
	}
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
		case errors.Is(err, ErrRevoked):
			// The token of the TOML is not one that this server accepts.
			return s.refuseToken()
		case err != nil:
			return s.failed("sync.enroll.fail", err)
		}
		// A device that paired polls at once. A device that waits for approval
		// asks again on the claim interval.
		return 0
	}
}

// failed records a fault and gives the wait before the next attempt. The wait
// doubles and stops at the poll interval: a device with no network must not ask
// every second, and it must not stop asking either.
func (s *Syncer) failed(event string, err error) time.Duration {
	s.log(event, err.Error())
	s.setError(err.Error())

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

// interval gives the poll interval: the value of the manifest when the server
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
	s.lastSync = now
	s.lastResult = "ok"
	s.syncError = ""
	s.backoff = 0
	s.mu.Unlock()
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

// contextUntil gives a context that ends when done closes or after the limit.
func contextUntil(done <-chan struct{}, limit time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
