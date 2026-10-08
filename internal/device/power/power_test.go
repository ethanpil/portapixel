package power

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// recorder is the fake program runner, the fake player and the fake DRM display
// in one object, so that a test can read the ORDER of the three. The order is the
// whole point of this package: only the DRM master may switch a display, and the
// player is the master while it runs.
type recorder struct {
	steps []string
	// answers maps a program name to the output that it gives. A name that is not
	// in the map gives no output and no error.
	answers map[string]string
	// fails names the programs that fail. The name "dpms" makes the Blanker fail.
	fails map[string]bool
	// playerErr is what Suspend and Resume give back. The real player gives an
	// error when its command queue is full while a cold launch runs.
	playerErr error
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
	r.steps = append(r.steps, "player suspend")
	return r.playerErr
}

func (r *recorder) Resume() error {
	r.steps = append(r.steps, "player resume")
	return r.playerErr
}

func (r *recorder) Off() error {
	r.steps = append(r.steps, "dpms off")
	if r.fails["dpms"] {
		return errors.New("the display says no")
	}
	return nil
}

func (r *recorder) On() error {
	r.steps = append(r.steps, "dpms on")
	if r.fails["dpms"] {
		return errors.New("the display says no")
	}
	return nil
}

// joined gives the steps as one line, so a test can look for a sequence.
func (r *recorder) joined() string { return strings.Join(r.steps, " | ") }

