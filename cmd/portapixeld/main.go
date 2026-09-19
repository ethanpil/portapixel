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

	"github.com/ethanpil/portapixel/internal/updater"
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
		os.Exit(versionCommand(args))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "portapixeld: unknown subcommand %q\n\n", name)
		usage()
		os.Exit(2)
	}
}

// versionCommand prints the build identity.
//
// The line with no flag is for a person and its shape never changes:
// "portapixeld <version> <arch>". "--json" prints the machine-readable form that
// internal/updater reads before it installs a release. A program must never take
// a field of the human line by its position: the first updater took the last
// field and compared the processor name with the release name.
func versionCommand(args []string) int {
	if len(args) == 1 && args[0] == "--json" {
		info := updater.BinaryInfo{Name: "portapixeld", Version: version.Version, Arch: version.Arch()}
		data, err := info.JSON()
		if err != nil {
			return fail("cannot say which version this build is: %v", err)
		}
		os.Stdout.Write(data)
		return 0
	}
	if len(args) > 0 {
		return fail("version takes no argument but --json")
	}
	fmt.Printf("portapixeld %s %s\n", version.Version, version.Arch())
	return 0
}

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
