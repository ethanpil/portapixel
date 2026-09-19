package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/browser"
	"github.com/ethanpil/portapixel/internal/device/health"
	"github.com/ethanpil/portapixel/internal/device/httpd"
	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/device/installer"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/device/mdns"
	"github.com/ethanpil/portapixel/internal/device/netcfg"
	"github.com/ethanpil/portapixel/internal/device/power"
	"github.com/ethanpil/portapixel/internal/device/scheduler"
	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/playlist"
	"github.com/ethanpil/portapixel/internal/updater"
	"github.com/ethanpil/portapixel/internal/version"
)

// GitHubRepo is the repository that a standalone device asks for a release (D28).
// The check is unauthenticated, so the repository must be public (D34).
const GitHubRepo = "ethanpil/portapixel"

// updateDir is the directory on the media partition that holds a sideloaded
// release bundle (D52).
const updateDir = "_update"

// shareRoot holds the licence list and the package manifest of the image (D33,
// D50).
const shareRoot = "/usr/share/portapixel"

// opsLogName is the name of the event log in the state directory
// (ARCHITECTURE section 3). The opslog package takes a path, not a directory.
const opsLogName = "ops.log"

// configPoll is how often the daemon looks at the modification time of
// portapixel.toml. A hand edit over SSH or from a laptop must take effect without
// a restart, and five seconds is fast enough for a person (plan section 8).
const configPoll = 5 * time.Second

// shutdownGrace is how long the HTTP server may take to finish its requests.
const shutdownGrace = 5 * time.Second

// updateApplyLimit is the longest that one update may take. A release of 25 MB on a
// slow link needs minutes; a download that never ends must not hold a goroutine for
// ever.
const updateApplyLimit = 20 * time.Minute

// clockPoll is how often the daemon asks the kernel if the clock has a source.
// Nothing in the path of an HTTP request may make a system call that can wait. So
// the daemon samples the answer here. It hands the answer to the report and to the
// scheduler (D40).
const clockPoll = 30 * time.Second

// daemon holds everything that the device runs. It is the wiring: each field is a
// package that owns its own rules.
type daemon struct {
	paths paths
	log   *opslog.Log

	id    identity.Identity
	state identity.State

	lib      *library.Library
	sched    *scheduler.Scheduler
	sup      *browser.Supervisor
	screen   *power.Controller
	announce *mdns.Announcer
	update   *updater.Manager
	install  *installer.Installer
	hub      *httpd.Hub
	// installHub is the progress stream of an install onto a disk. It replays its
	// last event, because the admin UI opens the stream after the POST answered.
	installHub *httpd.Hub
	reporter   *health.Reporter

	secret string
	port   int

	mu          sync.Mutex
	cfg         config.Config
	fromShadow  bool
	cfgWarning  string
	cfgCode     string
	cfgModified time.Time
	// codecs is what the player found out about the video formats of this device.
	// It arrives with the first heartbeat and it does not change (D12).
	codecs       manifest.CodecReport
	lastManifest library.PlayerManifest
	shuffleSeed  uint64
	// hostList is the Host header allowlist. It is built from the network
	// interfaces, so it is cached: every request would otherwise ask the kernel
	// for the interface list.
	hostList []string
	// clockOK is the last answer of the clock probe.
	clockOK bool
	// markerDone is true after the health marker of this release was written.
	markerDone bool
}

// runCommand is the "run" subcommand: the daemon.
func runCommand(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	p := addPathFlags(fs)
	listen := fs.String("listen", "", "the listen address, for example 127.0.0.1:8099. Empty uses [web].port on every address.")
	browserCmd := fs.String("browser-cmd", os.Getenv(BrowserCmdEnv),
		"replace the whole browser command. \"none\" switches the browser off. %u is the URL.")
	// The kiosk account exists on the device only. A development machine cannot
	// run a process as another user, so the default there is empty.
	defaultKiosk := "kiosk"
	if runtime.GOOS != "linux" {
		defaultKiosk = ""
	}
	kioskUser := fs.String("kiosk-user", defaultKiosk, "the unprivileged account that runs the browser. Empty runs it as this user.")
	kioskCache := fs.String("kiosk-cache", "/var/cache/kiosk", "the size capped tmpfs for the browser profile and cache")
	drmRoot := fs.String("drm-root", browser.DefaultDRMRoot, "where the kernel reports the display connectors")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	d, err := newDaemon(*p, *listen, *browserCmd, *kioskUser, *kioskCache, *drmRoot)
	if err != nil {
		return fail("%v", err)
	}
	return d.serve(*listen)
}

