// Command portapixel-server is the PortaPixel fleet server.
//
// It serves the device API, the admin UI and the media files. This file holds the
// subcommands and the flags. Every rule lives in a package under
// internal/server (ARCHITECTURE section 2).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ethanpil/portapixel/internal/version"
)

// defaultData is the data directory of a package install. The Docker image and a
// development run give another value.
const defaultData = "/var/lib/portapixel-server"

// addCommonFlags adds the two flags that every subcommand takes. The listen
// address is a flag as well as a setting in server.toml, because a container
// takes it from the command line and a package install from the file.
func addCommonFlags(fs *flag.FlagSet) (data, listen *string) {
	data = fs.String("data", defaultData, "the data directory: server.toml, the database, the media and the release mirror")
	listen = fs.String("listen", "", "the listen address, for example :8080 (it replaces the value in server.toml)")
	return data, listen
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
	case "set-password":
		os.Exit(setPasswordCommand(args))
	case "selftest":
		os.Exit(selftestCommand(args))
	case "version":
		os.Exit(version.Command("portapixel-server", args, os.Stdout, os.Stderr))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "portapixel-server: unknown subcommand %q\n\n", name)
		usage()
		os.Exit(2)
	}
}

// The "version" subcommand lives in internal/version, which owns both forms of
// the answer. The server replaces itself the same way that a device does, so both
// commands give the same shape and the updater reads one contract.

func usage() {
	fmt.Fprint(os.Stderr, `portapixel-server -- the PortaPixel fleet server

Usage:
  portapixel-server run [flags]           the server. This is the default.
  portapixel-server set-password [flags]  read a new admin password and store its hash
  portapixel-server selftest [flags]      check the build, the data directory and the database
  portapixel-server version [--json]      the build identity. --json is for a program.

Run "portapixel-server <subcommand> -h" for the flags of a subcommand.
`)
}

// fail prints a message and gives the exit code of a failed subcommand.
func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "portapixel-server: "+format+"\n", args...)
	return 1
}
