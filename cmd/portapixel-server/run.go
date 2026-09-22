package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ethanpil/portapixel/internal/httpguard"
	"github.com/ethanpil/portapixel/internal/opslog"
	srvpkg "github.com/ethanpil/portapixel/internal/server"
	"github.com/ethanpil/portapixel/internal/server/admin"
	"github.com/ethanpil/portapixel/internal/server/api"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
	"github.com/ethanpil/portapixel/internal/server/media"
	"github.com/ethanpil/portapixel/internal/server/releases"
)

// shutdownGrace is how long a shutdown waits for the requests that are in flight.
//
// Eight seconds, and not more: Docker sends SIGKILL ten seconds after SIGTERM by
// default, and OpenRC supervise-daemon waits about five. A grace period longer
// than the supervisor allows is a grace period that never finishes, and the
// process dies in the middle of the work that it was protecting. The deploy files
// raise the supervisor limits to fifteen seconds, which leaves room for this one.
const shutdownGrace = 8 * time.Second

// sweepInterval is how often the media store looks for files that no row can
// reach, and how often the pending list loses its old requests.
const sweepInterval = time.Hour

// runCommand is the server.
func runCommand(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dataDir, listen := addCommonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	srv, err := newServer(*dataDir, *listen)
	if err != nil {
		return fail("%v", err)
	}
	defer srv.Close()

	// A signal stops the server. The context ends on the first one, and a second
	// signal ends the process the hard way, because signal.NotifyContext stops
	// listening after the first.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return srv.Serve(ctx)
}

// server holds everything that the process owns.
type server struct {
	cfg     Config
	dataDir string
	db      *db.DB
	log     *opslog.Log
	store   *media.Store
	mirror  *releases.Mirror
	proxies *httpjson.Proxies
	handler http.Handler

	// bg is the life of the process. The mirror and the GitHub lister take it, so
	// that a request that ends does not cancel work that the fleet needs, and a
	// shutdown does cancel it.
	bg       context.Context
	cancelBG context.CancelFunc
	// stopSweep ends the background sweeps.
	stopSweep chan struct{}
	sweepOnce sync.Once
	// bgWait counts the background goroutines. The database closes after the last
	// one ends, so that nothing writes to a pool that is gone.
	bgWait sync.WaitGroup

	// refreshMu makes one refresh of the fleet cache whole. Two callers read the
	// database at the same time on a normal day: an admin write and the report of
	// the mirror. Without this lock the slower reader could publish its older
	// values over the newer ones, and nothing would read the database again until
	// the next admin write.
	refreshMu sync.Mutex
	// mu guards cfg and fleet, which the settings route writes.
	mu sync.RWMutex
	// fleet holds the values that every manifest needs. They come from the
	// settings table and from the releases table, and a poll reads them from here:
	// a fleet of 200 screens would otherwise ask the database for the same three
	// values 200 times a minute.
	fleet api.Fleet
}