// newDaemon builds every part and wires them together. It never fails on
// something that the network or the media partition does: a device must come up
// and report its own faults (D38, plan 3.3).
func newDaemon(p paths, listen, browserCmd, kioskUser, kioskCache, drmRoot string) (*daemon, error) {
	for _, dir := range []string{p.state, p.run, filepath.Join(p.releases, "health")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("make %s: %w", dir, err)
		}
	}

	d := &daemon{
		paths:      p,
		log:        opslog.New(filepath.Join(p.state, opsLogName)),
		hub:        httpd.NewHub(),
		installHub: httpd.NewReplayHub(),
		secret:     httpd.NewSecret(),
	}

	// 1. The identity comes from the hardware at each boot (D21).
	d.id = identity.Derive("/")
	state, err := identity.Resolve(p.state, d.id, d.log)
	if err != nil {
		d.log.Log("identity.state.write.fail", err.Error())
	}
	d.state = state

	// 2. The configuration. Load always gives a configuration that works.
	result := config.Load(p.media, p.state)
	d.cfg = result.Config
	d.cfg.Device.ID = d.id.DeviceID
	d.fromShadow = result.FromShadow
	d.cfgWarning = result.Warning
	d.cfgModified = configMTime(p.media)
	if result.Warning != "" {
		// Load repairs a bad value to its default and keeps the rest of the file
		// (D38). The dashboard needs the code to tell that fault from a hand edit
		// that the daemon refused.
		d.cfgCode = manifest.WarnConfigRepaired
		d.log.Log("config.warning", result.Warning)
	}

	d.log.Log("daemon.start", fmt.Sprintf("version=%s arch=%s device=%s source=%s media=%s",
		version.Version, version.Arch(), d.id.DeviceID, d.id.Source, p.media))

	// 3. The library. The scan is synchronous; the hashing is not.
	d.lib = library.New(library.Options{
		MediaRoot: p.media,
		StateDir:  p.state,
		Log:       d.log,
		Paired:    func() bool { return d.deviceState().Paired() },
		OnChange:  d.libraryChanged,
		OnRename:  d.playlistRenamed,
	})
	d.lib.Rescan()

	// 4. The scheduler. It reads the configuration at each evaluation.
	d.clockOK = scheduler.ClockSynced(time.Now)
	d.sched = scheduler.New(scheduler.Options{
		Config: d.config,
		Now:    d.localNow,
		Synced: d.clockSynced,
		Log:    d.log,
	})
	d.newSeed()
	d.sched.Evaluate()

	// 5. The health report.
	d.reporter = health.New(health.Sources{MediaRoot: p.media, StateDir: p.state})

	// 6. The browser.
	command := browser.CommandConfig{
		Override:   browserCmd,
		KioskUser:  kioskUser,
		CacheDir:   kioskCache,
		RuntimeDir: runtimeDir(kioskUser),
	}
	d.sup = browser.New(browser.Options{
		Command:         command,
		Log:             d.log,
		Now:             d.localNow,
		PlayerURL:       d.playerURL,
		Active:          d.activePlaylist,
		Display:         d.displaySettings,
		Reboot:          d.reboot,
		Grace:           func() { d.hub.Send(httpd.EventGrace, nil) },
		NightlyRestart:  func() string { return d.config().Playback.NightlyRestart },
		ScreenOffCovers: d.sched.ScreenOffCovers,
		DRMRoot:         drmRoot,
	})

	// 7. The screen power (D31). The off order is the display first and the browser
	// second, which is why one package owns both.
	d.screen = power.New(power.Options{
		Method:     func() string { return d.config().Display.PowerMethod },
		ShouldBeOn: d.sched.ScreenShouldBeOn,
		Now:        d.localNow,
		Run:        powerRunner{kiosk: kioskRunner{cmd: command}},
		Browser:    d.sup,
		Log:        d.log,
	})

	// 8. The listen port. The flag wins, so that a development machine can use a
	// port that needs no rights.
	d.port = d.config().Web.Port
	if listen != "" {
		if _, portText, err := net.SplitHostPort(listen); err == nil {
			if n, err := strconv.Atoi(portText); err == nil {
				d.port = n
			}
		}
	}
	d.refreshHosts()

	// 9. The mDNS announcement (D20). It needs the port, so it comes after it.
	d.announce = mdns.New(mdns.Options{
		Name: func() string { return netcfg.MDNSName(d.config(), d.id.DeviceID) },
		IPs:  health.LocalIPs,
		Port: d.port,
		Log:  d.log,
	})

	// 10. The updater. CheckRollback runs now, before anything serves: the health
	// gate has already put the old release back, and the bad release must go in
	// state.json before the first status call (plan section 15).
	d.update = updater.New(updater.Options{
		Root:           p.releases,
		BinaryName:     "portapixeld",
		SideloadDir:    filepath.Join(p.media, updateDir),
		IsBadRelease:   d.isBadRelease,
		MarkBadRelease: d.markBadRelease,
		Restart:        restartService,
		Log:            updater.Logger(d.log),
		Now:            d.localNow,
		Auto:           func() bool { return d.config().Updates.Auto },
		ApplyMinute:    d.applyMinute,
	})
	d.update.CheckRollback()

	// 11. install-to-disk (D54).
	d.install = installer.New(installer.Options{
		MediaRoot: p.media,
		MountRoot: p.run,
		Arch:      version.Arch(),
		Run:       rootRunner{},
		Log:       d.log,
	})
	return d, nil
}

