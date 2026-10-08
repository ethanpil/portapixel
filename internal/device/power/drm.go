package power

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Blanker switches the connectors of the display on and off. It needs no
// compositor and no program: it talks to the DRM device of the kernel.
//
// The player owns the DRM device while it runs. The first program that opens the
// device becomes the DRM master, and only the master may change a connector. So
// the caller must stop the player BEFORE it calls Off, and it must call On
// BEFORE it starts the player.
type Blanker interface {
	// Off puts every connected display in the DPMS state "off". It then holds the
	// DRM device open. The kernel puts the display on again when the last open
	// handle closes, so the display stays off only while the hold lasts. A second
	// call while the display is off does nothing.
	Off() error
	// On puts the displays on again and lets go of the device, so that the player
	// can open it. It does nothing when the displays are on.
	On() error
}

// The values of the DRM property "DPMS" that this package uses.
const (
	dpmsOn  = 0
	dpmsOff = 3
)

// How long Off waits for the DRM master. A player that was stopped gives up the
// device at once. The wait is for a player that is still on its way out, because
// the call that stopped it only queued the stop.
const (
	masterWait  = 3 * time.Second
	masterPause = 100 * time.Millisecond
)

// drmCard is one DRM device node, for example /dev/dri/card0. drm_linux.go has
// the real one. A test gives a fake.
type drmCard interface {
	// connectors lists the connectors of the card.
	connectors() ([]drmConnector, error)
	// setProperty sets one property of one connector.
	setProperty(connector, property uint32, value uint64) error
	// takeMaster makes this handle the DRM master. It is no error when the handle
	// is the master already.
	takeMaster() error
	close() error
}

// drmConnector is what Off needs to know of a connector.
type drmConnector struct {
	id        uint32
	connected bool
	dpms      uint32 // the ID of the DPMS property, or 0 when the connector has none
}

// heldCard is a card that has displays in the state "off". The handle stays open,
// because the kernel puts the displays on again when the last handle closes.
type heldCard struct {
	card    drmCard
	targets []drmConnector
}

// drmBlanker is the Blanker that uses the DRM device.
type drmBlanker struct {
	// open gives the cards of the machine. A card with no display function is not
	// in the list.
	open  func() ([]drmCard, error)
	wait  time.Duration
	pause time.Duration

	mu   sync.Mutex
	held []heldCard
}

// newDRMBlanker makes the Blanker of the real machine. On a system that is not
// Linux, Off gives an error.
func newDRMBlanker() *drmBlanker {
	return &drmBlanker{open: openCards, wait: masterWait, pause: masterPause}
}

// Off puts the displays of every card in the state "off" and holds the cards.
func (b *drmBlanker) Off() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.held) > 0 {
		return nil
	}

	cards, err := b.open()
	if err != nil {
		return err
	}
	var first error
	for _, card := range cards {
		held, err := b.blank(card)
		if err != nil && first == nil {
			first = err
		}
		if held == nil {
			_ = card.close()
			continue
		}
		b.held = append(b.held, *held)
	}
	if len(b.held) == 0 && first == nil {
		return errors.New("no display is connected")
	}
	// A fault on one card does not undo the other cards. On puts them all on.
	return first
}

// blank puts the connected displays of one card in the state "off". It gives nil
// when the card has no connected display. It gives the connectors that are off
// together with an error when a later connector failed, so that On can undo them.
func (b *drmBlanker) blank(card drmCard) (*heldCard, error) {
	all, err := card.connectors()
	if err != nil {
		return nil, err
	}
	var targets []drmConnector
	var first error
	for _, c := range all {
		if !c.connected {
			continue
		}
		if c.dpms == 0 {
			if first == nil {
				first = fmt.Errorf("connector %d has no DPMS property", c.id)
			}
			continue
		}
		targets = append(targets, c)
	}
	if len(targets) == 0 {
		return nil, first
	}
	if err := b.master(card); err != nil {
		return nil, err
	}

	held := &heldCard{card: card}
	for _, c := range targets {
		if err := card.setProperty(c.id, c.dpms, dpmsOff); err != nil {
			if first == nil {
				first = fmt.Errorf("connector %d: DPMS off: %w", c.id, err)
			}
			continue
		}
		held.targets = append(held.targets, c)
	}
	if len(held.targets) == 0 {
		return nil, first
	}
	return held, first
}

// master waits until the handle is the DRM master. A refusal means that another
// program, most likely the player, still holds the device.
func (b *drmBlanker) master(card drmCard) error {
	deadline := time.Now().Add(b.wait)
	for {
		err := card.takeMaster()
		if err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the display is busy; stop the player first: %w", err)
		}
		time.Sleep(b.pause)
	}
}

// On puts the displays on again and closes the cards. It closes every card, also
// when a displays does not answer: a handle that stays open would keep the display
// off and would keep the player from the device.
func (b *drmBlanker) On() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	var first error
	for _, h := range b.held {
		for _, c := range h.targets {
			if err := h.card.setProperty(c.id, c.dpms, dpmsOn); err != nil && first == nil {
				first = fmt.Errorf("connector %d: DPMS on: %w", c.id, err)
			}
		}
		if err := h.card.close(); err != nil && first == nil {
			first = err
		}
	}
	b.held = nil
	return first
}
