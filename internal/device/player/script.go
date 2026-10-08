package player

import _ "embed"

// transitionScript is the mpv script that draws the transitions. The daemon
// writes it into the run directory at each start of mpv (see Prepare), so the
// script always comes from the binary that runs.
//
//go:embed transitions.lua
var transitionScript []byte

// TransitionScript gives the embedded script. The selftest subcommand checks
// that the build put it into the binary.
func TransitionScript() []byte { return transitionScript }