// newServer opens the data directory and builds the routes.
func newServer(dataDir, listenFlag string) (*server, error) {
	cfg, warnings, err := LoadConfig(dataDir)
	if err != nil {
		return nil, err
	}
	if listenFlag != "" {
		cfg.Listen = listenFlag
	}

	logFile := filepath.Join(dataDir, "ops.log")
	log := opslog.New(logFile)
	for _, warning := range warnings {
		slog.Warn("server.toml", "detail", warning)
		log.Log("config-warning", warning)
	}

	// The first run has no password. Make one, print it one time, and store its
	// hash. See GeneratePassword for why.
	//
	// The password goes to the standard output and to nothing of ours. The ops log,
	// the database and server.toml never hold it: the file holds the bcrypt hash, and
	// the line below runs one time, because the next start finds that hash.
	//
	// The standard output of a service is not private. Both shipped units keep it:
	// systemd puts it in the journal and the OpenRC script in
	// /var/log/portapixel-server.log, and the Docker log holds it for the life of the
	// container. That is how the operator reads it, and it is also why deploy/README
	// must say "change the password and then clear that line".
	if cfg.AdminPasswordHash == "" {
		password := GeneratePassword()
		hash, err := HashPassword(password)
		if err != nil {
			return nil, err
		}
		cfg.AdminPasswordHash = hash
		if err := SaveConfig(dataDir, cfg); err != nil {
			return nil, err
		}
		fmt.Printf("\n"+
			"=====================================================================\n"+
			" PortaPixel: this is the first run, so the server made a password.\n"+
			"\n"+
			"     user:     admin (there is one account)\n"+
			"     password: %s\n"+
			"\n"+
			" Record it now. The server keeps only its hash, so it cannot print it\n"+
			" again. Change it on the Settings page or with\n"+
			" \"portapixel-server set-password\".\n"+
			"=====================================================================\n\n", password)
		log.Log("first-run", "the server made the admin password and stored its hash")
	}

	database, err := db.Open(filepath.Join(dataDir, DatabaseName))
	if err != nil {
		return nil, err
	}
	store, err := media.New(dataDir)
	if err != nil {
		database.Close()
		return nil, err
	}
	proxies, proxyWarnings := httpjson.NewProxies(cfg.TrustedProxies)
	// The configuration that hurts is the empty list behind a proxy, and the two
	// values below give it away: a public URL of https while this process answers
	// plain HTTP means that something else terminates TLS. Then every caller counts
	// as the proxy: one bad card stops enrollment for the whole fleet, five wrong
	// logins from anywhere lock the admin out, last_ip names the proxy on every row,
	// and the session cookie goes out with no Secure attribute.
	if strings.HasPrefix(cfg.PublicURL, "https://") && cfg.TLSCert == "" && len(cfg.TrustedProxies) == 0 {
		proxyWarnings = append(proxyWarnings,
			"public_url is https and this server answers plain HTTP, so a proxy is in front of it, "+
				"but trusted_proxies is empty: put the address of that proxy in trusted_proxies")
	}
	for _, warning := range proxyWarnings {
		slog.Warn("server.toml", "detail", warning)
		log.Log("config-warning", warning)
	}

	mirror := releases.NewMirror(dataDir, cfg.GitHubRepo)
	// A stopped process can leave a staging directory and a part file behind.
	mirror.CleanStaging()

	bg, cancelBG := context.WithCancel(context.Background())
	s := &server{
		cfg: cfg, dataDir: dataDir, db: database,
		log: log, store: store, mirror: mirror, proxies: proxies,
		bg: bg, cancelBG: cancelBG, stopSweep: make(chan struct{}),
	}
	mirror.Report = func(version, state, errText string) {
		if err := database.SetMirrorState(version, state, errText); err != nil {
			slog.Error("write the mirror state", "version", version, "error", err)
		}
		if errText != "" {
			log.Log("mirror-"+state, version+": "+errText)
		} else {
			log.Log("mirror-"+state, version)
		}
		s.refreshFleet()
	}

	s.refreshFleet()
	s.handler = s.routes()
	return s, nil
}

// Close gives the database back and ends the background work.
//
// The wait is not optional. The mirror writes its state through a callback of this
// type, and the two sweeps read and write as well, so a close that did not wait
// would give one of them a pool that is gone.
func (s *server) Close() {
	s.stopBackground()
	s.bgWait.Wait()
	if s.mirror != nil {
		s.mirror.Wait()
	}
	if s.db != nil {
		s.db.Close()
	}
}

// stopBackground ends the background context and the sweeps one time.
func (s *server) stopBackground() {
	s.sweepOnce.Do(func() {
		if s.cancelBG != nil {
			s.cancelBG()
		}
		close(s.stopSweep)
	})
}

// background1 runs one background job and counts it, so that Close can wait.
func (s *server) background1(job func()) {
	s.bgWait.Add(1)
	go func() {
		defer s.bgWait.Done()
		job()
	}()
}

// config gives the configuration as it is now.
func (s *server) config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// background gives the life of the process.
func (s *server) background() context.Context { return s.bg }