// applyMinute gives the minute of the day at which an automatic update is applied.
// It is the nightly restart time: the device restarts then anyway (D28, D30).
func (d *daemon) applyMinute() (int, bool) {
	return config.ParseClock(d.config().Playback.NightlyRestart)
}

// isBadRelease reports if a release failed its health gate before.
func (d *daemon) isBadRelease(v string) bool {
	return slices.Contains(d.deviceState().BadReleases, v)
}

// markBadRelease records a release that failed its health gate, so that the updater
// never installs it again (plan section 15).
func (d *daemon) markBadRelease(v string) {
	d.mu.Lock()
	d.state.MarkBadRelease(v)
	state := d.state
	d.mu.Unlock()

	if err := state.Save(d.paths.state); err != nil {
		d.log.Log("update.badlist.write.fail", err.Error())
	}
}

// serve starts the HTTP server and the goroutines, and waits for a signal.
func (d *daemon) serve(listen string) int {
	address := listen
	if address == "" {
		address = ":" + strconv.Itoa(d.port)
	}

	server := &http.Server{
		Addr:    address,
		Handler: httpd.New(d.deps()),
		// No read timeout: a media upload of a 1 GB video is a slow request that
		// is not a fault. The write timeout is out for the same reason and for the
		// SSE stream, which never ends.
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		d.log.Log("httpd.listen.fail", err.Error())
		return fail("cannot listen on %s: %v", address, err)
	}
	d.log.Log("httpd.listen", address)
	slog.Info("portapixeld is up", "address", address, "device", d.id.DeviceID, "version", version.Version)
	// The address of the player, with the boot secret taken out. The secret gates
	// the whole player API (D46), and a log line is a line that a person copies
	// into a support message. A developer who needs the true URL reads it from the
	// browser command line.
	slog.Info("the player is at", "url", browser.RedactURL(d.playerURL(-1)))

	done := make(chan struct{})
	var group sync.WaitGroup
	start := func(work func()) {
		group.Add(1)
		go func() {
			defer group.Done()
			work()
		}()
	}

	start(func() { d.sched.Run(done) })
	start(func() { d.sup.Run(done) })
	start(func() { d.lib.HashInBackground(done) })
	start(func() { d.watchSchedule(done) })
	start(func() { d.watchConfig(done) })
	start(func() { d.watchClock(done) })
	start(func() { d.writeHealthMarker(done) })
	// The screen schedule. Start() first, so that a device that boots inside its
	// night hours does not show a picture for the rest of the night.
	d.screen.Start()
	start(func() { d.screen.Run(done) })
	// Nothing below may hold up the start. A device with no network plays what it
	// has (plan 3.3).
	start(func() { d.announce.Run(done) })
	start(func() { d.update.Run(done, d.updateSource()) })

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	code := 0
	select {
	case sig := <-signals:
		d.log.Log("daemon.stop", "signal "+sig.String())
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			d.log.Log("httpd.fail", err.Error())
			code = 1
		}
	}

	// The order matters. The HTTP server drains first, so that no handler can call
	// a worker that has already gone. A POST /api/commands/restart-browser after the
	// supervisor stopped reports success and does nothing.
	//
	// The SSE stream of the player never ends by itself, and Shutdown does not
	// cancel a request context, so the hub ends its streams first. Without that
	// every stop of the daemon cost the whole grace time.
	d.hub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	server.Shutdown(ctx)
	close(done)
	group.Wait()
	return code
}

