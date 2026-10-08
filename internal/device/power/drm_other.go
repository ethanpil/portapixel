//go:build !linux

package power

import "errors"

// openCards has no DRM device on a system that is not Linux. A development
// machine can run the daemon, and the screen power then reports a fault in the
// ops log and does nothing.
func openCards() ([]drmCard, error) {
	return nil, errors.New("DPMS through DRM needs Linux")
}
