// Command portapixeld is the PortaPixel device daemon.
//
// It serves the admin UI, the player and the JSON API, and it supervises the
// browser. This file holds the subcommands, the flags and the wiring. Every rule
// lives in a package under internal/device (ARCHITECTURE section 4).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ethanpil/portapixel/internal/version"
)

// The default paths of an image install (ARCHITECTURE section 3). An on-box
// install gives other values on the command line, and the OpenRC service file
// always names all four.
const (
	defaultMedia    = "/media/ppmedia"
	defaultState    = "/var/lib/portapixel"
	defaultRun      = "/run/portapixel"
	defaultReleases = "/opt/portapixel"
)

// BrowserCmdEnv names the environment variable that replaces the browser command.
// It is for a development machine and for a test.
const BrowserCmdEnv = "PORTAPIXEL_BROWSER_CMD"

// paths are the four directories that the daemon works in.
type paths struct {
	media    string
	state    string
	run      string
	releases string
}

// addPathFlags adds the four path flags to a flag set.
func addPathFlags(fs *flag.FlagSet) *paths {
	p := &paths{}
	fs.StringVar(&p.media, "media", defaultMedia, "the media root: portapixel.toml, the playlists and the media files")
	fs.StringVar(&p.state, "state", defaultState, "the state directory on ext4: ops.log, state.json, the shadow configuration")
	fs.StringVar(&p.run, "run", defaultRun, "the run directory in RAM")
	fs.StringVar(&p.releases, "releases", defaultReleases, "the release directory: the current link and the health markers")
	return p
}

func main() {
	if len(os.Args) < 2 {
		os.Exit(runCommand(nil))
	}
	name, args := os.Args[1], os.Args[2:]
	// A flag in place of a subcommand means "run".
	if len(name) > 0 && name[0] == '-' {
		name, args = "run", os.Args[1:]
	}

	switch name {
	case "run":
		os.Exit(runCommand(args))
	case "selftest":
		os.Exit(selftestCommand(args))
	case "render-net":
		os.Exit(renderNetCommand(args))
	case "provision":
		os.Exit(provisionCommand(args))
	case "install-to-disk":
		os.Exit(installToDiskCommand(args))
	case "version":
		os.Exit(version.Command("portapixeld", args, os.Stdout, os.Stderr))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "portapixeld: unknown subcommand %q\n\n", name)
		usage()
		os.Exit(2)
	}
}

// versionCommand prints the build identity. internal/version owns both forms of
// the answer, and internal/updater reads the machine-readable one, so the writer
// and the reader cannot drift apart. The same function serves portapixel-server.

func usage() {
	fmt.Fprint(os.Stderr, `portapixeld -- the PortaPixel device daemon

Usage:
  portapixeld run [flags]         the daemon. This is the default.
  portapixeld selftest            check the embedded assets and the templates
  portapixeld render-net [flags]  write the network files from portapixel.toml
  portapixeld provision [flags]   the first boot steps that own TOML
  portapixeld install-to-disk DEV clone the running system to a disk (D54)
  portapixeld version [--json]    the build identity. --json is for a program.

Run "portapixeld <subcommand> -h" for the flags of a subcommand.
`)
}

// fail prints a message and gives the exit code of a failed subcommand.
func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "portapixeld: "+format+"\n", args...)
	return 1
}