// The off path must stop the player BEFORE it switches the display, and the on
// path must switch the display before it starts the player. The player is the
// DRM master while it runs. A DPMS call while the player runs fails, or it fights
// the player for the device.
func TestTransitionOrder(t *testing.T) {
	tests := []struct {
		name   string
		method string
		off    []string
		on     []string
	}{
		{
			name:   "dpms stops the player and then switches the display",
			method: MethodDPMS,
			off:    []string{"player suspend", "dpms off"},
			on:     []string{"dpms on", "player resume"},
		},
		{
			name:   "cec stops the player and then sends standby",
			method: MethodCEC,
			off:    []string{"player suspend", "cec-ctl -d /dev/cec0 --playback --to 0 --standby"},
			on:     []string{"cec-ctl -d /dev/cec0 --playback --to 0 --image-view-on", "player resume"},
		},
		{
			name:   "none stops the player and runs no program",
			method: MethodNone,
			off:    []string{"player suspend"},
			on:     []string{"player resume"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRecorder()
			c := New(Options{
				Method:     func() string { return tt.method },
				Run:        r,
				Blank:      r,
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
			if got, want := len(r.steps), len(tt.off); got != want {
				t.Errorf("off ran %d steps (%q), want %d", got, r.joined(), want)
			}
			if c.ScreenOn() {
				t.Error("ScreenOn is true after the screen went off")
			}

			r.steps = nil
			if err := c.Set(true, "a test"); err != nil {
				t.Fatalf("on: %v", err)
			}
			checkSequence(t, "on", r.joined(), tt.on)
			if got, want := len(r.steps), len(tt.on); got != want {
				t.Errorf("on ran %d steps (%q), want %d", got, r.joined(), want)
			}
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

// A display that does not switch must not keep a player running. The fault is a
// log line; the player is stopped and stays stopped.
func TestADisplayFaultStillStopsThePlayer(t *testing.T) {
	r := newRecorder()
	r.fails["dpms"] = true
	c := New(Options{Method: func() string { return MethodDPMS }, Run: r, Blank: r, Browser: r})

	if err := c.Set(false, "a test"); err != nil {
		t.Fatalf("off: %v", err)
	}
	if !strings.Contains(r.joined(), "player suspend") {
		t.Errorf("steps = %q, want a player suspend", r.joined())
	}
	if c.ScreenOn() {
		t.Error("ScreenOn is true after the screen went off")
	}

	// A display that does not come on must not keep the player stopped.
	r.steps = nil
	if err := c.Set(true, "a test"); err != nil {
		t.Fatalf("on: %v", err)
	}
	if !strings.Contains(r.joined(), "player resume") {
		t.Errorf("steps = %q, want a player resume", r.joined())
	}
	if !c.ScreenOn() {
		t.Error("ScreenOn is false after the screen came on")
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
	if !strings.Contains(r.joined(), "player suspend") {
		t.Errorf("steps = %q, want a player suspend", r.joined())
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
			r.fails[cecTool] = tt.fails
			c := New(Options{
				Method:     func() string { return MethodAuto },
				Run:        r,
				Blank:      r,
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
		Blank:      r,
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

// A manual command that meets a transition of the schedule must wait for it and
// then act on the state that the transition left.
//
// Before this the command read the state from before the transition, saw the state
// that it asked for, answered 200 and did nothing at all. The manual hold then kept
// the screen in the state of the transition until the next schedule edge, which is
// hours away.
func TestSetWaitsForATransitionThatRuns(t *testing.T) {
	r := newRecorder()

	// The display call of the loop blocks until the test lets it go.
	inCall := make(chan struct{})
	release := make(chan struct{})
	blocking := &blockingBlanker{inner: r, inCall: inCall, release: release}

	want := true
	c := New(Options{
		Method:     func() string { return MethodDPMS },
		ShouldBeOn: func(time.Time) bool { return want },
		Now:        time.Now,
		Run:        r,
		Blank:      blocking,
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

// blockingBlanker holds the first Off call, so that a test can make two
// goroutines meet in the middle of a transition.
type blockingBlanker struct {
	inner   Blanker
	inCall  chan struct{}
	release chan struct{}
	held    bool
}

func (b *blockingBlanker) Off() error {
	if !b.held {
		b.held = true
		close(b.inCall)
		<-b.release
	}
	return b.inner.Off()
}

func (b *blockingBlanker) On() error { return b.inner.On() }

// A player that refuses the message means that the transition did NOT happen. The
// state must stay where it was, so that the next tick of the loop tries again.
//
// This branch recorded the new state and wrote "power.on" anyway. The command queue
// of the player is full while a cold launch runs, so the screen-on at on_time got
// an error. step() then saw the state that the schedule wanted and returned.
// Nothing ever tried again: /api/status said screen_on true and the screen was
// black for the whole day.
func TestAPlayerThatRefusesKeepsTheOldState(t *testing.T) {
	r := newRecorder()
	c := New(Options{Method: func() string { return MethodDPMS }, Run: r, Blank: r, Browser: r})

	// Off first, which works, so the state is a known one.
	if err := c.Set(false, "a test"); err != nil {
		t.Fatalf("Set(false) = %v", err)
	}
	if c.ScreenOn() {
		t.Fatal("the screen is still on")
	}

	// The player is busy. Going on must fail and must change nothing.
	r.playerErr = errors.New("the player is busy; ask again in a moment")
	if err := c.Set(true, "the screen schedule"); err == nil {
		t.Fatal("Set(true) answered no error while the player refused")
	}
	if c.ScreenOn() {
		t.Error("the controller recorded the screen as on after a player that refused")
	}

	// The player answers again, so the next attempt works.
	r.playerErr = nil
	if err := c.Set(true, "the screen schedule"); err != nil {
		t.Fatalf("the second attempt = %v", err)
	}
	if !c.ScreenOn() {
		t.Error("the screen is not on after an attempt that worked")
	}
}

// A player that refuses to stop must leave the display alone. The display call
// would fail or fight the player, and the state stays "on" for the next tick.
func TestAPlayerThatRefusesToStopLeavesTheDisplayOn(t *testing.T) {
	r := newRecorder()
	r.playerErr = errors.New("the player is busy; ask again in a moment")
	c := New(Options{Method: func() string { return MethodDPMS }, Run: r, Blank: r, Browser: r})

	if err := c.Set(false, "a test"); err == nil {
		t.Fatal("Set(false) answered no error while the player refused")
	}
	if strings.Contains(r.joined(), "dpms") {
		t.Errorf("steps = %q, want no display call", r.joined())
	}
	if !c.ScreenOn() {
		t.Error("the controller recorded the screen as off after a player that refused")
	}
}
