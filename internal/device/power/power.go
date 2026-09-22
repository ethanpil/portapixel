package power

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/opslog"
)

// The words of display.power_method.
const (
	MethodAuto = "auto"
	MethodCEC  = "cec"
	MethodDPMS = "dpms"
	MethodNone = "none"
)

// tick is how often the loop compares the screen with the schedule. The times
// have a resolution of one minute, so 30 seconds crosses every boundary in time.
const tick = 30 * time.Second

// commandTimeout is how long one cec-ctl or wlr-randr call may take. Both talk to
// a local device or a local socket and answer at once. A call that hangs must not
// hold the loop, because the loop also drives the browser.
const commandTimeout = 15 * time.Second

// Runner runs one program and gives its output. The daemon gives a runner that
// starts the program in the kiosk session; a test gives a fake, so nothing here
// needs Linux, a display or root rights.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Screen is the browser. The screen-off step stops it: a browser with no picture
// only holds memory (D31).
type Screen interface {
	Suspend() error
	Resume() error
}

// Options are the parameters of a Controller.
type Options struct {
	// Method gives display.power_method: auto, cec, dpms or none. It is a
	// function, because a person can change the value while the daemon runs.
	Method func() string
	// ShouldBeOn reports if the screen schedule wants the screen on at t. With no
	// schedule it always answers true.
	ShouldBeOn func(t time.Time) bool
	// Now gives the local time of the device.
	Now func() time.Time
	// Run runs cec-ctl and wlr-randr.
	Run Runner
	// Browser is suspended and resumed with the screen.
	Browser Screen
	// CECDevices gives the CEC device nodes, for example /dev/cec0. A nil
	// function reads /dev/cec*.
	CECDevices func() []string
	Log        *opslog.Log
	// Tick is the period of the loop. 0 uses 30 seconds. A test sets a short
	// value.
	Tick time.Duration
}

// Controller holds the screen state and drives the transitions. It is safe for
// use by more than one goroutine.
type Controller struct {
	opt Options

	// applyMu holds one transition at a time. The state of the screen is decided
	// and applied under it, so a command from a person and the schedule loop cannot
	// read the same old state and both act on it.
	//
	// It is not mu. A transition talks to cec-ctl or wlr-randr and can take seconds,
	// and /api/status must answer in that time.
	applyMu sync.Mutex

	mu sync.Mutex
	// on is the state that the controller last applied.
	on bool
	// manual is true while a command from a person or from the fleet overrides
	// the schedule. The next schedule edge clears it.
	manual bool
	// lastWant is the answer of the schedule at the last tick. A different answer
	// is an edge.
	lastWant bool
	// chosen is the method that the probe picked, or "" before the first probe.
	chosen string
	// cecDevice and cecAddress are what the CEC probe found.
	cecDevice  string
	cecAddress string
}

// New makes a Controller. It does not talk to the display: the caller calls Start.
func New(opt Options) *Controller {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.ShouldBeOn == nil {
		opt.ShouldBeOn = func(time.Time) bool { return true }
	}
	if opt.Method == nil {
		opt.Method = func() string { return MethodAuto }
	}
	if opt.CECDevices == nil {
		opt.CECDevices = cecDevices
	}
	if opt.Tick <= 0 {
		opt.Tick = tick
	}
	return &Controller{opt: opt, on: true, lastWant: true}
}

// ScreenOn reports the state that the controller last applied. /api/status shows
// it (plan section 8).
func (c *Controller) ScreenOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.on
}

// Method gives the method that the controller uses, or "" before the first
// transition. The About page shows it.
func (c *Controller) Method() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.chosen
}

// Start puts the screen in the state that the schedule asks for. The daemon calls
// it one time, at start: a device that boots inside its night hours must not show
// a picture for the rest of the night.
func (c *Controller) Start() {
	want := c.opt.ShouldBeOn(c.opt.Now())

	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	c.mu.Lock()
	c.lastWant = want
	already := c.on == want
	c.mu.Unlock()

	if already {
		return
	}
	c.apply(want, "the screen schedule at start")
}

// Run drives the screen until done is closed.
func (c *Controller) Run(done <-chan struct{}) {
	t := time.NewTicker(c.opt.Tick)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			c.step()
		}
	}
}

// step is one pass of the loop. A manual command holds until the schedule crosses
// an edge, so "screen on" in the middle of the night lasts until the morning and
// not until the next tick.
func (c *Controller) step() {
	want := c.opt.ShouldBeOn(c.opt.Now())

	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	c.mu.Lock()
	edge := want != c.lastWant
	c.lastWant = want
	cleared := edge && c.manual
	if cleared {
		c.manual = false
	}
	hold := c.manual
	same := c.on == want
	c.mu.Unlock()

	if cleared {
		c.log("power.manual.end", "the screen schedule takes the screen again")
	}
	if hold || same {
		return
	}
	c.apply(want, "the screen schedule")
}

