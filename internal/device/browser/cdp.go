package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/ethanpil/portapixel/internal/opslog"
)

// Times of the DevTools Protocol.
const (
	// cdpStartTimeout is how long we wait for the browser to open its debug port
	// and show a page. A cold Chromium on a Pi Zero 2 W takes a long time.
	cdpStartTimeout = 45 * time.Second
	// cdpPoll is the time between two looks at /json while the browser starts.
	cdpPoll = 500 * time.Millisecond
	// cdpCallTimeout is the time that one protocol call may take.
	cdpCallTimeout = 10 * time.Second
	// cdpTargetTimeout is the time that the /json request may take.
	cdpTargetTimeout = 3 * time.Second
)

// cdpNavigator is navigation rung 1. It speaks the Chrome DevTools Protocol on
// the loopback port. One call per navigation and no extra process.
type cdpNavigator struct {
	proc     *launcher
	endpoint string // for example http://127.0.0.1:9222
	log      *opslog.Log

	// startTimeout is how long Start waits for a page target. 0 uses
	// cdpStartTimeout.
	startTimeout time.Duration

	mu     sync.Mutex
	conn   *websocket.Conn
	nextID int64
	lastOK string // the URL of the last navigation that the browser accepted
}

// newCDP makes rung 1.
func newCDP(proc *launcher, endpoint string, timeout time.Duration, log *opslog.Log) *cdpNavigator {
	if timeout <= 0 {
		timeout = cdpStartTimeout
	}
	return &cdpNavigator{proc: proc, endpoint: endpoint, startTimeout: timeout, log: log}
}

func (n *cdpNavigator) Name() string { return "cdp" }

// Start starts the browser and waits for a page target. An error means that the
// protocol did not answer, and the supervisor then falls to rung 2. The browser
// process stays: it already shows the URL.
func (n *cdpNavigator) Start(ctx context.Context, url string) error {
	if !n.proc.alive() || n.proc.lastURL() != url {
		if err := n.proc.start(url); err != nil {
			return err
		}
	}
	n.dropConn() // a new process means a new socket
	wait, cancel := context.WithTimeout(ctx, n.startTimeout)
	defer cancel()

	for {
		if !n.proc.alive() {
			return fmt.Errorf("the browser ended before the debug port answered: %s", n.proc.exitReason())
		}
		if _, err := pageTarget(wait, n.endpoint); err == nil {
			n.setLast(url)
			return nil
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("no page target on %s after %s", n.endpoint, n.startTimeout)
		case <-time.After(cdpPoll):
		}
	}
}

// Navigate sends the browser to url.
func (n *cdpNavigator) Navigate(ctx context.Context, url string) error {
	if _, err := n.call(ctx, "Page.navigate", map[string]any{"url": url}); err != nil {
		return err
	}
	n.setLast(url)
	return nil
}

// Reload loads the page again. A dashboard with a long dwell time needs it
// (D42).
func (n *cdpNavigator) Reload(ctx context.Context) error {
	_, err := n.call(ctx, "Page.reload", map[string]any{"ignoreCache": false})
	return err
}

// CurrentURL asks the page where it is. This is the liveness test of a URL
// window: our JavaScript is not on the page then, so the protocol is the only
// thing that can answer.
func (n *cdpNavigator) CurrentURL(ctx context.Context) (string, error) {
	raw, err := n.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    "location.href",
		"returnByValue": true,
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("the browser gave an answer that we cannot read: %w", err)
	}
	return out.Result.Value, nil
}

func (n *cdpNavigator) Alive() bool { return n.proc.alive() }

// Stop closes the socket and ends the browser.
func (n *cdpNavigator) Stop() error {
	n.dropConn()
	return n.proc.stop()
}

// call sends one protocol command and waits for the answer with the same id.
//
// The socket breaks when the browser restarts a renderer or when the page
// crashes. The first failure closes the socket and the call runs again on a new
// socket, so one lost socket is not a lost navigation.
func (n *cdpNavigator) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	raw, err := n.callOnce(ctx, method, params)
	if err == nil {
		return raw, nil
	}
	n.dropConn()
	return n.callOnce(ctx, method, params)
}

func (n *cdpNavigator) callOnce(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	conn, err := n.connect(ctx)
	if err != nil {
		return nil, err
	}
	n.nextID++
	id := n.nextID

	conn.SetDeadline(time.Now().Add(cdpCallTimeout))
	if err := websocket.JSON.Send(conn, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, fmt.Errorf("send %s: %w", method, err)
	}
	for {
		var reply struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := websocket.JSON.Receive(conn, &reply); err != nil {
			return nil, fmt.Errorf("read the answer to %s: %w", method, err)
		}
		if reply.ID != id {
			continue // an event, or the answer to an older call
		}
		if reply.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, reply.Error.Message)
		}
		return reply.Result, nil
	}
}

// connect gives a live socket. The caller holds the lock.
func (n *cdpNavigator) connect(ctx context.Context) (*websocket.Conn, error) {
	if n.conn != nil {
		return n.conn, nil
	}
	wsURL, err := pageTarget(ctx, n.endpoint)
	if err != nil {
		return nil, err
	}
	// The origin is only a formality for a local debug port, but the protocol
	// needs one.
	conn, err := websocket.Dial(wsURL, "", n.endpoint)
	if err != nil {
		return nil, fmt.Errorf("open the browser socket: %w", err)
	}
	n.conn = conn
	return conn, nil
}

// dropConn closes the socket. The next call opens a new one.
func (n *cdpNavigator) dropConn() {
	n.mu.Lock()
	conn := n.conn
	n.conn = nil
	n.mu.Unlock()

	if conn != nil {
		conn.Close()
	}
}

func (n *cdpNavigator) setLast(url string) {
	n.mu.Lock()
	n.lastOK = url
	n.mu.Unlock()
}

// pageTarget asks the browser for the WebSocket address of its page.
func pageTarget(ctx context.Context, endpoint string) (string, error) {
	get, cancel := context.WithTimeout(ctx, cdpTargetTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(get, http.MethodGet, strings.TrimSuffix(endpoint, "/")+"/json", nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ask %s for its targets: %w", endpoint, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %d", endpoint, res.StatusCode)
	}

	var targets []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
		WS   string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(res.Body).Decode(&targets); err != nil {
		return "", fmt.Errorf("read the target list: %w", err)
	}
	for _, t := range targets {
		if t.Type == "page" && t.WS != "" {
			return t.WS, nil
		}
	}
	return "", fmt.Errorf("%s shows no page yet", endpoint)
}
