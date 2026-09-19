// Command seed fills a development fleet server with realistic data, so the
// admin UI can be driven through every page by hand.
//
// It talks to the real API only, except for the backdate step: no admin route
// can move a last_seen time into the past, and a quiet screen and an offline
// screen are two of the states that the UI must draw.
//
// It is a development tool. It is not in any release.
//
//	go run ./tests/server-admin/seed fill     -password <the first-run password>
//	go run ./tests/server-admin/seed backdate -data <the data directory>   # server stopped
//	go run ./tests/server-admin/seed swap                                  # server running
//	go run ./tests/server-admin/seed poll                                  # keeps screens alive
//
// fill writes the device tokens into a state file, so swap and poll can speak
// for the same screens later.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	name, args := os.Args[1], os.Args[2:]

	fs := flag.NewFlagSet(name, flag.ExitOnError)
	base := fs.String("base", "http://127.0.0.1:8095", "the address of the running server")
	password := fs.String("password", "", "the admin password (fill only)")
	statePath := fs.String("state", "seed-state.json", "the file that holds the device tokens")
	dataDir := fs.String("data", "", "the data directory of the server (backdate only)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	var err error
	switch name {
	case "fill":
		err = fill(*base, *password, *statePath)
	case "backdate":
		err = backdate(*dataDir)
	case "swap":
		err = swap(*base, *statePath)
	case "poll":
		err = poll(*base, *statePath)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `seed -- fill a development fleet server

Usage:
  seed fill     -password <password> [-base URL] [-state FILE]
  seed backdate -data <directory>            the server must be stopped
  seed swap     [-base URL] [-state FILE]    reports new hardware on one screen
  seed poll     [-base URL] [-state FILE]    keeps the live screens checking in
`)
}
