package hwwatchdog

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeDevice counts what the daemon does to the device.
type fakeDevice struct {
	timeout  time.Duration
	pets     int
	disarmed int
	petErr   error
}

func (d *fakeDevice) Pet() error             { d.pets++; return d.petErr }
func (d *fakeDevice) Disarm() error          { d.disarmed++; return nil }
func (d *fakeDevice) Timeout() time.Duration { return d.timeout }

type fakeLog struct{ events []string }

func (l *fakeLog) Log(event, details string) { l.events = append(l.events, event+" "+details) }

func (l *fakeLog) has(event string) bool {
	for _, e := range l.events {
		if strings.HasPrefix(e, event+" ") {
			return true
		}
	}
	return false
}

func TestEnabled(t *testing.T) {
	tests := []struct {
		setting, mode string
		want          bool
	}{
		{"auto", ModeImage, true},
		{"auto", ModeOnBox, false},
		{"on", ModeImage, true},
		{"on", ModeOnBox, true},
		{"off", ModeImage, false},
		{"off", ModeOnBox, false},
		{"", ModeImage, false},
	}
	for _, tt := range tests {
		if got := Enabled(tt.setting, tt.mode); got != tt.want {
			t.Errorf("Enabled(%q, %q) = %v, want %v", tt.setting, tt.mode, got, tt.want)
		}
	}
}

func TestStartPetsAndStopDisarms(t *testing.T) {
	dev := &fakeDevice{timeout: 15 * time.Second}
	var gotPath string
	var gotWant time.Duration
	log := &fakeLog{}
	w := Start(Options{
		Setting: "auto", InstallMode: ModeImage, Log: log,
		Open: func(path string, want time.Duration) (Device, error) {
			gotPath, gotWant = path, want
			return dev, nil
		},
	})
	if gotPath != DefaultPath || gotWant != WantTimeout {
		t.Errorf("open(%q, %s), want (%q, %s)", gotPath, gotWant, DefaultPath, WantTimeout)
	}
	// The pet interval is a third of the timeout that the driver gave, not of
	// the timeout that the daemon asked for.
	if w.Interval() != 5*time.Second {
		t.Errorf("Interval = %s, want 5s", w.Interval())
	}
	if dev.pets != 1 {
		t.Errorf("Start gave %d pets, want 1", dev.pets)
	}
	w.Pet()
	w.Pet()
	if dev.pets != 3 {
		t.Errorf("pets = %d, want 3", dev.pets)
	}
	if w.Error() != "" {
		t.Errorf("Error = %q", w.Error())
	}
	w.Stop()
	if dev.disarmed != 1 {
		t.Errorf("disarmed %d times, want 1", dev.disarmed)
	}
	// After the stop, nothing touches the device.
	w.Pet()
	w.Stop()
	if dev.pets != 3 || dev.disarmed != 1 || w.Interval() != 0 {
		t.Errorf("after Stop: pets %d, disarmed %d, interval %s", dev.pets, dev.disarmed, w.Interval())
	}
	if !log.has("hwwatchdog.open") || !log.has("hwwatchdog.stop") {
		t.Errorf("log = %q", log.events)
	}
}

func TestADriverWithNoTimeoutGetsAPetEachSecond(t *testing.T) {
	w := Start(Options{Setting: "on", Open: func(string, time.Duration) (Device, error) {
		return &fakeDevice{}, nil
	}})
	if w.Interval() != time.Second {
		t.Errorf("Interval = %s, want 1s", w.Interval())
	}
}

func TestOffOpensNothing(t *testing.T) {
	for _, tt := range []struct{ setting, mode string }{{"off", ModeImage}, {"auto", ModeOnBox}} {
		opened := false
		w := Start(Options{Setting: tt.setting, InstallMode: tt.mode, Open: func(string, time.Duration) (Device, error) {
			opened = true
			return &fakeDevice{}, nil
		}})
		if opened || w.Interval() != 0 || w.Error() != "" {
			t.Errorf("%s/%s: opened %v, interval %s, error %q", tt.setting, tt.mode, opened, w.Interval(), w.Error())
		}
		w.Pet()
		w.Stop()
	}
}

// A missing or busy device is a warning and never a fault of the start.
func TestUnavailableDeviceIsReported(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"missing", &fs.PathError{Op: "open", Path: DefaultPath, Err: syscall.ENOENT}, "has no /dev/watchdog"},
		{"busy", &fs.PathError{Op: "open", Path: DefaultPath, Err: syscall.EBUSY}, "another program holds /dev/watchdog"},
		{"other", errors.New("bad"), "did not open: bad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &fakeLog{}
			w := Start(Options{Setting: "on", Log: log, Open: func(string, time.Duration) (Device, error) {
				return nil, tt.err
			}})
			if !strings.Contains(w.Error(), tt.want) {
				t.Errorf("Error = %q, want it to hold %q", w.Error(), tt.want)
			}
			if w.Interval() != 0 {
				t.Errorf("Interval = %s, want 0", w.Interval())
			}
			if !log.has("hwwatchdog.unavailable") {
				t.Errorf("log = %q", log.events)
			}
			w.Pet()
			w.Stop()
		})
	}
}

// A development machine has no watchdog. That is not a fault.
func TestUnsupportedSystemGivesNoWarning(t *testing.T) {
	w := Start(Options{Setting: "on", Open: func(string, time.Duration) (Device, error) {
		return nil, fmt.Errorf("not here: %w", errors.ErrUnsupported)
	}})
	if w.Error() != "" || w.Interval() != 0 {
		t.Errorf("Error %q, interval %s", w.Error(), w.Interval())
	}
}

func TestPetFaultIsLoggedOnce(t *testing.T) {
	dev := &fakeDevice{timeout: 60 * time.Second, petErr: errors.New("gone")}
	log := &fakeLog{}
	w := Start(Options{Setting: "on", Log: log, Open: func(string, time.Duration) (Device, error) { return dev, nil }})
	w.Pet()
	w.Pet()
	n := 0
	for _, e := range log.events {
		if strings.HasPrefix(e, "hwwatchdog.pet.fail ") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d pet faults in the log, want 1: %q", n, log.events)
	}
}