// routes builds the handler. internal/server holds the stack, and the route tests
// call the same constructor.
func (s *server) routes() http.Handler {
	return srvpkg.New(srvpkg.Deps{
		Device: api.Deps{
			DB:             s.db,
			Media:          s.store,
			Mirror:         s.mirror,
			Log:            s.log,
			Limiter:        httpguard.NewLimiter(),
			PendingLimiter: httpguard.NewPendingLimiter(),
			ClientIP:       s.proxies.ClientIP,
			Fleet:          s.readFleet,
		},
		Admin: admin.Deps{
			DB:            s.db,
			Media:         s.store,
			Mirror:        s.mirror,
			Log:           s.log,
			Sessions:      s.sessions(),
			Limiter:       httpguard.NewLimiter(),
			ClientIP:      s.proxies.ClientIP,
			DataDir:       s.dataDir,
			StartedAt:     time.Now(),
			Background:    s.background,
			CheckPassword: s.checkPassword,
			SetPassword:   s.setPassword,
			Settings:      s.settings,
			SaveSettings:  s.saveSettings,
			FleetChanged:  s.refreshFleet,
		},
		Hosts:   s.hosts,
		IsHTTPS: s.proxies.ClientIsHTTPS,
	})
}

// sessions makes the session store of the admin UI.
//
// The cookie takes the Secure attribute when the caller reached the server over
// TLS, either directly or through a proxy that we trust. The flag cannot be a
// constant: a homelab server on a closed network answers plain HTTP by design, and
// a cookie with Secure would never come back from it.
//
// The answer reads the REQUEST and not the configuration. A public URL of https
// with a request that really arrived over plain HTTP used to give a Secure cookie
// as well. The login then answered 200, the browser threw the cookie away, and the
// admin was in a login loop with no message anywhere. Over plain HTTP the flag
// protects nothing that is not already in the clear, so the request is the only
// input that can be right.
func (s *server) sessions() *httpguard.Sessions {
	sessions := httpguard.NewSessions()
	sessions.Secure = s.proxies.ClientIsHTTPS
	return sessions
}

// Serve listens and answers until the context ends.
func (s *server) Serve(ctx context.Context) int {
	cfg := s.config()
	httpSrv := &http.Server{
		Addr:    cfg.Listen,
		Handler: s.handler,
		// There is no ReadTimeout and no WriteTimeout. One would cut a 1 GB media
		// upload and a Range download of the same file. ReadHeaderTimeout is the
		// one that stops a connection that sends no headers at all, and the JSON
		// routes and the upload routes each set a deadline of their own on the
		// body (internal/server/httpjson).
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	s.background1(func() { s.store.SweepEvery(sweepInterval, s.db.KnownHashes, s.stopSweep) })
	s.background1(func() { s.sweepRowsEvery(sweepInterval) })

	scheme := "http"
	if cfg.TLSCert != "" {
		scheme = "https"
	}
	s.log.Log("start", "the server listens on "+cfg.Listen)
	slog.Info("portapixel-server", "listen", cfg.Listen, "scheme", scheme, "data", s.dataDir)
	if port := listenPort(cfg.Listen); cfg.PublicURL == "" && port != "" {
		slog.Info("the public URL is not set, so the admin UI answers on the loopback names only",
			"try", "http://localhost:"+port+"/")
	}

	errs := make(chan error, 1)
	go func() {
		if cfg.TLSCert != "" {
			errs <- httpSrv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
			return
		}
		errs <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fail("%v", err)
		}
		return 0
	case <-ctx.Done():
	}

	// A graceful stop: no new connection, and the requests in flight get their
	// time. A download of a large video is the request that needs it.
	slog.Info("stopping")
	s.log.Log("stop", "the server stops")
	// The mirror and the release list stop now. A download of 200 MB must not hold
	// the process open past the grace period of the supervisor.
	s.stopBackground()
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		// The grace period ended with a transfer still in flight. That is the end of
		// the grace period and not a fault: a device that pulls a 200 MB release holds
		// its connection for minutes, and the supervisor gives us eight seconds. Close
		// the rest deliberately and report success, so that a normal stop does not look
		// like a crash in the log of the unit.
		slog.Warn("a transfer was still in flight when the grace period ended", "error", err)
		s.log.Log("stop", "a transfer was still in flight when the grace period ended")
		httpSrv.Close()
	}
	return 0
}