// deps gives the HTTP layer the functions that it calls.
func (d *daemon) deps() httpd.Deps {
	return httpd.Deps{
		MediaRoot:      d.paths.media,
		Log:            d.log,
		Library:        d.lib,
		Sessions:       httpguard.NewSessions(),
		Limiter:        httpguard.NewLimiter(),
		Hub:            d.hub,
		PlayerSecret:   d.secret,
		Hosts:          d.hosts,
		Password:       func() string { return d.config().Web.Password },
		Status:         d.status,
		Config:         d.configView,
		SaveConfig:     d.saveConfig,
		ActivePlaylist: d.sched.Active,
		PlayerManifest: d.playerManifest,
		Heartbeat:      d.sup.Heartbeat,
		URLItem:        d.urlItem,
		PlayerReady:    d.sup.Ready,
		Command:        d.command,
		Rescan:         d.rescan,
		AdminURL:       d.adminURL,
		SetRootPassword: func(password string) error {
			return setRootPassword(d.paths.state, password)
		},
		SetCodecs:     d.setCodecs,
		CheckUpdate:   d.checkUpdate,
		ApplyUpdate:   d.applyUpdate,
		Disks:         d.install.Disks,
		StartInstall:  d.startInstall,
		InstallEvents: d.installHub,
		ShareRoot:     shareRoot,
	}
}

// updateSource says where this device looks for a release. A paired device will
// take the mirror of its fleet server; until the fleet client exists, every device
// asks GitHub (D28).
func (d *daemon) updateSource() updater.Source {
	return updater.Source{Repo: GitHubRepo}
}

// checkUpdate asks the release source. The handler holds no rule: the updater owns
// the refusals and the state.
func (d *daemon) checkUpdate(ctx context.Context) (updater.Release, error) {
	return d.update.Check(ctx, d.updateSource())
}

// applyUpdate installs the release that the last check found.
//
// The work goes into a goroutine: a download and a signature check take minutes, and
// the browser of the person must not wait. The state is in /api/status, which the
// About page polls.
func (d *daemon) applyUpdate(ctx context.Context) error {
	offered := d.update.Offered()
	if offered == nil {
		return errors.New("check for an update first; this device knows of no release to install")
	}
	if d.update.State().State == manifest.UpdateApplying {
		return updater.ErrBusy
	}
	go func() {
		// A context of its own: the request that started the update is over long
		// before the download ends.
		work, cancel := context.WithTimeout(context.Background(), updateApplyLimit)
		defer cancel()
		d.update.Apply(work, *offered)
	}()
	return nil
}

// startInstall starts an install onto a disk (D54). Every refusal answers here, so
// the progress stream opens only for an install that runs.
func (d *daemon) startInstall(device, confirm string) error {
	if d.install.Busy() {
		return installer.ErrBusy
	}
	if _, _, err := d.install.Check(device, confirm); err != nil {
		return err
	}
	go d.install.Run(context.Background(), device, confirm, d.installHub.Send)
	return nil
}

// setCodecs takes the codec report of the player (D12). It arrives with the first
// heartbeat and it does not change while the daemon runs.
func (d *daemon) setCodecs(report manifest.CodecReport) {
	clean := health.CleanCodecs(report)
	if clean == nil {
		return
	}
	d.mu.Lock()
	first := d.codecs == nil
	d.codecs = clean
	d.mu.Unlock()

	if first {
		names := make([]string, 0, len(clean))
		for name := range clean {
			names = append(names, name)
		}
		sort.Strings(names)
		d.log.Log("player.codecs", "the player reports "+strings.Join(names, " "))
	}
}

// ---------------------------------------------------------------- the state

func (d *daemon) config() config.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg
}

func (d *daemon) deviceState() identity.State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// localNow gives the time in the time zone of the device. The schedules and the
// nightly restart are local times (D40).
//
// scheduler.Location caches the zone object. The browser supervisor asks for the
// time once a second for months, and time.LoadLocation reads files.
func (d *daemon) localNow() time.Time {
	return time.Now().In(scheduler.Location(d.config().Device.Timezone))
}

// clockSynced gives the last answer of the clock probe.
func (d *daemon) clockSynced() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clockOK
}

// watchClock samples the clock probe. It is not in the path of a request: a probe
// there would be a system call, or once a subprocess, on every call of
// /api/status.
func (d *daemon) watchClock(done <-chan struct{}) {
	t := time.NewTicker(clockPoll)
	defer t.Stop()

	for {
		select {
		case <-done:
			return
		case <-t.C:
			synced := scheduler.ClockSynced(time.Now)
			d.mu.Lock()
			changed := synced != d.clockOK
			d.clockOK = synced
			d.mu.Unlock()
			if changed {
				// The scheduler keeps the rules inert until the clock is true, so
				// it must learn about the change at once and not in 20 seconds.
				d.sched.Evaluate()
			}
		}
	}
}

