package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/audio"
	"github.com/ethanpil/portapixel/internal/device/fallback"
	"github.com/ethanpil/portapixel/internal/device/health"
	"github.com/ethanpil/portapixel/internal/device/httpd"
	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/device/installer"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/device/mdns"
	"github.com/ethanpil/portapixel/internal/device/netcfg"
	"github.com/ethanpil/portapixel/internal/device/player"
	"github.com/ethanpil/portapixel/internal/device/power"
	"github.com/ethanpil/portapixel/internal/device/scheduler"
	"github.com/ethanpil/portapixel/internal/device/syncer"
	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
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

// readIdleLimit is how long a connection may send nothing before the server closes
// it. It is not a limit on a request: a media upload of 1 GB sends bytes all the
// time.
const readIdleLimit = 2 * time.Minute

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
	sup      *player.Supervisor
	screen   *power.Controller
	audio    *audio.Applier
	announce *mdns.Announcer
	update   *updater.Manager
	install  *installer.Installer
	sync     *syncer.Syncer
	// installHub is the progress stream of an install onto a disk. It replays its
	// last event, because the admin UI opens the stream after the POST answered.
	installHub *httpd.Hub
	reporter   *health.Reporter

	port int

	// stateMu holds one change of state.json at a time: the read, the change and
	// the write are one step. See updateState.
	stateMu sync.Mutex

	// cfgWriteMu holds one change of the configuration at a time: a save of the
	// API, a save of the fleet client and the reload of a hand edit. Each one reads
	// the configuration, changes it, writes it and adopts it. Two of them at the
	// same time lose one change. An unpair could then pair the device again.
	cfgWriteMu sync.Mutex

	mu          sync.Mutex
	cfg         config.Config
	fromShadow  bool
	cfgWarning  string
	cfgCode     string
	cfgModified time.Time
	shuffleSeed uint64
	// hostList is the Host header allowlist. It is built from the network
	// interfaces, so it is cached: every request would otherwise ask the kernel
	// for the interface list.
	hostList []string
	// fallbackData keeps the data of the fallback screen. See fallbackInfo.
	fallbackData slowInfo
	// clockOK is the last answer of the clock probe.
	clockOK bool
}

// runCommand is the "run" subcommand: the daemon.
func runCommand(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	p := addPathFlags(fs)
	listen := fs.String("listen", "", "the listen address, for example 127.0.0.1:8099. Empty uses [web].port on every address.")
	playerCmd := fs.String("player-cmd", os.Getenv(PlayerCmdEnv),
		"replace the mpv program. Its own arguments come after the arguments of the daemon. \"none\" switches the player off.")
	// The kiosk account exists on the device only. A development machine cannot
	// run a process as another user, so the default there is empty.
	defaultKiosk := "kiosk"
	if runtime.GOOS != "linux" {
		defaultKiosk = ""
	}
	kioskUser := fs.String("kiosk-user", defaultKiosk, "the unprivileged account that runs mpv. Empty runs it as this user.")
	drmRoot := fs.String("drm-root", player.DefaultDRMRoot, "where the kernel reports the graphics cards and the display connectors")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	d, err := newDaemon(*p, *listen, *playerCmd, *kioskUser, *drmRoot)
	if err != nil {
		return fail("%v", err)
	}
	return d.serve(*listen)
}

