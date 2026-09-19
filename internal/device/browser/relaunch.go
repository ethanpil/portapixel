package browser

import (
	"context"
	"errors"
)

// relaunchNavigator is navigation rung 2. It starts the browser again with the
// new URL. The screen goes black for a second or two at each navigation, which is
// why it is the second rung and not the first. It always works, and that is why
// it exists.
type relaunchNavigator struct {
	proc *launcher
}

func newRelaunch(proc *launcher) *relaunchNavigator {
	return &relaunchNavigator{proc: proc}
}

func (n *relaunchNavigator) Name() string { return "relaunch" }

// Start starts the browser on url. A browser that already shows this URL stays:
// the supervisor tries rung 1 first, and that attempt left a running process.
func (n *relaunchNavigator) Start(ctx context.Context, url string) error {
	if n.proc.alive() && n.proc.lastURL() == url {
		return nil
	}
	return n.proc.start(url)
}

func (n *relaunchNavigator) Navigate(ctx context.Context, url string) error {
	return n.proc.start(url)
}

// Reload starts the browser again on the same URL. There is no other way to load
// a page again from outside the browser.
func (n *relaunchNavigator) Reload(ctx context.Context) error {
	url := n.proc.lastURL()
	if url == "" {
		return errors.New("there is no page to load again")
	}
	return n.proc.start(url)
}

// CurrentURL gives the URL that the browser started with. This rung cannot see
// inside the page, so a live process is the whole of the liveness test.
func (n *relaunchNavigator) CurrentURL(ctx context.Context) (string, error) {
	if !n.proc.alive() {
		return "", errors.New("the browser is not running")
	}
	return n.proc.lastURL(), nil
}

func (n *relaunchNavigator) Alive() bool { return n.proc.alive() }

func (n *relaunchNavigator) Stop() error { return n.proc.stop() }