func (d *daemon) configView() httpd.ConfigView {
	d.mu.Lock()
	defer d.mu.Unlock()
	return httpd.ConfigView{
		Config:     d.cfg.Masked(),
		FromShadow: d.fromShadow,
		Warning:    d.cfgWarning,
	}
}

// hosts gives the Host header allowlist (D46). It is the cached list: the list is
// built from the network interfaces, and every request would otherwise ask the
// kernel for them.
func (d *daemon) hosts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hostList
}

// refreshHosts builds the allowlist again. The configuration watcher calls it, so
// a new device name or a new address is in the list within configPoll.
func (d *daemon) refreshHosts() {
	list := health.Hosts(d.config(), d.id.DeviceID, d.port)
	d.mu.Lock()
	d.hostList = list
	d.mu.Unlock()
}

// adminURL is the address that a person types, and the QR code on the fallback
// screen carries. The mDNS name is the address that works from any computer on
// the network.
func (d *daemon) adminURL() string {
	cfg := d.config()
	host := netcfg.MDNSName(cfg, d.id.DeviceID)
	if ips := health.LocalIPs(); host == "" && len(ips) > 0 {
		host = ips[0]
	}
	if d.port != 80 {
		host += ":" + strconv.Itoa(d.port)
	}
	return "http://" + host + "/"
}

// playerURL is the address that the browser opens. The secret in it gates the
// player API (D46).
func (d *daemon) playerURL(resume int) string {
	url := "http://127.0.0.1:" + strconv.Itoa(d.port) + "/player?k=" + d.secret
	if resume >= 0 {
		url += "&resume=" + strconv.Itoa(resume)
	}
	return url
}

func (d *daemon) displaySettings() browser.DisplaySettings {
	cfg := d.config()
	return browser.DisplaySettings{Rotation: cfg.Display.Rotation, VideoMode: cfg.Display.VideoMode}
}

// status builds the health report. The pairing code goes to the device itself
// only (D46).
func (d *daemon) status(loopback bool) manifest.Status {
	cfg := d.config()
	state := d.deviceState()
	browserState := d.sup.State()
	snap := d.lib.Snapshot()

	problems := make([]string, 0, len(snap.Problems))
	for _, p := range snap.Problems {
		if p.Playlist == "" {
			problems = append(problems, p.Message)
			continue
		}
		problems = append(problems, "The playlist \""+p.Playlist+"\": "+p.Message)
	}

	d.mu.Lock()
	fromShadow, warning, code, codecs := d.fromShadow, d.cfgWarning, d.cfgCode, d.codecs
	d.mu.Unlock()

	in := health.Inputs{
		Config:            cfg,
		ConfigFromShadow:  fromShadow,
		ConfigWarning:     warning,
		ConfigWarningCode: code,
		DeviceID:          d.id.DeviceID,
		HardwareChanged:   state.HardwareChanged,
		Codecs:            codecs,
		BrowserState:      browserState.Browser,
		NavigationRung:    browserState.Rung,
		DisplayConnected:  browserState.DisplayConnected,
		// The power controller is what switched the display, so it is the truth
		// about the screen. The browser is suspended as part of a transition, and
		// nothing else suspends it.
		ScreenOn:    d.screen.ScreenOn(),
		NowPlaying:  browserState.NowPlaying,
		Paired:      state.Paired(),
		ServerURL:   cfg.Server.URL,
		ClockSynced: d.clockSynced(),
		Problems:    problems,
		Update:      d.update.State(),
	}
	if loopback {
		in.PairingCode = state.PairingCode
	}
	return d.reporter.Status(in)
}

// ---------------------------------------------------------------- the playlist

// activePlaylist tells the browser what must play now. A playlist of one URL item
// is kiosk mode: the browser parks on the page and the player does not run (D42).
func (d *daemon) activePlaylist() browser.Active {
	name := d.sched.Active()
	out := browser.Active{Playlist: name}
	if p, ok := d.lib.Snapshot().Find(name); ok && p.Kiosk && len(p.Items) == 1 {
		out.KioskURL = p.Items[0].URL
		out.RefreshSeconds = p.Items[0].RefreshSeconds
	}
	return out
}

