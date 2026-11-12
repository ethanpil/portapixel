//go:build linux

package hwwatchdog

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// fileDevice is /dev/watchdog. The calls are those of
// Documentation/watchdog/watchdog-api.rst. WDIOC_SETTIMEOUT and
// WDIOC_GETTIMEOUT take a pointer to a C int (4 bytes) on amd64 and arm64.
// unix.IoctlSetPointerInt passes an int32. unix.IoctlGetInt reads into a Go int
// of 8 bytes, set to 0 first: the kernel writes the low 4 bytes, which is the
// whole value on a little-endian machine with a positive timeout.
type fileDevice struct {
	f       *os.File
	timeout time.Duration
}

// openDevice opens the device. The open starts the timer with the timeout of
// the driver. A driver that refuses want (EINVAL above its max_timeout) keeps
// its own timeout, and GETTIMEOUT gives the value that runs. A driver with
// max_hw_heartbeat_ms takes a longer timeout than its hardware: the watchdog
// core then pets the hardware while the daemon pets in time. The Pi bcm2835
// driver is one (hardware limit about 16 s), so a Pi gets 60 s too.
func openDevice(path string, want time.Duration) (Device, error) {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	_ = unix.IoctlSetPointerInt(fd, unix.WDIOC_SETTIMEOUT, int(want/time.Second))
	d := &fileDevice{f: f}
	if secs, err := unix.IoctlGetInt(fd, unix.WDIOC_GETTIMEOUT); err == nil && secs > 0 {
		d.timeout = time.Duration(secs) * time.Second
	}
	return d, nil
}

// Pet writes one byte. Each driver of the watchdog core takes a write as a pet;
// WDIOC_KEEPALIVE needs WDIOF_KEEPALIVEPING, which a few drivers do not set. The
// byte is not 'V', so it also cancels a magic close that was not finished.
func (d *fileDevice) Pet() error {
	_, err := d.f.Write([]byte{0})
	return err
}

// Disarm writes 'V' and closes. The kernel then stops the timer, unless it was
// built with CONFIG_WATCHDOG_NOWAYOUT or the module has nowayout=1.
func (d *fileDevice) Disarm() error {
	_, werr := d.f.Write([]byte("V"))
	cerr := d.f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func (d *fileDevice) Timeout() time.Duration { return d.timeout }
