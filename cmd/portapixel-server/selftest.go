package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/media"
	"github.com/ethanpil/portapixel/web"
)

// healthTimeout is how long the --url check waits. The Docker HEALTHCHECK calls
// it, so it must answer inside the interval of that check.
const healthTimeout = 5 * time.Second

// selftestCommand checks that this build and this data directory work.
//
// With --url it instead asks a server that already runs. That is the Docker
// HEALTHCHECK: the image holds no curl, and the binary can ask for itself.
func selftestCommand(args []string) int {
	fs := flag.NewFlagSet("selftest", flag.ExitOnError)
	dataDir, _ := addCommonFlags(fs)
	url := fs.String("url", "", "check a server that runs at this address instead of this data directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *url != "" {
		return checkURL(*url)
	}

	// The embedded assets. A build that lost its go:embed lines would serve a
	// blank page, and this is the check that CI runs (plan section 17 item 2).
	for _, name := range []string{"pp.css", "api.js", "playlist-editor.js", "ui.js"} {
		if !web.Exists(web.Shared, name) {
			return fail("web/shared/%s is not in this build", name)
		}
	}
	fmt.Println("the shared web assets are in this build")

	// The admin UI is a separate piece of work. Say what is there and do not
	// fail: a server with no UI still answers every route of the admin API, and
	// the device side does not need the UI at all.
	if web.Exists(web.ServerAdmin, "index.html") {
		fmt.Println("the admin UI is in this build")
	} else {
		fmt.Println("the admin UI has no index.html yet; the API answers, the pages do not")
	}

	// The configuration and the database. A selftest with no data directory of its
	// own makes one in a temporary place, so it changes nothing.
	dir := *dataDir
	if _, err := os.Stat(dir); err != nil {
		tmp, err := os.MkdirTemp("", "portapixel-selftest")
		if err != nil {
			return fail("%v", err)
		}
		defer os.RemoveAll(tmp)
		dir = tmp
		fmt.Printf("%s is not there, so the check uses a temporary directory\n", *dataDir)
	}

	cfg, warnings, err := LoadConfig(dir)
	if err != nil {
		return fail("%v", err)
	}
	for _, warning := range warnings {
		fmt.Println("server.toml:", warning)
	}
	fmt.Printf("server.toml reads: listen %s, repository %s\n", cfg.Listen, cfg.GitHubRepo)

	database, err := db.Open(filepath.Join(dir, DatabaseName))
	if err != nil {
		return fail("%v", err)
	}
	defer database.Close()

	result, err := database.IntegrityCheck()
	if err != nil {
		return fail("the integrity check failed: %v", err)
	}
	if result != "ok" {
		return fail("the database is damaged: %s", result)
	}
	fmt.Println("the database opens and its integrity check says ok")

	store, err := media.New(dir)
	if err != nil {
		return fail("%v", err)
	}
	probe, err := os.CreateTemp(store.Root(), "selftest*")
	if err != nil {
		return fail("the media store is not writable: %v", err)
	}
	probe.Close()
	os.Remove(probe.Name())
	fmt.Println("the media store is writable")

	fmt.Println("selftest: everything passed")
	return 0
}

// checkURL asks a running server if it is healthy. The session route is the right
// one: it needs no session of its own, it touches the database through the
// settings, and it answers JSON.
func checkURL(base string) int {
	client := &http.Client{Timeout: healthTimeout}
	resp, err := client.Get(base + "/api/admin/session")
	if err != nil {
		return fail("%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail("%s/api/admin/session answered %s", base, resp.Status)
	}
	fmt.Println("the server answers")
	return 0
}

// openDatabase opens the database of a data directory. The set-password command
// uses it, so the two places that open it name the file the same way.
func openDatabase(dataDir string) (*db.DB, error) {
	return db.Open(filepath.Join(dataDir, DatabaseName))
}