// Set is the manual command: screen-on and screen-off from the API, and later
// from the fleet queue. It overrides the schedule until the next schedule edge.
//
// It waits for a transition that runs. Without that wait the command read the state
// from before a transition that was still in the display call, saw the state that it
// asked for, answered 200 and did nothing. The manual hold then kept the screen in
// the state of the transition until the next schedule edge, hours later.
func (c *Controller) Set(on bool, reason string) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	c.mu.Lock()
	c.manual = true
	same := c.on == on
	c.mu.Unlock()

	if same {
		return nil
	}
	return c.apply(on, reason)
}

// apply makes one transition and writes one ops log line. The caller holds
// applyMu: the decision to make a transition and the transition itself are one
// step.
//
// The order is the reason that this package exists. Going off, the display is
// switched first and the browser second: the DPMS path talks to the compositor of
// the browser session, so a browser that stopped first takes the compositor with
// it and the display stays on. Going on, the browser starts first, so a
// compositor exists when the display call runs.
func (c *Controller) apply(on bool, reason string) error {
	if on {
		if err := c.resume(); err != nil {
			// The browser refused the message, so the transition did NOT happen. Keep
			// the old state, so that the next tick of the loop tries again.
			//
			// This branch recorded the new state and wrote "power.on" anyway. The
			// command queue of the browser is full while a cold launch runs, and the
			// screen-on at on_time then got ErrBusy. step() saw the state that the
			// schedule wanted, returned, and never tried again: /api/status said
			// screen_on true, the browser stayed suspended and the screen was black
			// for the whole day.
			c.log("power.on.fail", reason+": "+err.Error()+"; the loop tries again")
			return err
		}
		c.screen(true)
		c.mu.Lock()
		c.on = true
		c.mu.Unlock()
		c.log("power.on", reason)
		return nil
	}
	// Going off, the display call comes first and it has no error to give. A browser
	// that refuses to stop keeps the state at on, so the next tick tries again.
	c.screen(false)
	if err := c.suspend(); err != nil {
		c.log("power.off.fail", reason+": "+err.Error()+"; the loop tries again")
		return err
	}
	c.mu.Lock()
	c.on = false
	c.mu.Unlock()
	c.log("power.off", reason)
	return nil
}

// suspend stops the browser. A browser that is busy gives an error, which the API
// reports: a command that says "done" and does nothing is worse than an error.
func (c *Controller) suspend() error {
	if c.opt.Browser == nil {
		return nil
	}
	return c.opt.Browser.Suspend()
}

func (c *Controller) resume() error {
	if c.opt.Browser == nil {
		return nil
	}
	return c.opt.Browser.Resume()
}

// screen switches the display itself. Every fault is a log line and nothing more.
// A display that stays on is a fault to report; it is not a reason to keep a
// browser running through the night.
func (c *Controller) screen(on bool) {
	method := c.pick()
	if method == MethodNone {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	var err error
	switch method {
	case MethodCEC:
		c.mu.Lock()
		device, address := c.cecDevice, c.cecAddress
		c.mu.Unlock()
		err = c.cec(ctx, device, address, on)
	case MethodDPMS:
		err = c.dpms(ctx, on)
	}
	if err != nil {
		state := "off"
		if on {
			state = "on"
		}
		c.log("power."+method+".fail", "the display did not go "+state+": "+err.Error())
	}
}

// pick gives the method to use now. "auto" probes CEC one time and keeps the
// answer: the probe costs a subprocess and the hardware does not change while the
// daemon runs.
func (c *Controller) pick() string {
	switch method := strings.TrimSpace(c.opt.Method()); method {
	case MethodCEC, MethodDPMS, MethodNone:
		c.mu.Lock()
		c.chosen = method
		c.mu.Unlock()
		return method
	}

	c.mu.Lock()
	chosen := c.chosen
	c.mu.Unlock()
	if chosen != "" {
		return chosen
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	device, address, ok := c.probeCEC(ctx)

	c.mu.Lock()
	if ok {
		c.chosen, c.cecDevice, c.cecAddress = MethodCEC, device, address
	} else {
		c.chosen = MethodDPMS
	}
	chosen = c.chosen
	c.mu.Unlock()

	c.log("power.method", chosen+" (display.power_method is auto)")
	return chosen
}

func (c *Controller) log(event, details string) {
	if c.opt.Log != nil {
		c.opt.Log.Log(event, details)
	}
}
