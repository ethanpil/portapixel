package browser

import (
	"context"
	"net/http"
	"time"
)

// Navigator is one rung of the navigation ladder (ARCHITECTURE section 7).
//
// The interface exists because the two rungs are two different mechanisms with
// one job, and because every rung must be a tested code path. A test drives the
// same calls against a stub browser.
type Navigator interface {
	// Name is "cdp" or "relaunch". It goes in the ops log and in /api/status.
	Name() string
	// Start brings the browser up and puts it on url.
	Start(ctx context.Context, url string) error
	Navigate(ctx context.Context, url string) error
	Reload(ctx context.Context) error
	// CurrentURL gives the address of the page. The relaunch rung gives the last
	// URL that it started with, while the process lives.
	CurrentURL(ctx context.Context) (string, error)
	Alive() bool
	Stop() error
}

// probeTimeout is the time that the reachability probe may take. A page that
// needs more than five seconds to answer its first byte is not a page that we
// put on a wall (D19).
const probeTimeout = 5 * time.Second

// Reachable reports if a URL item can be shown. The daemon probes before it
// navigates, so that the screen never shows the error page of the browser (D19).
//
// HEAD first, because it costs one packet and no page. Many servers do not
// answer HEAD, so a HEAD that fails or is refused goes to GET. Any answer under
// 500 counts as reachable: a page that needs a login still renders something, and
// that is the choice of the person who put the URL in the playlist. Only a
// network that does not answer, and a server that reports its own failure, count
// as unreachable.
func Reachable(url string) bool {
	client := &http.Client{Timeout: probeTimeout}
	if res, err := client.Head(url); err == nil {
		res.Body.Close()
		if res.StatusCode < 400 {
			return true
		}
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	res, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode < 500
}
