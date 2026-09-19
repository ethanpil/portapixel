package main

import (
	"flag"
	"fmt"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/playlist"
	"github.com/ethanpil/portapixel/web"
)

// selftestCommand checks that this binary is complete.
//
// The build puts the web assets into the binary with go:embed. A pattern that
// matches nothing, or an asset that somebody moved, makes a binary that starts,
// serves nothing, and shows a black screen. The CI gate for the aarch64 image runs
// this subcommand under qemu-user, because it cannot boot that image (plan
// section 17).
func selftestCommand(args []string) int {
	fs := flag.NewFlagSet("selftest", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	faults := 0
	check := func(what string, ok bool, detail string) {
		if ok {
			fmt.Printf("ok    %s\n", what)
			return
		}
		faults++
		fmt.Printf("FAIL  %s: %s\n", what, detail)
	}

	// 1. The web assets.
	check("the player is in the binary", web.Exists(web.Player, "index.html"), "web/player/index.html is missing")
	check("the shared stylesheet is in the binary", web.Exists(web.Shared, "pp.css"), "web/shared/pp.css is missing")

	// 2. The configuration template. Render it, parse it again, and check it: a
	// template that cannot be parsed would break the first boot of every device.
	rendered := config.Render(config.Default())
	parsed, err := config.Parse(rendered)
	switch {
	case err != nil:
		check("the default configuration parses", false, err.Error())
	default:
		check("the default configuration parses", true, "")
		if errs := parsed.Validate(); len(errs) > 0 {
			check("the default configuration is valid", false, errs.Error())
		} else {
			check("the default configuration is valid", true, "")
		}
		// Render is stable: the same values must give the same bytes.
		check("the configuration template is stable", string(config.Render(parsed)) == string(rendered),
			"a second render gave other bytes")
	}

	// 3. The playlist template, with the same reasoning.
	example := playlist.Playlist{
		Meta:  playlist.Meta{Name: "Default"},
		Items: []playlist.Item{{File: "welcome.jpg", Duration: 10}},
	}
	playlistBytes := playlist.Render(example)
	if _, err := playlist.Parse(playlistBytes, playlist.Options{}); err != nil {
		check("the playlist template parses", false, err.Error())
	} else {
		check("the playlist template parses", true, "")
	}

	if faults > 0 {
		fmt.Printf("\n%d checks failed\n", faults)
		return 1
	}
	fmt.Println("\nall checks passed")
	return 0
}
