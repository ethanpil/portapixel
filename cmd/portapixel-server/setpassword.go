package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

// setPasswordCommand reads a new admin password and writes its hash into
// server.toml.
//
// The password comes from the standard input, so a script can pipe it in:
//
//	echo "a long password" | portapixel-server set-password --data /var/lib/portapixel-server
//
// A person who runs it at a terminal gets a prompt. The characters are visible
// while they type: to hide them the program would need a terminal package, and
// the contract permits no dependency for that. The prompt says so, and the pipe
// above is the way to keep the password off the screen.
func setPasswordCommand(args []string) int {
	fs := flag.NewFlagSet("set-password", flag.ExitOnError)
	dataDir, _ := addCommonFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, _, err := LoadConfig(*dataDir)
	if err != nil {
		return fail("%v", err)
	}

	// A terminal gets a prompt. A pipe gets nothing, so the output of a script
	// stays clean.
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		fmt.Printf("New admin password for %s.\n", ConfigPath(*dataDir))
		fmt.Printf("It needs %d characters or more, and it is visible while you type.\n", minPasswordLength)
		fmt.Print("Password: ")
	}

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return fail("no password came in on the standard input")
	}
	password := strings.TrimRight(line, "\r\n")

	hash, err := HashPassword(password)
	if err != nil {
		return fail("%v", err)
	}
	cfg.AdminPasswordHash = hash
	if err := SaveConfig(*dataDir, cfg); err != nil {
		return fail("%v", err)
	}

	// The "still on the installer password" banner of the admin UI comes from the
	// database, so a password that a person chose has to be written there as well.
	if database, err := openDatabase(*dataDir); err == nil {
		database.SetSetting(settingPasswordSet, "yes")
		database.Close()
	}

	fmt.Println("The admin password is set.")
	return 0
}