// newDaemon builds every part and wires them together. It never fails on
// something that the network or the media partition does: a device must come up
// and report its own faults (D38, plan 3.3).
func newDaemon(p paths, listen, playerCmd, kioskUser, drmRoot string) (*daemon, error) {
	// The health directory under the run directory holds the marker of this boot.
	// The one under the release root holds the .bad markers of the gate, which must
	// survive a reboot (see internal/updater/doc.go).
	for _, dir := range []string{
		p.state,
		p.run,
		filepath.Join(p.run, updater.HealthDir),
		filepath.Join(p.releases, updater.HealthDir),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("make %s: %w", dir, err)
		}
	}

	d := &daemon{
		paths:      p,
		log:        opslog.New(filepath.Join(p.state, opsLogName)),
		installHub: httpd.NewReplayHub(),
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

	// 6. The player: mpv on DRM/KMS, as the kiosk account.
	d.sup = player.New(player.Options{
		Command: player.CommandConfig{
			Override:  playerCmd,
			KioskUser: kioskUser,
			RunDir:    p.run,
		},
		Log: d.log,
		// No Now: the player measures durations with time.Now, which a step of the
		// system clock does not change. The zone is for the wall clock only.
		Local:           d.inZone,
		Manifest:        d.playerManifest,
		Fallback:        d.fallbackInfo,
		Display:         d.displaySettings,
		Reboot:          d.reboot,
		NightlyRestart:  func() string { return d.config().Playback.NightlyRestart },
		Motion:          func() string { return d.config().Playback.Motion },
		Watchdog:        d.watchdogSettings,
		ScreenOffCovers: d.sched.ScreenOffCovers,
		DRMRoot:         drmRoot,
	})

	// 7. The screen power (D31). The off order is the player first and the display
	// second: mpv holds the DRM device, and the DPMS call needs it. One package owns
	// both steps for that reason.
	d.screen = power.New(power.Options{
		Method:     func() string { return d.config().Display.PowerMethod },
		ShouldBeOn: d.sched.ScreenShouldBeOn,
		Now:        d.localNow,
		Run:        rootRunner{},
		Player:     d.sup,
		Log:        d.log,
	})

	// 7a. The sound (D11). amixer talks to the mixer of the kernel, which belongs
	// to root, and the ALSA file is in /etc. Both are the account of the daemon.
	d.audio = audio.New(audio.Options{Run: rootRunner{}, Log: d.log})

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
		// The factory name is unique by construction, so it is the fallback when
		// another device on the network already answers for the chosen name (D20).
		Fallback: func() string { return netcfg.FactoryMDNSName(d.id.DeviceID) },
		IPs:      health.LocalIPs,
		Port:     d.port,
		Log:      d.log,
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
		// A device in a reboot loop stops every automatic action (plan 3.3, rung 4).
		// An update that installs itself on a box that cannot come up would take the
		// one release that works away from it.
		Auto: func() bool {
			if _, loop := d.deviceState().RebootLoop(time.Now()); loop {
				return false
			}
			return d.config().Updates.Auto
		},
		ApplyMinute: d.applyMinute,
		SourceKind:  d.updateSourceKind,
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

	// 12. The fleet client (D24, D25). Restore puts the schedule of the last
	// manifest back before anything serves: a paired device must not use the rules
	// of the TOML for the seconds between the start and its first poll, and a paired
	// device with no network must not use them at all. Nothing else here talks to a
	// network; the poll loop starts in serve.
	d.sync = syncer.New(syncer.Options{
		MediaRoot:  p.media,
		Log:        d.log,
		Now:        d.localNow,
		Identity:   d.id,
		Config:     d.config,
		State:      d.deviceState,
		SaveState:  d.updateState,
		SaveServer: d.saveServer,
		SaveName:   d.saveName,
		// The fleet server holds a device token and its dashboard shows the whole
		// report, so a heartbeat is trusted. It is not loopback, so it never carries
		// the pairing code.
		Status: func() manifest.Status { return d.status(false, true) },
		// Closures and not bound method values. A bound method value takes the
		// receiver now. A block that moves above the one that makes d.sched would
		// bind a nil pointer here, and the panic would come later, on a paired
		// device, inside Restore. A closure asks for the field when it is called.
		SetFleetRules: func(def string, rules []manifest.Rule, screen *manifest.ScreenRule) {
			d.sched.SetFleetRules(def, rules, screen)
		},
		ClearFleetRules: func() { d.sched.ClearFleetRules() },
		Rescan:          func() { d.lib.Rescan() },
		Command:         d.command,
		Update:          d.fleetUpdate,
		ResetUpdate:     func() { d.update.Reset() },
		CachedSHA:       func(abs string, size, modNS int64) (string, bool) { return d.lib.CachedSHA(abs, size, modNS) },
		FindSHA:         func(sha string) (string, bool) { return d.lib.FindSHA(sha) },
		NoteSHA:         func(abs, sha string) { d.lib.NoteSHA(abs, sha) },
	})
	d.sync.Restore()
	return d, nil
}

// updateState changes the device state and writes the file under one lock. The
// fleet client and the updater both change it, so the read, the change and the write
// of one change must not be three steps that another goroutine can come between.
//
// The write is inside the lock. It was outside once, and then two callers could
// write their snapshots in the other order: an Unpair came back after a reboot,
// and the ID of a command that ran was lost, so the server sent the command again
// and the screen rebooted twice. stateMu and not mu, because mu is held by every
// status call and a write to a flash card takes milliseconds.
//
// A change that changes nothing writes nothing. The claim poll of a device that
// waits for approval asks every ten seconds for days, and an fsync each time is
// flash wear for a screen that nobody approved yet.
func (d *daemon) updateState(change func(*identity.State)) error {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()

	d.mu.Lock()
	before, _ := json.Marshal(d.state)
	change(&d.state)
	state := d.state
	after, _ := json.Marshal(d.state)
	d.mu.Unlock()

	if bytes.Equal(before, after) {
		return nil
	}
	return state.Save(d.paths.state)
}

