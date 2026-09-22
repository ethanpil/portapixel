package power

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// recorder is the fake program runner and the fake browser in one object, so that
// a test can read the ORDER of the two. The order is the whole point of this
// package: wlr-randr talks to the compositor of the browser session.
type recorder struct {
	steps []string
	// answers maps a program name to the output that it gives. A name that is not
	// in the map gives no output and no error.
	answers map[string]string
	// fails names the programs that fail.
	fails map[string]bool
	// browserErr is what Suspend and Resume give back. browser.ErrBusy is the real
	// one: the command queue of the browser is full while a cold launch runs.
	browserErr error
}

func newRecorder() *recorder {
	return &recorder{answers: map[string]string{}, fails: map[string]bool{}}
}

func (r *recorder) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.steps = append(r.steps, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	if r.fails[name] {
		return []byte("the tool says no"), errors.New("exit status 1")
	}
	return []byte(r.answers[name]), nil
}

func (r *recorder) Suspend() error {
	r.steps = append(r.steps, "browser suspend")
	return r.browserErr
}

func (r *recorder) Resume() error {
	r.steps = append(r.steps, "browser resume")
	return r.browserErr
}

// joined gives the steps as one line, so a test can look for a sequence.
func (r *recorder) joined() string { return strings.Join(r.steps, " | ") }

const randrOutput = "HDMI-A-1 \"Acme 27\"\n  Make: Acme\n"

// The off path must switch the display BEFORE it stops the browser, and the on
// path must start the browser first. A browser that stopped first takes the
// compositor with it, and wlr-randr then has nothing to talk to: the display
// stays on all night.
func TestTransitionOrder(t *testing.T) {
	tests := []struct {
		name   string
		method string
		answer string
		off    []string
		on     []string
	}{
		{
			name:   "dpms switches the output and then stops the browser",
			method: MethodDPMS,
			answer: randrOutput,
			off:    []string{"wlr-randr --output HDMI-A-1 --off", "browser suspend"},
			on:     []string{"browser resume", "wlr-randr --output HDMI-A-1 --on"},
		},
		{
			name:   "cec sends standby and then stops the browser",
			method: MethodCEC,
			off:    []string{"cec-ctl -d /dev/cec0 --playback --to 0 --standby", "browser suspend"},
			on:     []string{"browser resume", "cec-ctl -d /dev/cec0 --playback --to 0 --image-view-on"},
		},
		{
			name:   "none stops the browser and runs no program",
			method: MethodNone,
			off:    []string{"browser suspend"},
			on:     []string{"browser resume"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRecorder()
			r.answers[randrTool] = tt.answer
			c := New(Options{
				Method:     func() string { return tt.method },
				Run:        r,
				Browser:    r,
				CECDevices: func() []string { return []string{"/dev/cec0"} },
			})
			// The CEC path needs the device that the probe found. A fixed method
			// skips the probe, so the test sets the node the same way the daemon
			// does when display.power_method is "cec".
			c.cecDevice = "/dev/cec0"

			if err := c.Set(false, "a test"); err != nil {
				t.Fatalf("off: %v", err)
			}
			checkSequence(t, "off", r.joined(), tt.off)
			if c.ScreenOn() {
				t.Error("ScreenOn is true after the screen went off")
			}

			r.steps = nil
			if err := c.Set(true, "a test"); err != nil {
				t.Fatalf("on: %v", err)
			}
			checkSequence(t, "on", r.joined(), tt.on)
			if !c.ScreenOn() {
				t.Error("ScreenOn is false after the screen came on")
			}
		})
	}
}

// checkSequence reports a missing step or a wrong order.
func checkSequence(t *testing.T, what, got string, want []string) {
	t.Helper()
	at := 0
	for _, step := range want {
		found := strings.Index(got[at:], step)
		if found < 0 {
			t.Fatalf("%s steps = %q; %q is missing or out of order", what, got, step)
		}
		at += found + len(step)
	}
}

// A display that does not switch must not keep a browser running. The fault is a
// log line; the browser still stops.
func TestADisplayFaultStillStopsTheBrowser(t *testing.T) {
	r := newRecorder()
	r.answers[randrTool] = randrOutput
	r.fails[randrTool] = true
	c := New(Options{Method: func() string { return MethodDPMS }, Run: r, Browser: r})

	if err := c.Set(false, "a test"); err != nil {
		t.Fatalf("off: %v", err)
	}
	if !strings.Contains(r.joined(), "browser suspend") {
		t.Errorf("steps = %q, want a browser suspend", r.joined())
	}
	if c.ScreenOn() {
		t.Error("ScreenOn is true after the screen went off")
	}
}

