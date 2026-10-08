package power

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeCard is a DRM card with a list of connectors. It records each call, so that
// a test can read the order. It is master only after takeMaster worked.
type fakeCard struct {
	name  string
	conns []drmConnector
	log   *[]string
	// busy is the number of takeMaster calls that fail before one works. It models
	// a player that is still on its way out.
	busy int
	// failSet names a connector that refuses a property.
	failSet uint32
	master  bool
	closed  bool
}

func (c *fakeCard) record(format string, args ...any) {
	*c.log = append(*c.log, c.name+" "+fmt.Sprintf(format, args...))
}

func (c *fakeCard) connectors() ([]drmConnector, error) {
	c.record("list")
	return c.conns, nil
}

func (c *fakeCard) takeMaster() error {
	c.record("master")
	if c.busy > 0 {
		c.busy--
		return errors.New("operation not permitted")
	}
	c.master = true
	return nil
}

func (c *fakeCard) setProperty(connector, property uint32, value uint64) error {
	if !c.master {
		return errors.New("permission denied: not the DRM master")
	}
	c.record("set connector %d property %d = %d", connector, property, value)
	if connector == c.failSet {
		return errors.New("invalid argument")
	}
	return nil
}

func (c *fakeCard) close() error {
	c.record("close")
	c.closed = true
	return nil
}

func newTestBlanker(cards ...*fakeCard) *drmBlanker {
	list := make([]drmCard, len(cards))
	for i, c := range cards {
		list[i] = c
	}
	return &drmBlanker{
		open:  func() ([]drmCard, error) { return list, nil },
		wait:  200 * time.Millisecond,
		pause: time.Millisecond,
	}
}

func lines(log []string) string { return strings.Join(log, "\n") }

// Off sets DPMS to off on each connected connector and keeps the card open. On
// sets DPMS to on, and then closes the card.
func TestBlankerOffThenOn(t *testing.T) {
	var log []string
	card := &fakeCard{name: "card0", log: &log, conns: []drmConnector{
		{id: 31, connected: false, dpms: 2},
		{id: 32, connected: true, dpms: 5},
	}}
	b := newTestBlanker(card)

	if err := b.Off(); err != nil {
		t.Fatalf("Off() = %v", err)
	}
	want := "card0 list\ncard0 master\ncard0 set connector 32 property 5 = 3"
	if got := lines(log); got != want {
		t.Errorf("Off() calls:\n%s\nwant:\n%s", got, want)
	}
	if card.closed {
		t.Fatal("Off() closed the card: the kernel would switch the display on")
	}

	log = nil
	if err := b.On(); err != nil {
		t.Fatalf("On() = %v", err)
	}
	want = "card0 set connector 32 property 5 = 0\ncard0 close"
	if got := lines(log); got != want {
		t.Errorf("On() calls:\n%s\nwant:\n%s", got, want)
	}
	if !card.closed {
		t.Error("On() left the card open: the player could not take the device")
	}
}

// A second Off while the display is off, and an On while the display is on, do
// nothing.
func TestBlankerIsIdempotent(t *testing.T) {
	var log []string
	card := &fakeCard{name: "card0", log: &log, conns: []drmConnector{{id: 1, connected: true, dpms: 9}}}
	b := newTestBlanker(card)

	if err := b.On(); err != nil {
		t.Fatalf("On() with the display on = %v", err)
	}
	if len(log) != 0 {
		t.Errorf("On() with the display on made calls: %v", log)
	}
	if err := b.Off(); err != nil {
		t.Fatal(err)
	}
	n := len(log)
	if err := b.Off(); err != nil {
		t.Fatal(err)
	}
	if len(log) != n {
		t.Errorf("a second Off() made calls: %v", log[n:])
	}
	if err := b.On(); err != nil {
		t.Fatal(err)
	}
	// After On the blanker can go off again.
	log = nil
	card.master, card.closed = false, false
	if err := b.Off(); err != nil {
		t.Fatalf("Off() after On() = %v", err)
	}
	if len(log) == 0 {
		t.Error("Off() after On() made no calls")
	}
}