// saveServer writes [server] url and token to portapixel.toml through the normal
// save path. Pairing from the settings page and unpairing are the two explicit
// saves that the fleet client makes (CONTEXT: never write the configuration of the
// user except through an explicit save).
func (d *daemon) saveServer(url, token string) error {
	d.cfgWriteMu.Lock()
	defer d.cfgWriteMu.Unlock()

	next := d.config()
	if next.Server.URL == url && next.Server.Token == token {
		return nil
	}
	next.Server.URL = url
	next.Server.Token = token
	// fromPairing. The two pairing fields are locked while the device is paired, and
	// this IS the write of the pairing. POST /api/pair enrolls first and saves after,
	// so the device was already paired when it reached this line and it refused its
	// own write: the UI showed a failure for a pairing that worked, and the address
	// and the token never reached portapixel.toml. A device that lost its token could
	// then not enroll again by itself. DELETE /api/pair has the same shape.
	_, err := d.writeConfigLocked(next, true)
	return err
}

// errConfigNotClean refuses a rename while the running configuration is not the
// file on PPMEDIA as the person wrote it.
var errConfigNotClean = errors.New("portapixel.toml has a fault, or the device did not take the last edit of it; " +
	"the device does not write the file until a person corrects it")

// saveName is the work of the fleet "rename" command. It writes device.name
// through the normal save path, so the checks of an admin save apply. It is an
// explicit remote action of the admin of the fleet, not a hand edit.
// device.name is not a managed field, so a paired device takes it.
//
// A name that the device already has writes nothing. The server sends a command
// again when an acknowledgement is lost, and each write costs flash.
//
// The save writes the whole file from the running configuration. So it first
// takes a hand edit that the daemon did not read yet. It refuses when the running
// configuration is not the file on PPMEDIA: a shadow copy, the defaults, a
// repaired file, or a hand edit that the daemon refused or took only in part.
// The write would then remove the values of the person (CONTEXT.md, section 5).
// The local Settings page asks before it writes; a remote command cannot ask.
func (d *daemon) saveName(name string) error {
	d.cfgWriteMu.Lock()
	defer d.cfgWriteMu.Unlock()

	next, clean := d.cleanConfigLocked()
	if !clean {
		return errConfigNotClean
	}
	if next.Device.Name == name {
		return nil
	}
	next.Device.Name = name
	_, err := d.writeConfigLocked(next, false)
	return err
}

// cleanConfigLocked takes a hand edit that the daemon did not read yet, and then
// gives the running configuration. clean is false when the running
// configuration is not the file on PPMEDIA as the person wrote it: a shadow
// copy, the defaults, a repaired file, or a hand edit that the daemon refused or
// took only in part. A save writes the whole file, so a save that no person
// asked for in this moment must not write then (CONTEXT.md, section 5). The
// caller holds cfgWriteMu.
func (d *daemon) cleanConfigLocked() (cfg config.Config, clean bool) {
	d.mu.Lock()
	known := d.cfgModified
	d.mu.Unlock()
	if modified := configMTime(d.paths.media); !modified.Equal(known) {
		d.reloadConfigLocked(modified)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg, !d.fromShadow && d.cfgWarning == "" && d.cfgCode == ""
}

// fleetUpdate is the work of the fleet "update" command: check the mirror of the
// server and install what it offers (D28).
func (d *daemon) fleetUpdate(ctx context.Context) error {
	rel, err := d.update.Check(ctx, d.updateSource())
	if err != nil {
		return err
	}
	return d.update.Apply(ctx, rel)
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
	if err := d.updateState(func(st *identity.State) { st.MarkBadRelease(v) }); err != nil {
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
		// No read timeout and no write timeout: a media upload of a 1 GB video is a
		// slow request that is not a fault, and the SSE stream never ends.
		//
		// IdleTimeout takes the place of both for a connection that sends nothing.
		// Such a connection used to hold a goroutine and a file handle for ever. A
		// few hundred of them are the memory of a device with 512 MB.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       readIdleLimit,
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		d.log.Log("httpd.listen.fail", err.Error())
		return fail("cannot listen on %s: %v", address, err)
	}
	d.log.Log("httpd.listen", address)
	slog.Info("portapixeld is up", "address", address, "device", d.id.DeviceID, "version", version.Version)

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
	//
	// The sound is in this group as well: amixer runs a program, and a mixer that
	// does not answer must not keep the first picture waiting (D11).
	start(func() { d.audio.Apply(d.config().Audio) })
	start(func() { d.announce.Run(done) })
	// RunSource and not Run: a paired device follows the release that its server
	// approves, and that value changes while the daemon runs (D28).
	start(func() { d.update.RunSource(done, d.updateSource) })
	start(func() { d.sync.Run(done) })

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
	// a worker that has already gone. A POST /api/commands/restart-player after the
	// supervisor stopped reports success and does nothing.
	//
	// An install stream never ends by itself, and Shutdown does not cancel a
	// request context, so the hub ends its streams first. Without that, a stop of
	// the daemon during an install cost the whole grace time.
	d.installHub.Close()
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
		Hosts:          d.hosts,
		Password:       func() string { return d.config().Web.Password },
		Status:         d.status,
		Config:         d.configView,
		SaveConfig:     d.saveConfig,
		ActivePlaylist: d.sched.Active,
		Command:        d.command,
		Rescan:         d.rescan,
		SetRootPassword: func(password string) error {
			return setRootPassword(d.paths.state, password)
		},
		PairState:     d.sync.State,
		Pair:          d.pair,
		Unpair:        d.sync.Unpair,
		Managed:       d.sync.Managed,
		CheckUpdate:   d.checkUpdate,
		ApplyUpdate:   d.applyUpdate,
		Disks:         d.install.Disks,
		StartInstall:  d.startInstall,
		InstallEvents: d.installHub,
		ShareRoot:     shareRoot,
	}
}

