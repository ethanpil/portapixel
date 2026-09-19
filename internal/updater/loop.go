package updater

import (
	"context"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// stepTimeout is the longest that one pass of the loop may take. A download of
// 25 MB on a slow link needs minutes; a download that never ends must not hold the
// loop for ever.
const stepTimeout = 10 * time.Minute

// Run drives the automatic update and the sideload watch until done is closed.
//
// Three jobs, one loop:
//
//   - The sideload directory is read at every tick. A person who drops a bundle on
//     the stick must not have to wait or press anything (D52).
//   - With [updates] auto = true the loop checks the release source once a day, at
//     a minute that this device picked at random. A thousand devices must not ask
//     GitHub in the same second.
//   - It applies a release that a check found at the time of the nightly restart.
//     The screen is dark or the day is over, and the device restarts anyway then
//     (D28, D30).
//
// Nothing here may hold up the start of the daemon. Run is a goroutine and every
// fault is an ops log line.
func (m *Manager) Run(done <-chan struct{}, src Source) {
	m.RunSource(done, func() Source { return src })
}

// RunSource is Run with a source that can change while the daemon runs.
//
// A paired device takes the release that its fleet server approved, and the server
// can approve another version at any time, or the device can be unpaired. So the
// loop asks for the source at each pass and never holds a copy of it (D28).
func (m *Manager) RunSource(done <-chan struct{}, source func() Source) {
	t := time.NewTicker(m.opt.Tick)
	defer t.Stop()

	for {
		select {
		case <-done:
			return
		case <-t.C:
			m.step(done, source())
		}
	}
}

// step is one pass of the loop.
func (m *Manager) step(done <-chan struct{}, src Source) {
	ctx, cancel := contextUntil(done, stepTimeout)
	defer cancel()

	if err := m.Sideload(ctx); err != nil {
		// Sideload already wrote the reason in the ops log.
		return
	}
	if m.opt.Auto == nil || !m.opt.Auto() {
		return
	}

	now := m.opt.Now()
	day := now.Format("2006-01-02")
	minute := now.Hour()*60 + now.Minute()

	m.mu.Lock()
	checkDue := m.lastCheckDay != day && minute >= m.checkMinute
	if checkDue {
		m.lastCheckDay = day
	}
	m.mu.Unlock()

	if checkDue {
		if _, err := m.Check(ctx, src); err != nil {
			return
		}
	}
	m.applyWhenDue(ctx, day, minute)
}

// applyWhenDue installs a release that a check found, at the time of the nightly
// restart and one time on a day.
func (m *Manager) applyWhenDue(ctx context.Context, day string, minute int) {
	if m.opt.ApplyMinute == nil {
		return
	}
	target, ok := m.opt.ApplyMinute()
	if !ok {
		// No apply time. The person turned the nightly restart off, so there is no
		// moment at which a restart costs nothing.
		return
	}
	// A two minute window, so that a busy loop cannot miss the time.
	if minute < target || minute > target+1 {
		return
	}

	m.mu.Lock()
	offered := m.offered
	ready := m.state.State == manifest.UpdateAvailable && offered != nil && m.lastApplyDay != day
	if ready {
		m.lastApplyDay = day
	}
	m.mu.Unlock()

	if !ready {
		return
	}
	m.opt.Log("update.auto", "the automatic update installs "+offered.Version)
	_ = m.Apply(ctx, *offered)
}

// contextUntil gives a context that ends when done closes or after the limit.
func contextUntil(done <-chan struct{}, limit time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