// playerManifest builds the manifest and remembers it, because the index in
// POST /api/player/url-item counts in the list that the player received.
func (d *daemon) playerManifest() library.PlayerManifest {
	cfg := d.config()
	tier := d.reporter.Tier(cfg)
	name := d.sched.Active()

	d.mu.Lock()
	seed := d.shuffleSeed
	d.mu.Unlock()

	var out library.PlayerManifest
	if p, ok := d.lib.Snapshot().Find(name); ok && !p.Kiosk {
		out = library.BuildManifest(&p, cfg, tier, seed)
	} else {
		out = library.BuildManifest(nil, cfg, tier, seed)
	}

	d.mu.Lock()
	d.lastManifest = out
	d.mu.Unlock()
	return out
}

// urlItem hands the browser to the URL item at index. The index is a position in
// the manifest that the player received.
func (d *daemon) urlItem(index int) (bool, error) {
	d.mu.Lock()
	m := d.lastManifest
	d.mu.Unlock()

	if m.Playlist == nil || index < 0 || index >= len(m.Playlist.Items) {
		return false, fmt.Errorf("there is no item %d in the playlist that you have", index)
	}
	item := m.Playlist.Items[index]
	if item.Kind != playlist.KindURL {
		return false, fmt.Errorf("item %d is not a URL item", index)
	}
	resume := index + 1
	if resume >= len(m.Playlist.Items) {
		resume = 0
	}
	return d.sup.URLItem(item.URL, item.Duration, item.RefreshSeconds, resume), nil
}

// newSeed makes the shuffle order of a playlist that starts now (D17).
func (d *daemon) newSeed() {
	d.mu.Lock()
	d.shuffleSeed = rand.Uint64()
	d.mu.Unlock()
}

// libraryChanged runs after each scan of the media root. The player asks for the
// manifest again, and the browser looks at the kiosk mode: a playlist that became
// one URL item, or stopped being one, changes what the browser must show (D42).
func (d *daemon) libraryChanged() {
	if d.sup == nil {
		return // the first scan, before the browser exists
	}
	d.sup.PlaylistChanged()
	d.hub.Send(httpd.EventPlaylist, map[string]string{"playlist": d.sched.Active()})
}

// watchSchedule turns a playlist change into an event for the player and a look
// at the kiosk mode for the browser.
func (d *daemon) watchSchedule(done <-chan struct{}) {
	changes, cancel := d.sched.Subscribe()
	defer cancel()

	for {
		select {
		case <-done:
			return
		case name := <-changes:
			d.newSeed()
			d.sup.PlaylistChanged()
			d.hub.Send(httpd.EventPlaylist, map[string]string{"playlist": name})
		}
	}
}

// ---------------------------------------------------------------- the configuration

// saveConfig checks, writes and applies a configuration from the API.
func (d *daemon) saveConfig(incoming config.Config) (httpd.Applied, error) {
	old := d.config()
	next := config.MergeMasked(old, incoming)
	// The identity comes from the hardware. An edit of device.id does nothing
	// (D21), so the value that we write is always the true one.
	next.Device.ID = d.id.DeviceID

	if errs := next.Validate(); len(errs) > 0 {
		return httpd.Applied{}, errs
	}
	if err := config.Save(d.paths.media, d.paths.state, next); err != nil {
		d.log.Log("config.save.fail", err.Error())
		return httpd.Applied{}, err
	}

	changes := config.ChangeClass(old, next)
	d.adopt(next, false, "", "")
	d.applyChanges(changes)
	d.log.Log("config.save", fmt.Sprintf("%d changes", len(changes)))
	return httpd.Applied{Applied: highestClass(changes), Changes: changes}, nil
}

// watchConfig picks up a hand edit of portapixel.toml. A person with SSH, or a
// person with the card in a laptop, must not have to restart anything (plan
// section 8).
func (d *daemon) watchConfig(done <-chan struct{}) {
	t := time.NewTicker(configPoll)
	defer t.Stop()

	for {
		select {
		case <-done:
			return
		case <-t.C:
			modified := configMTime(d.paths.media)
			d.mu.Lock()
			same := modified.Equal(d.cfgModified)
			d.mu.Unlock()
			if same {
				// The addresses of the device change without a config edit: a DHCP
				// lease or a cable that a person plugged in. The allowlist is built
				// from the interfaces, so it is built again here and not in every
				// request (D46).
				d.refreshHosts()
				continue
			}
			d.reloadConfig(modified)
		}
	}
}

