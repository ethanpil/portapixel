// Package web holds the web assets and puts them into the binary.
//
// Why this package exists: the daemon and the server are one file each. A
// device has no package manager and no build step for the web UI, so the files
// in the repository are the files that ship (ARCHITECTURE section 8). This
// package is the only place that speaks to the embedded file system. Each
// program gets a sub-tree, so a route can never read the files of another UI.
//
// The patterns use the "all:" prefix. Without it, go:embed leaves out a file
// whose name starts with a full stop or an underscore.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:player all:shared all:device-admin all:server-admin
var files embed.FS

// The four sub-trees. Each one is the root of the URL space that serves it:
// Player answers /player, Shared answers /shared, DeviceAdmin answers / on a
// device, and ServerAdmin answers / on the fleet server.
var (
	Player      = sub("player")
	Shared      = sub("shared")
	DeviceAdmin = sub("device-admin")
	ServerAdmin = sub("server-admin")
)

// sub gives one directory of the embedded tree. A failure here is a fault of the
// build, not of the device. A panic at start is better than a web UI that serves
// nothing.
func sub(dir string) fs.FS {
	out, err := fs.Sub(files, dir)
	if err != nil {
		panic("web: cannot open the embedded directory " + dir + ": " + err.Error())
	}
	return out
}

// Exists reports if a file is in the tree. The selftest subcommand uses it to
// prove that the build embedded the assets.
func Exists(tree fs.FS, name string) bool {
	f, err := tree.Open(name)
	if err != nil {
		return false
	}
	f.Close()
	return true
}
