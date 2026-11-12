package hwwatchdog

import (
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"syscall"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
)

// DefaultPath is the watchdog device of the kernel.
const DefaultPath = "/dev/watchdog"

// WantTimeout is the timeout that the daemon asks the driver for. It is long
// enough for a slow start of a daemon that supervise-daemon restarted, and short
// enough that a screen that hangs comes back in about a minute.
const WantTimeout = 60 * time.Second

// The install modes of the daemon flag --install-mode. os/install.sh writes the
// mode into /etc/conf.d/portapixeld.
const (
	ModeImage = "image"
	ModeOnBox = "onbox"
)

// unknownTimeoutPet is the pet interval when the driver does not give its
// timeout. No driver has a timeout under 1 s.
const unknownTimeoutPet = time.Second

// Device is an open watchdog device. The timer of the hardware runs from the
// open.
type Device interface {
	// Pet starts the timer again.
	Pet() error
	// Disarm writes the magic character and closes the device. The timer stops.
	Disarm() error
	// Timeout is the timeout that the driver uses, or 0 when the driver does not
	// say.
	Timeout() time.Duration
}

// Opener opens the device at path and asks for the timeout want.
type Opener func(path string, want time.Duration) (Device, error)

// Logger is the ops log. *opslog.Log is one.
type Logger interface {
	Log(event, details string)
}

// Options are the inputs of Start.
type Options struct {
	// Setting is watchdog.hardware: auto, on or off.
	Setting string
	// InstallMode is ModeImage or ModeOnBox.
	InstallMode string
	// Path is DefaultPath when it is empty.
	Path string
	// Open is the real device when it is nil.
	Open Opener
	Log  Logger
}

// Enabled reports if the setting turns the hardware watchdog on in this
// install mode. "auto" is on for every mode but an on-box install.
func Enabled(setting, installMode string) bool {
	switch setting {
	case config.HardwareOn:
		return true
	case config.HardwareAuto:
		return installMode != ModeOnBox
	}
	return false
}

// Watchdog is the open device, or nothing. Every method is safe on a Watchdog
// with no device.
//
// Pet and Stop are for one goroutine, the main loop of the daemon. Error does
// not change after Start, so any goroutine may read it.
type Watchdog struct {
	dev       Device
	interval  time.Duration
	err       string
	log       Logger
	petFailed bool
}

// Start opens the device when the setting says so. It never fails: a device that
// is missing or busy gives a Watchdog with no device and a sentence in Error.
func Start(o Options) *Watchdog {
	w := &Watchdog{log: o.Log}
	if !Enabled(o.Setting, o.InstallMode) {
		w.logf("hwwatchdog.off", "setting=%s install-mode=%s", o.Setting, o.InstallMode)
		return w
	}
	path := o.Path
	if path == "" {
		path = DefaultPath
	}
	open := o.Open
	if open == nil {
		open = openDevice
	}
	dev, err := open(path, WantTimeout)
	switch {
	case errors.Is(err, errors.ErrUnsupported):
		// A development machine. It has no watchdog, and that is not a fault.
		w.logf("hwwatchdog.off", "%v", err)
		return w
	case o.Setting == config.HardwareAuto && errors.Is(err, fs.ErrNotExist):
		// "auto" uses the watchdog when the machine has one. A machine with no
		// driver is not a fault, so the log gets a line and the status no warning.
		w.logf("hwwatchdog.absent", "%v", err)
		return w
	case err != nil:
		w.err = unavailable(path, err)
		w.logf("hwwatchdog.unavailable", "%s", w.err)
		return w
	}
	w.dev = dev
	timeout := dev.Timeout()
	w.interval = timeout / 3
	if timeout <= 0 {
		w.interval = unknownTimeoutPet
	}
	w.logf("hwwatchdog.open", "path=%s timeout=%s pet=%s", path, timeoutText(timeout), w.interval)
	w.Pet()
	return w
}

// unavailable gives the sentence of the status warning.
func unavailable(path string, err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "The hardware watchdog is on, but this machine has no " + path +
			". Load the watchdog driver of the board, or set watchdog.hardware to off."
	case errors.Is(err, syscall.EBUSY):
		return "The hardware watchdog is on, but another program holds " + path +
			". Stop that program, or set watchdog.hardware to off."
	}
	return fmt.Sprintf("The hardware watchdog is on, but %s did not open: %v.", path, err)
}

func timeoutText(t time.Duration) string {
	if t <= 0 {
		return "unknown"
	}
	return strconv.Itoa(int(t/time.Second)) + "s"
}

// Interval is the time between two pets, or 0 when there is no device.
func (w *Watchdog) Interval() time.Duration {
	if w.dev == nil {
		return 0
	}
	return w.interval
}

// Pet starts the timer of the hardware again. It logs the first fault only: a
// device that refuses one pet refuses all of them, and the ops log must not fill.
func (w *Watchdog) Pet() {
	if w.dev == nil {
		return
	}
	if err := w.dev.Pet(); err != nil && !w.petFailed {
		w.petFailed = true
		w.logf("hwwatchdog.pet.fail", "%v", err)
	}
}

// Stop disarms the timer and closes the device. Call it on a clean stop only.
func (w *Watchdog) Stop() {
	if w.dev == nil {
		return
	}
	if err := w.dev.Disarm(); err != nil {
		w.logf("hwwatchdog.stop.fail", "%v", err)
	} else {
		w.logf("hwwatchdog.stop", "the timer is off")
	}
	w.dev = nil
}

// Error is the sentence of the status warning, or "".
func (w *Watchdog) Error() string { return w.err }

func (w *Watchdog) logf(event, format string, args ...any) {
	if w.log != nil {
		w.log.Log(event, fmt.Sprintf(format, args...))
	}
}
