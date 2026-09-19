package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ethanpil/portapixel/internal/device/installer"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/version"
)

// installToDiskCommand is "portapixeld install-to-disk DEV" (D54).
//
// With no device it lists the candidates. With a device it asks the person to type
// the name back, unless --yes is given, and then it writes the progress to the
// terminal.
func installToDiskCommand(args []string) int {
	fs := flag.NewFlagSet("install-to-disk", flag.ExitOnError)
	p := addPathFlags(fs)
	confirm := fs.String("confirm", "", "the device path typed back, for example /dev/sda. It must match exactly.")
	list := fs.Bool("list", false, "list the disks that this machine can install onto and stop")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	inst := installer.New(installer.Options{
		MediaRoot: p.media,
		MountRoot: p.run,
		Arch:      version.Arch(),
		Run:       rootRunner{},
		Log:       opslog.New(filepath.Join(p.state, opsLogName)),
	})

	if *list || fs.NArg() == 0 {
		return listDisks(inst, *list)
	}
	device := fs.Arg(0)

	// The confirmation is the same rule as the API: the device path, character for
	// character. A command line that erases a disk on one word is a command line
	// that erases the wrong disk.
	if *confirm == "" {
		fmt.Fprintf(os.Stderr,
			"portapixeld install-to-disk: this erases everything on %s.\n"+
				"Run it again with --confirm %s to go on.\n", device, device)
		return 2
	}

	fmt.Printf("Installing onto %s. This takes a few minutes.\n", device)
	failed := false
	inst.Run(context.Background(), device, *confirm, func(name string, data any) {
		switch event := data.(type) {
		case installer.Progress:
			line := fmt.Sprintf("%3d%%  %s", event.Percent, event.Phase)
			if event.Message != "" {
				line += " (" + event.Message + ")"
			}
			fmt.Println(line)
		case installer.Done:
			if !event.OK {
				failed = true
				fmt.Fprintf(os.Stderr, "portapixeld install-to-disk: %s\n", event.Error)
				return
			}
			fmt.Println("\nThe disk is ready. Now do these three things, in this order:")
			fmt.Println("  1. Power the machine off.")
			fmt.Println("  2. Take the USB stick out.")
			fmt.Printf("  3. Start the machine again and let it boot from %s.\n", device)
			fmt.Println("\nBoth disks now carry the same partition labels. Taking the stick out is")
			fmt.Println("what tells the machine which one to use.")
		}
	})
	if failed {
		return 1
	}
	return 0
}

// listDisks prints the candidate targets. explicit says that the person asked for
// the list, so an empty list is not an error then.
func listDisks(inst *installer.Installer, explicit bool) int {
	disks, err := inst.Disks()
	if err != nil {
		return fail("%v", err)
	}
	if len(disks) == 0 {
		fmt.Println("No other disk is in this machine.")
		if explicit {
			return 0
		}
		return 1
	}
	fmt.Println("Disks that this machine can install onto:")
	for _, d := range disks {
		note := ""
		if d.TooSmall {
			note = "  TOO SMALL"
		} else if d.Removable {
			note = "  removable"
		}
		fmt.Printf("  %-14s %6d MB  %s%s\n", d.Device, d.SizeBytes>>20, d.Model, note)
	}
	if !explicit {
		fmt.Println("\nRun: portapixeld install-to-disk DEV --confirm DEV")
	}
	return 0
}