// A manual command must hold until the schedule crosses an edge. "Screen on" at
// midnight has to last until the morning: a hold that ended at the next tick made
// the button useless.
func TestManualCommandHoldsUntilTheNextEdge(t *testing.T) {
	r := newRecorder()
	on := true
	c := New(Options{
		Method:     func() string { return MethodNone },
		ShouldBeOn: func(time.Time) bool { return on },
		Run:        r,
		Browser:    r,
		Tick:       time.Hour,
	})
	c.Start()

	// A person switches the screen off while the schedule wants it on.
	if err := c.Set(false, "the admin asked"); err != nil {
		t.Fatal(err)
	}
	c.step()
	if c.ScreenOn() {
		t.Fatal("a tick with no edge took the screen back on")
	}
	c.step()
	if c.ScreenOn() {
		t.Fatal("a second tick with no edge took the screen back on")
	}

	// The schedule crosses into its off window. That is the edge that ends the
	// hold, and the screen is already off, so nothing changes.
	on = false
	c.step()
	if c.ScreenOn() {
		t.Fatal("the screen came on when the schedule asked for off")
	}

	// The schedule crosses back. The hold is gone, so the screen follows.
	on = true
	c.step()
	if !c.ScreenOn() {
		t.Fatal("the screen did not follow the schedule after the hold ended")
	}
}

// With no screen schedule the answer is always "on", so the loop must never
// switch anything off.
func TestNoScheduleKeepsTheScreenOn(t *testing.T) {
	r := newRecorder()
	c := New(Options{
		Method:  func() string { return MethodNone },
		Run:     r,
		Browser: r,
		Tick:    time.Hour,
	})
	c.Start()
	for range 5 {
		c.step()
	}
	if !c.ScreenOn() {
		t.Error("ScreenOn is false")
	}
	if len(r.steps) != 0 {
		t.Errorf("steps = %q, want none", r.joined())
	}
}

// A device that boots inside its night hours must not show a picture.
func TestStartFollowsTheSchedule(t *testing.T) {
	r := newRecorder()
	c := New(Options{
		Method:     func() string { return MethodNone },
		ShouldBeOn: func(time.Time) bool { return false },
		Run:        r,
		Browser:    r,
	})
	c.Start()
	if c.ScreenOn() {
		t.Error("ScreenOn is true after a start inside the off window")
	}
	if !strings.Contains(r.joined(), "browser suspend") {
		t.Errorf("steps = %q, want a browser suspend", r.joined())
	}
}

// "auto" must pick CEC only when a display acknowledges, and the answer must be
// kept: the probe costs a subprocess and the hardware does not change.
func TestAutoPicksTheMethod(t *testing.T) {
	tests := []struct {
		name    string
		devices []string
		probe   string
		fails   bool
		want    string
	}{
		{
			name:    "a display acknowledges",
			devices: []string{"/dev/cec0"},
			probe:   "\tPhysical Address                    : 1.0.0.0\n",
			want:    MethodCEC,
		},
		{
			name:    "the adapter reports no display",
			devices: []string{"/dev/cec0"},
			probe:   "\tPhysical Address                    : f.f.f.f\n",
			want:    MethodDPMS,
		},
		{
			name:    "there is no CEC device",
			devices: nil,
			want:    MethodDPMS,
		},
		{
			name:    "cec-ctl is not installed",
			devices: []string{"/dev/cec0"},
			fails:   true,
			want:    MethodDPMS,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRecorder()
			r.answers[cecTool] = tt.probe
			r.answers[randrTool] = randrOutput
			r.fails[cecTool] = tt.fails
			c := New(Options{
				Method:     func() string { return MethodAuto },
				Run:        r,
				Browser:    r,
				CECDevices: func() []string { return tt.devices },
			})

			if got := c.pick(); got != tt.want {
				t.Fatalf("pick() = %q, want %q", got, tt.want)
			}
			// The second call must not probe again.
			before := len(r.steps)
			if got := c.pick(); got != tt.want {
				t.Fatalf("the second pick() = %q, want %q", got, tt.want)
			}
			if len(r.steps) != before {
				t.Errorf("the second pick() ran %q", r.steps[before:])
			}
		})
	}
}

// The CEC on path wakes the display and then tells it which input to show.
func TestCECOnSendsActiveSource(t *testing.T) {
	r := newRecorder()
	r.answers[cecTool] = "\tPhysical Address  : 2.0.0.0\n"
	c := New(Options{
		Method:     func() string { return MethodAuto },
		Run:        r,
		Browser:    r,
		CECDevices: func() []string { return []string{"/dev/cec1"} },
	})
	if err := c.Set(false, "a test"); err != nil {
		t.Fatal(err)
	}
	r.steps = nil
	if err := c.Set(true, "a test"); err != nil {
		t.Fatal(err)
	}
	checkSequence(t, "on", r.joined(), []string{
		"cec-ctl -d /dev/cec1 --playback --to 0 --image-view-on",
		"cec-ctl -d /dev/cec1 --playback --active-source phys-addr=2.0.0.0",
	})
}