// reloadConfig reads the file again and applies the changes.
//
// A live reload is not a start. config.Load falls back to the shadow copy and to
// the factory defaults. That is right at boot (D38) and wrong here: one bad
// character takes the device back to the settings of last week, or to DHCP. So the
// reload reads the file on PPMEDIA and nothing else. A file that does not parse
// keeps the configuration that runs. A file that breaks a rule does the same. The
// person then gets a warning that names the field.
func (d *daemon) reloadConfig(modified time.Time) {
	old := d.config()

	d.mu.Lock()
	d.cfgModified = modified
	d.mu.Unlock()

	var next config.Config
	data, err := os.ReadFile(config.MediaPath(d.paths.media))
	if err == nil {
		next, err = config.Parse(data)
	}
	if err == nil {
		next.Device.ID = d.id.DeviceID
		if errs := next.Validate(); len(errs) > 0 {
			err = errs
		}
	}
	if err != nil {
		warning := "portapixel.toml has an error: " + firstLine(err.Error()) +
			"; the device still uses the last good settings"
		d.log.Log("config.reload.bad", err.Error())
		d.mu.Lock()
		d.cfgWarning = warning
		d.cfgCode = manifest.WarnConfigBadEdit
		d.mu.Unlock()
		return
	}

	changes := config.ChangeClass(old, next)
	d.adopt(next, false, "", "")
	d.refreshHosts()
	if len(changes) == 0 {
		return
	}
	d.applyChanges(changes)
	d.log.Log("config.reload", fmt.Sprintf("%d changes from a hand edit", len(changes)))
}

// firstLine keeps the first message of a list of field faults. The warning goes on
// a dashboard card, not in a log.
func firstLine(text string) string {
	if at := strings.IndexAny(text, ";\n"); at > 0 {
		return text[:at]
	}
	return text
}

// adopt takes a new configuration into the daemon. code is the warning code of
// manifest, or "" when there is no warning.
func (d *daemon) adopt(cfg config.Config, fromShadow bool, warning, code string) {
	d.mu.Lock()
	d.cfg = cfg
	d.fromShadow = fromShadow
	d.cfgWarning = warning
	d.cfgCode = code
	d.cfgModified = configMTime(d.paths.media)
	d.mu.Unlock()
}

// applyChanges does the work of a change that takes effect at once, and restarts
// the browser for a change that it takes on its command line.
//
// One playlist event for the whole apply. A change of two playback fields sent
// two events. Two events that arrive together made the player start two play
// loops: double speed and two video decoders.
func (d *daemon) applyChanges(changes []config.Change) {
	browserRestart := false
	tellPlayer := false
	for _, c := range changes {
		switch c.Class {
		case config.Browser:
			browserRestart = true
		case config.Live:
			// The scheduler and the library read the configuration themselves.
			// Only the player needs to be told.
			if strings.HasPrefix(c.Field, "playback.") || c.Field == "schedule" {
				tellPlayer = true
			}
		}
	}
	d.sched.Evaluate()
	if tellPlayer {
		d.hub.Send(httpd.EventPlaylist, map[string]string{"playlist": d.sched.Active()})
	}
	if browserRestart {
		d.sup.DisplayChanged()
	}
}

// playlistRenamed corrects the references to a playlist that took a new name.
//
// The schedule rules and playback.default_playlist name a playlist by its
// directory name. Without this, a rename left every rule with the name of a
// directory that is not there. The rule then matched nothing, the default playlist
// was gone, and the screen showed the fallback picture.
func (d *daemon) playlistRenamed(old, next string) {
	cfg := d.config()
	updated, count := renamePlaylistRefs(cfg, old, next)
	if count == 0 {
		return
	}
	if err := config.Save(d.paths.media, d.paths.state, updated); err != nil {
		d.log.Log("playlist.rename.refs.fail", err.Error())
		return
	}
	d.adopt(updated, false, "", "")
	d.log.Log("playlist.rename.refs", fmt.Sprintf("%d references in portapixel.toml now name %s", count, next))
	d.sched.Evaluate()
	d.sup.PlaylistChanged()
}

// renamePlaylistRefs puts the new name in every place that names the old one. It
// gives the changed configuration and how many references it changed.
func renamePlaylistRefs(cfg config.Config, old, next string) (config.Config, int) {
	count := 0
	if cfg.Playback.DefaultPlaylist == old {
		cfg.Playback.DefaultPlaylist = next
		count++
	}
	// The rules are copied, because the value that came in shares its array with
	// the configuration that runs.
	rules := make([]config.Rule, len(cfg.Schedule))
	copy(rules, cfg.Schedule)
	for i := range rules {
		if rules[i].Playlist == old {
			rules[i].Playlist = next
			count++
		}
	}
	cfg.Schedule = rules
	return cfg, count
}