// updateSource says where this device looks for a release. A paired device takes
// the mirror of its fleet server and nothing else; a standalone device asks GitHub
// (D28).
func (d *daemon) updateSource() updater.Source {
	if src, ok := d.sync.UpdateSource(); ok {
		return src
	}
	return updater.Source{Repo: GitHubRepo}
}

// updateSourceKind names the source that this device may install from now. The
// updater refuses a release of any other source: a check and an apply are minutes
// apart, and a device that paired in that time must install what its server approved
// (D28).
func (d *daemon) updateSourceKind() string {
	if _, ok := d.sync.UpdateSource(); ok {
		return "fleet"
	}
	return "github"
}

// checkUpdate asks the release source. The handler holds no rule: the updater owns
// the refusals and the state.
//
// A paired device with no approved release has nothing to offer, and that is not a
// fault. updater.Check answers ErrNoRelease for such a source, so the About page says
// "there is no newer release" and not an error about a source that is missing. The
// rule was here as well before, and the two other callers did not have it: a healthy
// paired device then showed an update fault.
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

// pair is POST /api/pair. The address and the token go into portapixel.toml,
// because the person on the settings page asked for it (D25).
func (d *daemon) pair(ctx context.Context, url, token string) (httpd.PairState, error) {
	return d.sync.Pair(ctx, url, token, true)
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
	// The hub replays its last event to a new subscriber, and the admin UI opens the
	// stream AFTER this call answers. Without this line the first event of install
	// number two is the "done" of install number one.
	d.installHub.Reset()
	go d.install.Run(context.Background(), device, confirm, d.installHub.Send)
	return nil
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

// localNow gives the time in the time zone of the device. The schedules, the
// nightly restart and the clock of the fallback screen are local times (D40).
func (d *daemon) localNow() time.Time {
	return d.inZone(time.Now())
}

// inZone gives t in the time zone of the device. It removes the monotonic reading
// of t, so use it for a time of day and never to measure a duration.
//
// scheduler.Location caches the zone object. The player supervisor converts a time
// once a second for months, and time.LoadLocation reads files.
func (d *daemon) inZone(t time.Time) time.Time {
	return t.In(scheduler.Location(d.config().Device.Timezone))
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
// the network. With no name and no address there is no URL, and the fallback
// screen shows no QR code.
func (d *daemon) adminURL() string {
	cfg := d.config()
	host := netcfg.MDNSName(cfg, d.id.DeviceID)
	if ips := health.LocalIPs(); host == "" && len(ips) > 0 {
		host = ips[0]
	}
	if host == "" {
		return ""
	}
	if d.port != 80 {
		host += ":" + strconv.Itoa(d.port)
	}
	return "http://" + host + "/"
}

// fallbackInfo gives the data of the fallback screen (D18): the name, the ID, the
// admin address, the IP addresses, the pairing code and one warning. The player
// adds the clock and the burn-in step, and draws the screen again when a fact
// changes.
//
// The player loop calls this function, and the loop must never wait for the disk:
// it also serves Suspend, the watchdog and the nightly restart. The report reads
// the media mount, and a stick that fails can hold that call for a long time. So
// the loop waits for fallbackWait at most, and then it gets the last answer.
func (d *daemon) fallbackInfo() fallback.Info {
	return d.fallbackData.get(d.buildFallbackInfo, fallbackWait)
}

// fallbackWait is the longest time that the player loop waits for the data of the
// fallback screen. A normal call takes a few milliseconds.
const fallbackWait = 2 * time.Second

// slowInfo runs a function that can be slow in a goroutine of its own, and it
// starts the function again only after the last call has ended.
type slowInfo struct {
	mu      sync.Mutex
	running bool // a call of build runs now
	last    fallback.Info
}

// get gives the answer of build. A call that starts build waits for it, up to
// wait. When build needs more, get gives the answer of the last call that ended,
// and build goes on in the background.
//
// A call that finds build still running does not wait at all: it gives the last
// answer at once. It waited for the build of an earlier call before, so a stick
// that hangs held the loop for the whole wait at each check of the fallback
// screen.
func (c *slowInfo) get(build func() fallback.Info, wait time.Duration) fallback.Info {
	c.mu.Lock()
	if c.running {
		defer c.mu.Unlock()
		return c.last
	}
	c.running = true
	done := make(chan struct{})
	go func() {
		info := build()
		c.mu.Lock()
		c.last, c.running = info, false
		c.mu.Unlock()
		close(done)
	}()
	c.mu.Unlock()

	select {
	case <-done:
	case <-time.After(wait):
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// buildFallbackInfo reads the report of the device. It is the loopback report:
// the screen is the device itself, so it shows the pairing code (D46).
func (d *daemon) buildFallbackInfo() fallback.Info {
	st := d.status(true, true)
	var lines []string
	if !st.ClockSynced {
		lines = append(lines, "Waiting for the clock.")
	}
	if st.ConfigFromShadow {
		lines = append(lines, "The configuration comes from the backup copy on the device.")
	}
	for _, w := range st.Warnings {
		if w.Message != "" && w.Code != manifest.WarnClockUnsynced && w.Code != manifest.WarnConfigShadow {
			lines = append(lines, w.Message)
		}
	}
	return fallback.Info{
		Name:        st.Name,
		DeviceID:    st.DeviceID,
		URL:         d.adminURL(),
		IPs:         st.IPs,
		PairingCode: st.PairingCode,
		Warning:     strings.Join(lines, "   •   "),
	}
}

// watchdogSettings gives the live thresholds of the recovery ladder (D30). The
// supervisor asks at each check, so a save or a hand edit of [watchdog] takes
// effect with no restart.
func (d *daemon) watchdogSettings() player.WatchdogSettings {
	w := d.config().Watchdog
	return player.WatchdogSettings{
		Enabled:              w.Enabled,
		HeartbeatTimeout:     time.Duration(w.HeartbeatTimeout) * time.Second,
		RestartWindow:        time.Duration(w.RestartWindow) * time.Minute,
		RestartsBeforeReboot: w.RestartsBeforeReboot,
	}
}

func (d *daemon) displaySettings() player.DisplaySettings {
	cfg := d.config()
	return player.DisplaySettings{
		Rotation:    cfg.Display.Rotation,
		VideoMode:   cfg.Display.VideoMode,
		VideoOutput: cfg.Display.VideoOutput,
	}
}

// status builds the health report.
//
// loopback is the device itself, and it is the ONE caller that may see the
// pairing code: the fallback screen shows it (D46). trusted is wider: the device
// itself, an admin with a session, or the fleet server on a heartbeat. An
// untrusted caller gets the report with the secrets taken out (health.redact).
func (d *daemon) status(loopback, trusted bool) manifest.Status {
	cfg := d.config()
	state := d.deviceState()
	playerState := d.sup.State()
	snap := d.lib.Snapshot()
	nowPlayingSHA(playerState.NowPlaying, snap)

	problems := make([]string, 0, len(snap.Problems))
	for _, p := range snap.Problems {
		if p.Playlist == "" {
			problems = append(problems, p.Message)
			continue
		}
		problems = append(problems, "The playlist \""+p.Playlist+"\": "+p.Message)
	}

	d.mu.Lock()
	fromShadow, warning, code := d.fromShadow, d.cfgWarning, d.cfgCode
	d.mu.Unlock()

	fleet := d.sync.Report()

	in := health.Inputs{
		Config:            cfg,
		ConfigFromShadow:  fromShadow,
		ConfigWarning:     warning,
		ConfigWarningCode: code,
		DeviceID:          d.id.DeviceID,
		HardwareChanged:   state.HardwareChanged,
		PlayerState:       playerState.Player,
		VideoOutput:       playerState.VideoOutput,
		Hwdec:             playerState.Hwdec,
		DisplayConnected:  playerState.DisplayConnected,
		// The power controller is what switched the display, so it is the truth
		// about the screen. The player is suspended as part of a transition, and
		// nothing else suspends it.
		ScreenOn:       d.screen.ScreenOn(),
		NowPlaying:     playerState.NowPlaying,
		Paired:         fleet.Paired,
		ServerURL:      fleet.ServerURL,
		LastSync:       fleet.LastSync,
		LastSyncResult: fleet.LastResult,
		SyncError:      fleet.SyncError,
		ServerInsecure: syncer.InsecureURL(fleet.ServerURL),
		ClockSynced:    d.clockSynced(),
		Problems:       problems,
		Update:         d.update.State(),
		Trusted:        trusted,
		MDNSNameTaken:  d.announce.NameTaken(),
	}
	if d.audio != nil {
		in.AudioError = d.audio.Error()
	}
	if n, loop := state.RebootLoop(time.Now()); loop {
		in.RebootLoops = n
	}
	if loopback {
		in.PairingCode = state.PairingCode
	}
	return d.reporter.Status(in)
}

// nowPlayingSHA writes the hash of the file on the screen into the report. The
// fleet server finds its library object with it: the name on the device is the
// object name, which is not the name in the library.
//
// The hash comes from the snapshot, which holds only the hashes that the library
// already knows. A local file that was not hashed yet gets no hash here: the
// status call never reads a file.
func nowPlayingSHA(np *manifest.NowPlaying, snap library.Snapshot) {
	if np == nil {
		return
	}
	np.SHA256 = snap.ItemSHA(np.Playlist, np.Item)
}

// ---------------------------------------------------------------- the playlist

// playerManifest builds the manifest of the active playlist.
func (d *daemon) playerManifest() library.PlayerManifest {
	cfg := d.config()
	name := d.sched.Active()

	d.mu.Lock()
	seed := d.shuffleSeed
	d.mu.Unlock()

	if p, ok := d.lib.Snapshot().Find(name); ok {
		return library.BuildManifest(&p, cfg, seed)
	}
	return library.BuildManifest(nil, cfg, seed)
}

// newSeed makes the shuffle order of a playlist that starts now (D17).
func (d *daemon) newSeed() {
	d.mu.Lock()
	d.shuffleSeed = rand.Uint64()
	d.mu.Unlock()
}

// libraryChanged runs after each scan of the media root. The player compares the
// new manifest with the list of mpv and replaces the list only when it changed.
func (d *daemon) libraryChanged() {
	if d.sup == nil {
		return // the first scan, before the player exists
	}
	d.sup.PlaylistChanged()
}

// watchSchedule gives the player a new playlist when the schedule changes.
func (d *daemon) watchSchedule(done <-chan struct{}) {
	changes, cancel := d.sched.Subscribe()
	defer cancel()

	for {
		select {
		case <-done:
			return
		case name := <-changes:
			d.scheduleChanged(name)
		}
	}
}

// scheduleChanged gives the player the playlist that the schedule names now.
//
// A rename, a fleet manifest and an unpair change the rules first and scan the
// media root after that. The last scan then does not hold the new playlist, and
// the player showed the fallback screen until that scan (seen in the lab at the
// first pairing). So a name that the last scan does not hold makes a scan first.
// An empty name is a paired device with no content: that is the fallback screen.
//
// The seed comes before the scan. The scan tells the player too, and both
// messages must give the same order of a shuffled playlist.
func (d *daemon) scheduleChanged(name string) {
	d.newSeed()
	if name != "" {
		if _, ok := d.lib.Snapshot().Find(name); !ok {
			d.lib.Rescan()
		}
	}
	d.sup.PlaylistChanged()
}

// ---------------------------------------------------------------- the configuration

// saveConfig checks, writes and applies a configuration from the API.
func (d *daemon) saveConfig(incoming config.Config) (httpd.Applied, error) {
	return d.writeConfig(incoming, false)
}

// writeConfig is the one write of the configuration. fromPairing says that the
// caller is /api/pair itself, which is the ONLY caller that may change
// server.url and server.token while the device is paired.
func (d *daemon) writeConfig(incoming config.Config, fromPairing bool) (httpd.Applied, error) {
	d.cfgWriteMu.Lock()
	defer d.cfgWriteMu.Unlock()
	return d.writeConfigLocked(incoming, fromPairing)
}

// writeConfigLocked is writeConfig for a caller that holds cfgWriteMu.
func (d *daemon) writeConfigLocked(incoming config.Config, fromPairing bool) (httpd.Applied, error) {
	old := d.config()
	next := config.MergeMasked(old, incoming)
	// The identity comes from the hardware. An edit of device.id does nothing
	// (D21), so the value that we write is always the true one.
	next.Device.ID = d.id.DeviceID

	if errs := next.Validate(); len(errs) > 0 {
		return httpd.Applied{}, errs
	}

	// The settings boundary of D48. The fleet server owns the default playlist, the
	// schedule and the screen times while the device is paired. Everything else
	// stays with the local admin, so the refusal is per field and not per route.
	//
	// ChangeClass names only the fields that differ, so a normal save that sends the
	// same values back passes.
	changes := config.ChangeClass(old, next)
	if name, paired := d.sync.Managed(); paired {
		for _, c := range changes {
			if syncer.ManagedField(c.Field) {
				return httpd.Applied{}, httpd.ErrManaged{Server: name, Field: c.Field}
			}
			if pairingField(c.Field) && !fromPairing {
				return httpd.Applied{}, httpd.ErrManaged{Server: name, Field: c.Field}
			}
		}
	}

	if err := config.Save(d.paths.media, d.paths.state, next); err != nil {
		d.log.Log("config.save.fail", err.Error())
		return httpd.Applied{}, err
	}

	d.adopt(next, false, "", "")
	d.applyChanges(changes)
	d.log.Log("config.save", fmt.Sprintf("%d changes", len(changes)))
	return httpd.Applied{Applied: highestClass(changes), Changes: changes}, nil
}

// pairingField reports if a configuration field belongs to the pairing.
//
// The two values are written together by POST /api/pair and cleared together by
// DELETE /api/pair, and nothing else may take them apart: a token beside the
// address of another server is a token that the device sends to a stranger. The
// lock is on PUT /api/config; the pairing route writes them through writeConfig
// with fromPairing set.
func pairingField(field string) bool {
	return field == "server.url" || field == "server.token"
}

// keepManaged holds the fields that the fleet server owns at the value that runs.
//
// A hand edit of portapixel.toml is the path here. The other fields of the same edit
// apply as they are: the file of the person is never rewritten, and the status says
// in one sentence which field the device did not take.
func (d *daemon) keepManaged(old, next config.Config) (config.Config, []string) {
	// A test of the reload path builds no fleet client.
	if d.sync == nil {
		return next, nil
	}
	if _, paired := d.sync.Managed(); !paired {
		return next, nil
	}
	var kept []string
	for _, c := range config.ChangeClass(old, next) {
		if !syncer.ManagedField(c.Field) && !pairingField(c.Field) {
			continue
		}
		kept = append(kept, c.Field)
		switch c.Field {
		case "playback.default_playlist":
			next.Playback.DefaultPlaylist = old.Playback.DefaultPlaylist
		case "schedule":
			next.Schedule = old.Schedule
		case "display.on_time":
			next.Display.OnTime = old.Display.OnTime
		case "display.off_time":
			next.Display.OffTime = old.Display.OffTime
		case "display.power_days":
			next.Display.PowerDays = old.Display.PowerDays
		case "server.url":
			next.Server.URL = old.Server.URL
		case "server.token":
			next.Server.Token = old.Server.Token
		}
	}
	return next, kept
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
	d.cfgWriteMu.Lock()
	defer d.cfgWriteMu.Unlock()
	d.reloadConfigLocked(modified)
}

// reloadConfigLocked is reloadConfig for a caller that holds cfgWriteMu.
func (d *daemon) reloadConfigLocked(modified time.Time) {
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

	// A hand edit passes the same boundary as the settings page (D48). The fields
	// that the fleet server owns keep the value that runs, the other fields of the
	// edit apply, and the file of the person is not rewritten.
	next, kept := d.keepManaged(old, next)
	warning, code := "", ""
	if len(kept) > 0 {
		name, _ := d.sync.Managed()
		warning = "the fleet server " + name + " manages " + strings.Join(kept, ", ") +
			"; the value in portapixel.toml is not used"
		code = manifest.WarnConfigManagedIgnored
		d.log.Log("config.reload.managed", warning)
	}

	changes := config.ChangeClass(old, next)
	d.adopt(next, false, warning, code)
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
// the player for a change that mpv takes on its command line.
//
// One playlist message for the whole apply: a change of two playback fields must
// not load the playlist two times.
func (d *daemon) applyChanges(changes []config.Change) {
	playerRestart := false
	tellPlayer := false
	sound := false
	for _, c := range changes {
		if strings.HasPrefix(c.Field, "audio.") {
			sound = true
		}
		switch c.Class {
		case config.Player:
			playerRestart = true
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
		d.sup.PlaylistChanged()
	}
	// The sound comes before the player restart. mpv opens the default ALSA device
	// when it starts, so /etc/asound.conf must hold the new card before the new
	// process runs (D11). Apply writes that file first and sets the mixer after it.
	if sound && d.audio != nil {
		d.audio.Apply(d.config().Audio)
	}
	if playerRestart {
		d.sup.DisplayChanged()
	}
}

// playlistRenamed corrects the references to a playlist that took a new name.
//
// The schedule rules and playback.default_playlist name a playlist by its
// directory name. Without this, a rename left every rule with the name of a
// directory that is not there. The rule then matched nothing, the default playlist
// was gone, and the screen showed the fallback picture.
//
// It is a save of the configuration like the others: it holds cfgWriteMu, it
// takes an unread hand edit first, and it does not write over a file with a
// fault. Before this, it read, changed and wrote with no lock, so a save of the
// settings page at the same time lost one of the two changes.
func (d *daemon) playlistRenamed(old, next string) {
	d.cfgWriteMu.Lock()
	defer d.cfgWriteMu.Unlock()

	cfg, clean := d.cleanConfigLocked()
	updated, count := renamePlaylistRefs(cfg, old, next)
	if count == 0 {
		return
	}
	if !clean {
		d.log.Log("playlist.rename.refs.fail", fmt.Sprintf("%d references in portapixel.toml still name %s: %v",
			count, old, errConfigNotClean))
		return
	}
	if err := config.Save(d.paths.media, d.paths.state, updated); err != nil {
		d.log.Log("playlist.rename.refs.fail", err.Error())
		return
	}
	d.adopt(updated, false, "", "")
	d.log.Log("playlist.rename.refs", fmt.Sprintf("%d references in portapixel.toml now name %s", count, next))
	d.sched.Evaluate()
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

// highestClass gives the class that the UI must report: a reboot beats a player
// restart, and a player restart beats a live change.
func highestClass(changes []config.Change) string {
	out := string(config.Live)
	for _, c := range changes {
		switch c.Class {
		case config.Reboot:
			return string(config.Reboot)
		case config.Player:
			out = string(config.Player)
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
// A command that the player could not take gives player.ErrBusy, and the API
// answers 503. A command that says "done" and does nothing is worse than an error:
// the person looks at the screen and waits.
func (d *daemon) command(name string) error {
	switch name {
	case "reboot":
		go d.reboot("the admin asked for a reboot")
	case "restart-player":
		return d.sup.Restart("the admin asked for a player restart")
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
	// Record the reboot BEFORE it happens. Everything in RAM goes away, so the count
	// of a loop must be on the disk (plan 3.3, rung 4). This is not a steady state
	// write: a device that reboots is not in its steady state.
	if err := d.updateState(func(st *identity.State) { st.MarkReboot(time.Now()) }); err != nil {
		d.log.Log("identity.state.write.fail", err.Error())
	}
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
	// chpasswd reads LINES of "user:password". A newline in the value therefore sets
	// the password of a SECOND account. A value that carried a newline and then
	// "kiosk:letmein" gave a login to the account that runs the player, which is
	// the one isolation boundary of the device (D43). A colon would cut the value at
	// the wrong place.
	if strings.IndexFunc(password, isControlOrColon) >= 0 {
		return errors.New("the root password must hold no control character and no colon")
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

// isControlOrColon reports the characters that chpasswd reads as structure and not
// as part of a password.
func isControlOrColon(r rune) bool {
	return r < 0x20 || r == 0x7f || r == ':'
}

// writeHealthMarker writes <run>/health/<version>.ok one time, when the device is
// up. The update gate watches for this file and rolls back without it (plan
// section 15). updater.MarkerPath names the file, so the daemon, the gate and the
// tests cannot spell it three ways.
//
// "Up" is the HTTP server and the first picture of mpv: content or the fallback
// screen. A device that waits for a display is up: a headless boot with the
// television off must not roll an update back.
//
// The marker lives in the run directory, which is a tmpfs. What it proves is
// "the release that runs NOW came up", so its life is one boot, and a reboot
// clears it by construction. A marker beside the releases on the flash had to be
// removed again by somebody, and it cost the flash a write on a device that in
// its steady state writes nothing at all (D2).
//
// A write that fails is tried again at the next tick. One try was wrong: a moment
// of no space leaves no marker, and the gate then rolls back a release that works.
// The write is atomic, so the gate never reads a file of zero bytes.
//
// Nothing rewrites the marker. The one stale case is a restart of the service with
// no reboot, and the init script removes the file in start_pre, before this daemon
// starts.
func (d *daemon) writeHealthMarker(done <-chan struct{}) {
	path := updater.MarkerPath(d.paths.run, version.Version)
	// The content names the run that wrote the marker. An operator and the gate can
	// then tell one run from another, and the gate puts the line in the ops log.
	body := []byte(fmt.Sprintf("version=%s pid=%d start=%s\n",
		version.Version, os.Getpid(), time.Now().UTC().Format(time.RFC3339)))

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
			if err := fsutil.WriteFileAtomic(path, body, 0o644); err != nil {
				if !said {
					// Once. A line at every second for an hour would empty the log.
					said = true
					d.log.Log("health.marker.fail", err.Error()+"; the daemon tries again every second")
				}
				continue
			}
			return
		}
	}
}