func TestPhysicalAddress(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"a display answers", "\tPhysical Address   : 1.0.0.0\n", "1.0.0.0"},
		{"no display", "\tPhysical Address   : f.f.f.f\n", ""},
		{"no display in capitals", "Physical Address : F.F.F.F", ""},
		{"an empty value", "Physical Address :", ""},
		{"another line with a colon", "Driver Info:\n  CEC Version : 2.0\n", ""},
		{"nothing at all", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := physicalAddress(tt.text); got != tt.want {
				t.Errorf("physicalAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFirstOutput(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
		ok   bool
	}{
		{"one output", "HDMI-A-1 \"Acme\"\n  Make: Acme\n", "HDMI-A-1", true},
		{"windows line ends", "DP-1 \"x\"\r\n  Make: y\r\n", "DP-1", true},
		{"only indented lines", "  Make: Acme\n", "", false},
		{"nothing", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := firstOutput(tt.text)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok = %v", err, tt.ok)
			}
			if got != tt.want {
				t.Errorf("firstOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A manual command that meets a transition of the schedule must wait for it and
// then act on the state that the transition left.
//
// Before this the command read the state from before the transition, saw the state
// that it asked for, answered 200 and did nothing at all. The manual hold then kept
// the screen in the state of the transition until the next schedule edge, which is
// hours away.
func TestSetWaitsForATransitionThatRuns(t *testing.T) {
	r := newRecorder()
	r.answers[randrTool] = randrOutput

	// The display call of the loop blocks until the test lets it go.
	inCall := make(chan struct{})
	release := make(chan struct{})
	blocking := &blockingRunner{inner: r, inCall: inCall, release: release}

	want := true
	c := New(Options{
		Method:     func() string { return MethodDPMS },
		ShouldBeOn: func(time.Time) bool { return want },
		Now:        time.Now,
		Run:        blocking,
		Browser:    r,
	})
	c.Start()

	// The schedule crosses an edge and the loop starts to switch the screen off.
	want = false
	go c.step()
	<-inCall

	// The admin asks for the screen on while the display call runs.
	done := make(chan error, 1)
	go func() { done <- c.Set(true, "the admin asked for the screen on") }()

	// Nothing may answer before the transition ends.
	select {
	case err := <-done:
		t.Fatalf("Set() answered %v while a transition was running", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Set() = %v", err)
	}
	if !c.ScreenOn() {
		t.Error("the screen is off after a command that asked for it on")
	}
}

// blockingRunner holds the first call inside the runner, so that a test can make
// two goroutines meet in the middle of a transition.
type blockingRunner struct {
	inner   Runner
	inCall  chan struct{}
	release chan struct{}
	held    bool
}

func (b *blockingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !b.held && name == randrTool && len(args) > 1 {
		b.held = true
		close(b.inCall)
		<-b.release
	}
	return b.inner.Run(ctx, name, args...)
}

// A browser that refuses the message means that the transition did NOT happen. The
// state must stay where it was, so that the next tick of the loop tries again.
//
// This branch recorded the new state and wrote "power.on" anyway. The command queue
// of the browser is full while a cold launch runs, so the screen-on at on_time got
// ErrBusy. step() then saw the state that the schedule wanted and returned. Nothing
// ever tried again: /api/status said screen_on true and the screen was black for
// the whole day.
func TestABrowserThatRefusesKeepsTheOldState(t *testing.T) {
	r := newRecorder()
	r.answers["wlr-randr"] = randrOutput
	c := New(Options{Method: func() string { return MethodDPMS }, Run: r, Browser: r})

	// Off first, which works, so the state is a known one.
	if err := c.Set(false, "a test"); err != nil {
		t.Fatalf("Set(false) = %v", err)
	}
	if c.ScreenOn() {
		t.Fatal("the screen is still on")
	}

	// The browser is busy. Going on must fail and must change nothing.
	r.browserErr = errors.New("the browser is busy; ask again in a moment")
	if err := c.Set(true, "the screen schedule"); err == nil {
		t.Fatal("Set(true) answered no error while the browser refused")
	}
	if c.ScreenOn() {
		t.Error("the controller recorded the screen as on after a browser that refused")
	}

	// The browser answers again, so the next attempt works.
	r.browserErr = nil
	if err := c.Set(true, "the screen schedule"); err != nil {
		t.Fatalf("the second attempt = %v", err)
	}
	if !c.ScreenOn() {
		t.Error("the screen is not on after an attempt that worked")
	}
}
