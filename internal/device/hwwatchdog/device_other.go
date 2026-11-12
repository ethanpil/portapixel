//go:build !linux

package hwwatchdog

import (
	"errors"
	"fmt"
	"time"
)

// openDevice has no device away from Linux. A development machine runs with no
// hardware watchdog and no warning.
func openDevice(path string, want time.Duration) (Device, error) {
	return nil, fmt.Errorf("no hardware watchdog away from Linux: %w", errors.ErrUnsupported)
}
