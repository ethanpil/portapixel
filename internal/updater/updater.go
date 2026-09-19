package updater

import (
	"errors"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/fleet"
	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/version"
)

// The names under the release root. The shell of os/overlay reads the same names,
// so they are constants and not text in a function (see doc.go).
const (
	ReleasesDir  = "releases"
	HealthDir    = "health"
	CurrentLink  = "current"
	PreviousLink = "previous"
	// PendingFile holds the version that must write a health marker. The gate
	// does nothing without it.
	PendingFile = ".swap-pending"
	// SumsName is the checksum file of a release. SigSuffix is the extension of a
	// minisign signature.
	SumsName  = "SHA256SUMS"
	SigSuffix = ".minisig"
	// OKSuffix and BadSuffix are the two markers of the health gate.
	OKSuffix  = ".ok"
	BadSuffix = ".bad"
)

// spaceReserve is the free space that a release must leave behind. A release
// binary is about 20 MB, and the partition also holds the ops log and the state
// file. A device that a release filled is a device that can write nothing.
const spaceReserve = 64 << 20

// smallTimeout is how long one small request of the updater may take: the release
// check, the signature and the checksum file. The release binary is not in it. A
// limit on the whole download aborted every release on a slow link, because
// http.Client.Timeout covers the body read as well (see fleet.NewClient).
//
// It is a var so that a test can lower it.
var smallTimeout = 30 * time.Second

// The refusals. Every one of them happens before the symlink flip.
var (
	ErrNoKey      = errors.New("this build holds no minisign public key, so it cannot install a release")
	ErrBadRelease = errors.New("this release failed its health check once, so the device never installs it again")
	ErrDowngrade  = errors.New("this release is not newer than the one that runs")
	ErrArch       = errors.New("this release is for another processor")
	ErrNoSpace    = errors.New("there is not enough free space for a release")
	ErrBusy       = errors.New("an update is running already")
	ErrNoRelease  = errors.New("there is no newer release")
)

// Options are the parameters of a Manager. Everything that touches the world
// outside this package is a field, so a test gives a fake and needs no network,
// no root rights and no symlink.
type Options struct {
	// Root is the release root, for example /opt/portapixel.
	Root string
	// BinaryName is the name of the program inside a release directory, for
	// example "portapixeld". The release assets are <BinaryName>-<arch>.
	BinaryName string
	// Arch is the processor name of this build. "" uses version.Arch().
	Arch string
	// Running is the release that runs now. "" uses version.Version.
	Running string
	// PublicKey is the minisign key of the project. "" uses version.PublicKey. A
	// test gives its own key.
	PublicKey string

	// Client makes the requests. A nil client gets one with a timeout.
	Client *http.Client
	// SideloadDir is <media>/_update. An empty value switches the sideload off.
	SideloadDir string

	// IsBadRelease reports if a version failed its health gate before.
	IsBadRelease func(version string) bool
	// MarkBadRelease records a version that failed its health gate.
	MarkBadRelease func(version string)
	// Restart restarts the service. On the device it is
	// "rc-service portapixeld restart", detached.
	Restart func() error
	// Flip moves a symlink in one step. "" uses FlipSymlink. A test on Windows
	// gives a function that writes a small file instead.
	Flip func(link, target string) error
	// BinaryVersion gives what a staged binary says about itself. A nil function
	// runs the binary with "version --json". See readBinaryInfo.
	BinaryVersion func(path string) (BinaryInfo, error)
	// FreeBytes gives the free space of a directory. "" uses fsutil.FreeBytes.
	FreeBytes func(dir string) (uint64, error)
	// SourceKind names the release source that this device may install from now:
	// "fleet" for a paired device and "github" for a standalone one. A nil function
	// switches the test off.
	//
	// It exists because a check and an apply are two steps with minutes between
	// them. A device that paired in that time must not install the release that the
	// GitHub check offered: its server approved another version, or none (D28).
	SourceKind func() string

	Log func(event, details string)
	Now func() time.Time
	// Auto reports [updates] auto. A nil function means "off".
	Auto func() bool
	// ApplyMinute gives the minute of the day at which an automatic update is
	// applied, and false when there is no such time. It is the nightly restart
	// time (D28). The caller parses the clock value, so that this package needs no
	// device configuration.
	ApplyMinute func() (int, bool)
	// Tick is the period of the loop. 0 uses one minute.
	Tick time.Duration
}

