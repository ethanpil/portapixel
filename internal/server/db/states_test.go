package db

import (
	"testing"

	"github.com/ethanpil/portapixel/internal/server/releases"
)

// TestTheMirrorStatesAgree keeps the two sets of state words together.
//
// internal/server/releases writes the state of a mirror and this package stores it.
// The mirror cannot import this package: it reports through a callback, so that the
// download path knows nothing about SQL. So each side holds its own constants, and
// a typo in one of them would give a mirror that never reports "done" and a fleet
// that never gets a release. This test is the guard.
func TestTheMirrorStatesAgree(t *testing.T) {
	for _, c := range []struct {
		name          string
		here, inThere string
	}{
		{"working", MirrorWorking, releases.MirrorWorking},
		{"done", MirrorDone, releases.MirrorDone},
		{"failed", MirrorFailed, releases.MirrorFailed},
	} {
		if c.here != c.inThere {
			t.Errorf("the %s state is %q here and %q in internal/server/releases",
				c.name, c.here, c.inThere)
		}
	}
}
