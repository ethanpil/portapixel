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
	"github.com/ethanpil/portapixel/internal/server/admin"
	"github.com/ethanpil/portapixel/internal/server/api"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/media"
	"github.com/ethanpil/portapixel/internal/server/releases"
)

// shutdownGrace is how long a shutdown waits for the requests that are in flight.
// A device that downloads a large video gets that time to finish.
const shutdownGrace = 20 * time.Second

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
	handler http.Handler

	// mu guards cfg, which the settings route writes.
	mu sync.RWMutex
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
			" Write it down now. The server keeps only its hash, so it cannot\n"+
			" print it again. Change it on the Settings page or with\n"+
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
	mirror := releases.NewMirror(dataDir, cfg.GitHubRepo)
	mirror.Report = func(version, state, errText string) {
		if err := database.SetMirrorState(version, state, errText); err != nil {
			slog.Error("write the mirror state", "version", version, "error", err)
		}
		if errText != "" {
			log.Log("mirror-"+state, version+": "+errText)
		} else {
			log.Log("mirror-"+state, version)
		}
	}

	s := &server{
		cfg: cfg, dataDir: dataDir, db: database,
		log: log, store: store, mirror: mirror,
	}
	s.handler = s.routes()
	return s, nil
}

// Close gives the database back.
func (s *server) Close() {
	if s.db != nil {
		s.db.Close()
	}
}

// config gives the configuration as it is now.
func (s *server) config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// routes builds the handler.
//
// The device API and the admin UI get different guards, which is why they are two
// muxes. The device API takes no Host allowlist and no CSRF header: a device is
// not a browser, it carries a bearer token and no cookie, and its Host header is
// whatever the admin typed into its configuration. The browser side takes both
// guards (D46).
func (s *server) routes() http.Handler {
	deviceMux := http.NewServeMux()
	api.Deps{
		DB:          s.db,
		Media:       s.store,
		Mirror:      s.mirror,
		Log:         s.log,
		Limiter:     httpguard.NewLimiter(),
		ServerName:  s.serverName,
		DefaultPoll: s.defaultPoll,
	}.Routes(deviceMux)

	uiMux := http.NewServeMux()
	admin.Deps{
		DB:            s.db,
		Media:         s.store,
		Mirror:        s.mirror,
		Log:           s.log,
		Sessions:      httpguard.NewSessions(),
		Limiter:       httpguard.NewLimiter(),
		DataDir:       s.dataDir,
		StartedAt:     time.Now(),
		Hosts:         s.hosts,
		CheckPassword: s.checkPassword,
		SetPassword:   s.setPassword,
		Settings:      s.settings,
		SaveSettings:  s.saveSettings,
	}.Routes(uiMux)
	s.staticRoutes(uiMux)

	var ui http.Handler = uiMux
	ui = httpguard.RequireHeader(ui)
	ui = httpguard.HostAllowlist(s.hosts)(ui)

	root := http.NewServeMux()
	root.Handle("/api/v1/", deviceMux)
	root.Handle("/", ui)
	return root
}

// Serve listens and answers until the context ends.
func (s *server) Serve(ctx context.Context) int {
	cfg := s.config()
	httpSrv := &http.Server{
		Addr:    cfg.Listen,
		Handler: s.handler,
		// A device that uploads nothing and reads a large video needs no write
		// deadline, and a deadline here would cut a download of a 1 GB file. The
		// read header timeout is the one that stops a connection that sends
		// nothing at all.
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

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
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		return fail("the shutdown did not finish: %v", err)
	}
	return 0
}

// The functions below are what the route packages call for the values that live
// in server.toml or in the settings table.

func (s *server) hosts() []string { return AllowedHosts(s.config()) }

func (s *server) serverName() string {
	return s.db.Setting(db.SettingServerName, "PortaPixel")
}

func (s *server) defaultPoll() int {
	return s.db.SettingInt(db.SettingPollSeconds, s.config().DefaultPollSeconds)
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
	defer s.mu.Unlock()
	cfg := s.cfg
	cfg.AdminPasswordHash = hash
	if err := SaveConfig(s.dataDir, cfg); err != nil {
		return err
	}
	s.cfg = cfg
	// The admin chose this password, so the "still on the installer password"
	// banner goes away.
	return s.db.SetSetting(settingPasswordSet, "yes")
}

// settings gives the settings of the admin UI.
func (s *server) settings() admin.Settings {
	cfg := s.config()
	poll := s.defaultPoll()
	return admin.Settings{
		ServerName:        s.serverName(),
		PollSeconds:       poll,
		PublicURL:         cfg.PublicURL,
		QuietAfterSeconds: poll * 5 / 2,
		WeakPassword:      s.db.Setting(settingPasswordSet, "") != "yes",
	}
}

// settingPasswordSet remembers that a person chose the admin password. The first
// run makes a random password, which is not weak, but the admin UI still says
// "this is the password that the installer made" until somebody changes it.
const settingPasswordSet = "password_chosen"

// saveSettings writes the settings. The name and the poll interval take effect at
// once; the public URL needs a restart, and the route says so.
func (s *server) saveSettings(in admin.Settings) error {
	if err := s.db.SetSetting(db.SettingServerName, strings.TrimSpace(in.ServerName)); err != nil {
		return err
	}
	if err := s.db.SetSetting(db.SettingPollSeconds, fmt.Sprint(in.PollSeconds)); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.PublicURL == s.cfg.PublicURL && in.PollSeconds == s.cfg.DefaultPollSeconds {
		return nil
	}
	cfg := s.cfg
	cfg.PublicURL = in.PublicURL
	cfg.DefaultPollSeconds = in.PollSeconds
	if err := SaveConfig(s.dataDir, cfg); err != nil {
		return err
	}
	s.cfg = cfg
	return nil
}
