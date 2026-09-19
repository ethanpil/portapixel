// Command tagcheck proves that a release tag and a build version are one value.
//
// The health gate waits for <releases>/health/<version>.ok and the daemon writes
// the name of the version that -ldflags put in it. If the tag and that version
// were two values, every good release would roll back and be marked bad for ever
// (final review 19). The check uses the same function as the device, so a change
// in one place cannot leave the other behind.
//
// Usage: tagcheck v1.5.0 1.5.0
package main

import (
	"fmt"
	"os"

	"github.com/ethanpil/portapixel/internal/updater"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: tagcheck <tag> <version>")
		os.Exit(2)
	}
	tag, version := os.Args[1], os.Args[2]

	if got := updater.NormalizeVersion(tag); got != version {
		fmt.Fprintf(os.Stderr,
			"FAIL: updater.NormalizeVersion(%q) gives %q and the build uses %q.\n"+
				"The release tag and the release version must be one value.\n", tag, got, version)
		os.Exit(1)
	}
	if !updater.ValidVersion(version) {
		fmt.Fprintf(os.Stderr,
			"FAIL: %q is not a name that a device accepts as a release directory.\n", version)
		os.Exit(1)
	}
	if updater.CompareVersions(version, "dev") <= 0 {
		fmt.Fprintf(os.Stderr,
			"FAIL: a device sees %q as not newer than a development build.\n", version)
		os.Exit(1)
	}
	fmt.Printf("ok: the tag %s builds version %s, and the health marker is %s%s\n",
		tag, version, version, updater.OKSuffix)
}