// sweepRowsEvery drops the rows that the clock retired: the enroll requests that
// waited too long, and the commands that no screen ever ran.
//
// The commands need a sweep of their own, because the poll of one device can only
// retire the commands of that device. A screen that never comes back would leave
// every order of its queue at "delivered" for ever.
func (s *server) sweepRowsEvery(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopSweep:
			return
		case <-ticker.C:
			if err := s.db.SweepPending(); err != nil {
				slog.Warn("the sweep of the pending list failed", "error", err)
			}
			if err := s.db.SweepCommands(); err != nil {
				slog.Warn("the sweep of the command queue failed", "error", err)
			}
		}
	}
}

// The functions below are what the route packages call for the values that live
// in server.toml or in the settings table.

func (s *server) hosts() []string { return AllowedHosts(s.config()) }

// readFleet gives the cached values of the manifest.
func (s *server) readFleet() api.Fleet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fleet
}

// refreshFleet reads the values of the manifest from the database into the cache.
// Every write that changes one of them calls it.
func (s *server) refreshFleet() {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	name := s.db.Setting(db.SettingServerName, "PortaPixel")
	poll := s.db.SettingInt(db.SettingPollSeconds, db.DefaultPollSeconds)
	if poll < db.MinPollSeconds {
		poll = db.MinPollSeconds
	}
	var release *db.Release
	if rel, err := s.db.ApprovedRelease(); err == nil {
		release = &rel
	}
	s.mu.Lock()
	s.fleet = api.Fleet{ServerName: name, DefaultPoll: poll, Release: release}
	s.mu.Unlock()
}

func (s *server) checkPassword(password string) bool {
	return CheckPassword(password, s.config().AdminPasswordHash)
}

// setPassword writes a new admin password into server.toml.
func (s *server) setPassword(password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	cfg := s.cfg
	cfg.AdminPasswordHash = hash
	if err := SaveConfig(s.dataDir, cfg); err != nil {
		s.mu.Unlock()
		return err
	}
	s.cfg = cfg
	s.mu.Unlock()
	// The admin chose this password, so the "still on the installer password"
	// banner goes away.
	return s.db.SetSetting(settingPasswordSet, "yes")
}

// settings gives the settings of the admin UI.
func (s *server) settings() admin.Settings {
	cfg := s.config()
	fleet := s.readFleet()
	return admin.Settings{
		ServerName:        fleet.ServerName,
		PollSeconds:       fleet.DefaultPoll,
		PublicURL:         cfg.PublicURL,
		QuietAfterSeconds: fleet.DefaultPoll * 5 / 2,
		WeakPassword:      s.db.Setting(settingPasswordSet, "") != "yes",
	}
}

// settingPasswordSet remembers that a person chose the admin password. The first
// run makes a random password, which is not weak, but the admin UI still says
// "this is the password that the installer made" until somebody changes it.
const settingPasswordSet = "password_chosen"

// saveSettings writes the settings. Every value takes effect at once: the Host
// allowlist comes from a function that runs on each request, and the manifest
// values come from a cache that this function fills again.
func (s *server) saveSettings(in admin.Settings) error {
	if err := s.db.SetSetting(db.SettingServerName, strings.TrimSpace(in.ServerName)); err != nil {
		return err
	}
	if err := s.db.SetSetting(db.SettingPollSeconds, fmt.Sprint(in.PollSeconds)); err != nil {
		return err
	}
	s.mu.Lock()
	changed := in.PublicURL != s.cfg.PublicURL
	if changed {
		cfg := s.cfg
		cfg.PublicURL = in.PublicURL
		if err := SaveConfig(s.dataDir, cfg); err != nil {
			s.mu.Unlock()
			return err
		}
		s.cfg = cfg
	}
	s.mu.Unlock()
	s.refreshFleet()
	return nil
}