// highestClass gives the class that the UI must report: a reboot beats a browser
// restart, and a browser restart beats a live change.
func highestClass(changes []config.Change) string {
	out := string(config.Live)
	for _, c := range changes {
		switch c.Class {
		case config.Reboot:
			return string(config.Reboot)
		case config.Browser:
			out = string(config.Browser)
		}
	}
	return out
}

// configMTime gives the modification time of portapixel.toml, or the zero time.
func configMTime(mediaRoot string) time.Time {
	info, err := os.Stat(config.MediaPath(mediaRoot))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// ---------------------------------------------------------------- commands

// command runs a device command from the API or from the fleet queue.
//
// A command that the browser could not take gives browser.ErrBusy, and the API
// answers 503. A command that says "done" and does nothing is worse than an error:
// the person looks at the screen and waits.
func (d *daemon) command(name string) error {
	switch name {
	case "reboot":
		go d.reboot("the admin asked for a reboot")
	case "restart-browser":
		return d.sup.Restart("the admin asked for a browser restart")
	case "screen-on":
		// The power controller owns the order and the manual override: the command
		// holds until the next edge of the screen schedule (D31).
		return d.screen.Set(true, "the admin asked for the screen on")
	case "screen-off":
		return d.screen.Set(false, "the admin asked for the screen off")
	case "rescan":
		d.rescan()
	default:
		return fmt.Errorf("%q is not a command that this device knows", name)
	}
	return nil
}

// rescan reads the media root again and looks for a sideloaded release bundle.
//
// The scan is synchronous: the answer of POST /api/rescan must mean that the scan
// happened. An upload that reports success, beside a playlist list with no file in
// it, is a fault that a person sees.
//
// The look for a bundle is not synchronous. An update takes minutes, and a rescan
// must answer at once (D52).
func (d *daemon) rescan() library.Snapshot {
	snap := d.lib.Rescan()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), updateApplyLimit)
		defer cancel()
		d.update.Sideload(ctx)
	}()
	return snap
}

// reboot reboots the device. The watchdog ladder and the admin UI both call it.
func (d *daemon) reboot(reason string) {
	d.log.Log("device.reboot", reason)
	if runtime.GOOS != "linux" {
		slog.Warn("a reboot was asked for, and this system is not the device", "reason", reason)
		return
	}
	// Give the answer of the HTTP request time to leave the machine.
	time.Sleep(time.Second)
	if err := exec.Command("reboot").Run(); err != nil {
		d.log.Log("device.reboot.fail", err.Error())
	}
}

// setRootPassword changes the root password of the system (D23). It also removes
// the saved hash of the default password, so that the nag goes away at once.
func setRootPassword(stateDir, password string) error {
	if runtime.GOOS != "linux" {
		return errors.New("this system is not the device; the root password cannot be changed here")
	}
	cmd := exec.Command("chpasswd")
	cmd.Stdin = strings.NewReader("root:" + password + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("chpasswd: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	os.Remove(filepath.Join(stateDir, health.RootHashFile))
	return nil
}

// writeHealthMarker writes <releases>/health/<version>.ok when the device is up.
// The update gate watches for this file and rolls back without it (plan section
// 15).
//
// "Up" is the HTTP server and the browser. A device that waits for a display is
// up: a headless boot with the television off must not roll an update back.
//
// A write that fails is tried again at the next tick. One try was wrong. A moment
// of no space, or a partition that is still read-only, leaves no marker. The
// update gate then rolls back a release that works.
func (d *daemon) writeHealthMarker(done <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()

	said := false
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if !d.sup.Started() {
				continue
			}
			path := filepath.Join(d.paths.releases, "health", version.Version+".ok")
			err := os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
			if err == nil {
				d.mu.Lock()
				d.markerDone = true
				d.mu.Unlock()
				return
			}
			if !said {
				// Once. A line at every second for an hour would empty the log.
				said = true
				d.log.Log("health.marker.fail", err.Error()+"; the daemon tries again every second")
			}
		}
	}
}

// runtimeDir gives XDG_RUNTIME_DIR of the browser account. cage and Wayland need
// it, and /run is a tmpfs, so the OpenRC service makes the directory at each boot.
func runtimeDir(kioskUser string) string {
	if kioskUser == "" {
		return ""
	}
	u, err := user.Lookup(kioskUser)
	if err != nil {
		return ""
	}
	return "/run/user/" + u.Uid
}