// Manager holds the update state and does the work. It is safe for use by more
// than one goroutine, and only one update runs at a time.
type Manager struct {
	opt Options

	// sideload holds the one look at the sideload directory. It is not mu: the look
	// itself calls Apply, which takes mu many times.
	sideload sync.Mutex

	mu    sync.Mutex
	state manifest.UpdateState
	busy  bool
	// offered is the release of the last check that found something.
	offered *Release
	// checkMinute is the minute of the day of the daily automatic check. It is
	// random, so a thousand devices do not ask GitHub in the same second.
	checkMinute int
	// lastCheckDay and lastApplyDay stop a second run on one day.
	lastCheckDay string
	lastApplyDay string
}

// New makes a Manager. It touches no file: the caller calls CheckRollback first.
func New(opt Options) *Manager {
	if opt.Arch == "" {
		opt.Arch = version.Arch()
	}
	if opt.Running == "" {
		opt.Running = version.Version
	}
	if opt.PublicKey == "" {
		opt.PublicKey = version.PublicKey
	}
	if opt.Client == nil {
		opt.Client = fleet.NewClient()
	}
	if opt.Flip == nil {
		opt.Flip = FlipSymlink
	}
	if opt.BinaryVersion == nil {
		opt.BinaryVersion = readBinaryInfo
	}
	if opt.FreeBytes == nil {
		opt.FreeBytes = fsutil.FreeBytes
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Log == nil {
		opt.Log = func(string, string) {}
	}
	if opt.Tick <= 0 {
		opt.Tick = time.Minute
	}
	if opt.BinaryName == "" {
		opt.BinaryName = "portapixeld"
	}
	return &Manager{
		opt:         opt,
		state:       manifest.UpdateState{State: manifest.UpdateIdle, Current: opt.Running},
		checkMinute: randomMinute(),
	}
}

// Logger wires an ops log into Options.Log.
func Logger(log *opslog.Log) func(event, details string) {
	if log == nil {
		return func(string, string) {}
	}
	return log.Log
}

// State gives the state for /api/status.
func (m *Manager) State() manifest.UpdateState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Offered gives the release of the last check, or nil.
func (m *Manager) Offered() *Release {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.offered
}

// AssetName is the name of the release asset of this build, for example
// "portapixeld-arm64".
func (m *Manager) AssetName() string { return m.opt.BinaryName + "-" + m.opt.Arch }

// paths under the release root. DownloadDir holds the part file of a download that
// did not finish, so an attempt that failed does not lose the bytes that arrived.
// Its name starts with a full stop, which ValidVersion refuses and prune skips.
const DownloadDir = ".download"

func (m *Manager) releasesDir() string { return filepath.Join(m.opt.Root, ReleasesDir) }
func (m *Manager) healthDir() string   { return filepath.Join(m.opt.Root, HealthDir) }
func (m *Manager) pendingPath() string { return filepath.Join(m.opt.Root, PendingFile) }
func (m *Manager) downloadDir() string { return filepath.Join(m.releasesDir(), DownloadDir) }

// setState records the state and the error text.
func (m *Manager) setState(state, errText string) {
	m.mu.Lock()
	m.state.State = state
	m.state.Error = errText
	m.mu.Unlock()
}

// take marks the manager busy. It gives false when an update runs already: two
// updates in one release root would fight over the staging directory.
func (m *Manager) take() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy {
		return false
	}
	m.busy = true
	return true
}

func (m *Manager) release() {
	m.mu.Lock()
	m.busy = false
	m.mu.Unlock()
}

// randomMinute gives a minute of the day for the automatic check.
func randomMinute() int { return rand.IntN(1440) }

// Reset forgets the release of the last check.
//
// The daemon calls it when the device pairs or unpairs. Without it a release that a
// GitHub check offered stayed installable after the device paired, and the nightly
// apply then installed a version that the fleet server never approved (D28).
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.offered = nil
	m.state = manifest.UpdateState{State: manifest.UpdateIdle, Current: m.opt.Running}
	m.lastCheckDay = ""
	m.lastApplyDay = ""
}
