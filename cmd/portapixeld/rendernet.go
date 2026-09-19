package main

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/netcfg"
	"github.com/ethanpil/portapixel/internal/opslog"
)

// renderNetCommand writes the network files from portapixel.toml.
//
// The OpenRC service portapixel-net runs this before the network starts. No shell
// script parses TOML (ARCHITECTURE section 4), and this is the only reason that
// the daemon has a subcommand for it.
//
// It must never stop the boot. When the configuration is bad, the exit code says
// so and the service keeps the files of the last good render, so the device stays
// reachable (plan 3.3).
func renderNetCommand(args []string) int {
	fs := flag.NewFlagSet("render-net", flag.ExitOnError)
	p := addPathFlags(fs)
	root := fs.String("root", "", "write under this directory in place of /. For a test.")
	dry := fs.Bool("print", false, "print the files and write nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	result := config.Load(p.media, p.state)
	cfg := result.Config
	if errs := cfg.Validate(); len(errs) > 0 {
		return fail("the configuration is not correct, so the network files are not written: %v", errs)
	}

	if *dry {
		fmt.Printf("--- %s\n%s\n", netcfg.InterfacesPath, netcfg.Interfaces(cfg))
		fmt.Printf("--- %s\n%s\n", netcfg.WPAPath, netcfg.WPASupplicant(cfg))
		fmt.Printf("--- %s\n%s\n", netcfg.HostnamePath, netcfg.Hostname(cfg))
		if resolv := netcfg.ResolvConf(cfg); resolv != nil {
			fmt.Printf("--- %s\n%s\n", netcfg.ResolvPath, resolv)
		}
		return 0
	}

	if err := netcfg.Write(*root, cfg); err != nil {
		return fail("%v", err)
	}

	log := opslog.New(filepath.Join(p.state, opsLogName))
	details := "mode=" + cfg.Network.Mode
	if cfg.Network.WifiSSID != "" {
		details += " wifi=yes"
	}
	if result.FromShadow {
		details += " from the shadow configuration"
	}
	log.Log("net.render", details)
	return 0
}