// A player that is still on its way out holds the master for a short time. Off
// waits and then works. A player that never lets go gives an error, and Off does
// not hold the card.
func TestBlankerWaitsForTheMaster(t *testing.T) {
	var log []string
	card := &fakeCard{name: "card0", log: &log, busy: 3,
		conns: []drmConnector{{id: 1, connected: true, dpms: 9}}}
	b := newTestBlanker(card)
	if err := b.Off(); err != nil {
		t.Fatalf("Off() = %v", err)
	}
	if got := strings.Count(lines(log), "master"); got != 4 {
		t.Errorf("Off() asked for the master %d times, want 4", got)
	}

	log = nil
	stuck := &fakeCard{name: "card1", log: &log, busy: 1 << 30,
		conns: []drmConnector{{id: 1, connected: true, dpms: 9}}}
	b = newTestBlanker(stuck)
	err := b.Off()
	if err == nil || !strings.Contains(err.Error(), "stop the player") {
		t.Fatalf("Off() = %v, want an error that names the player", err)
	}
	if !stuck.closed {
		t.Error("Off() kept a card that it could not use")
	}
	if len(b.held) != 0 {
		t.Error("Off() holds a card after an error")
	}
}

// Two cards: Off puts the displays of both off, and On puts both on. A card with
// no connected display is closed at once.
func TestBlankerWithTwoCards(t *testing.T) {
	var log []string
	a := &fakeCard{name: "card0", log: &log, conns: []drmConnector{{id: 1, connected: false, dpms: 9}}}
	b := &fakeCard{name: "card1", log: &log, conns: []drmConnector{{id: 7, connected: true, dpms: 4}}}
	blanker := newTestBlanker(a, b)

	if err := blanker.Off(); err != nil {
		t.Fatalf("Off() = %v", err)
	}
	if !a.closed {
		t.Error("the card with no display stays open")
	}
	if b.closed {
		t.Error("the card with a display is closed")
	}
	if err := blanker.On(); err != nil {
		t.Fatalf("On() = %v", err)
	}
	if !b.closed {
		t.Error("On() left a card open")
	}
}

// No connected display is an error, not a silent success. The log then says why
// the screen stays on.
func TestBlankerWithNoDisplay(t *testing.T) {
	var log []string
	card := &fakeCard{name: "card0", log: &log, conns: []drmConnector{{id: 1, connected: false, dpms: 9}}}
	b := newTestBlanker(card)
	if err := b.Off(); err == nil {
		t.Error("Off() with no connected display gave no error")
	}
	if !card.closed {
		t.Error("Off() kept a card with no display")
	}
}

// A connected connector with no DPMS property is an error too.
func TestBlankerWithNoDPMSProperty(t *testing.T) {
	var log []string
	card := &fakeCard{name: "card0", log: &log, conns: []drmConnector{{id: 3, connected: true}}}
	err := newTestBlanker(card).Off()
	if err == nil || !strings.Contains(err.Error(), "no DPMS property") {
		t.Errorf("Off() = %v, want an error about the DPMS property", err)
	}
}

// One connector that refuses must not stop the others, and On must undo the
// connectors that did go off.
func TestBlankerWithOneConnectorThatRefuses(t *testing.T) {
	var log []string
	card := &fakeCard{name: "card0", log: &log, failSet: 11, conns: []drmConnector{
		{id: 11, connected: true, dpms: 5},
		{id: 12, connected: true, dpms: 5},
	}}
	b := newTestBlanker(card)
	if err := b.Off(); err == nil {
		t.Error("Off() answered no error while a connector refused")
	}
	log = nil
	if err := b.On(); err != nil {
		t.Fatalf("On() = %v", err)
	}
	if got, want := lines(log), "card0 set connector 12 property 5 = 0\ncard0 close"; got != want {
		t.Errorf("On() calls:\n%s\nwant:\n%s", got, want)
	}
}

// A card that cannot open gives its error to Off.
func TestBlankerWhenNoCardOpens(t *testing.T) {
	b := &drmBlanker{open: func() ([]drmCard, error) { return nil, errors.New("no card") }}
	if err := b.Off(); err == nil || err.Error() != "no card" {
		t.Errorf("Off() = %v, want the error of open", err)
	}
	if err := b.On(); err != nil {
		t.Errorf("On() = %v, want nil", err)
	}
}
