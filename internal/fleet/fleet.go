// Package fleet holds the rules that every device call to a fleet server
// follows: how an address that the server names becomes a whole address, which
// requests may carry the device token, and how long a call may take.
//
// The fleet sync client and the updater both talk to the same server. Each one had
// its own copy of these rules before this, and the copies did not agree. The updater
// dropped the token on a redirect to another host and the sync client did not. The
// two joined a server path in two ways, so one manifest gave two addresses for one
// server behind a proxy. One package, one rule.
package fleet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The timeouts of the transport. None of them is a limit on the whole request: a
// video of 1 GB on a slow link is a download of many minutes and not a fault.
//
// http.Client.Timeout covers the body read, so a client with one would abort every
// large download. The limits here cover only the steps that must answer at once,
// and store.Download stops a download that makes no progress.
const (
	dialTimeout           = 10 * time.Second
	tlsTimeout            = 10 * time.Second
	responseHeaderTimeout = 30 * time.Second
	idleConnTimeout       = 90 * time.Second
)

// NewClient makes the HTTP client of every fleet call and every download.
//
// The client has no overall timeout. The caller gives each small JSON request a
// context with a deadline of its own, and a download gets the round context and the
// progress guard of store.Download.
func NewClient() *http.Client {
	return &http.Client{
		CheckRedirect: DropBearerOffHost,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
			TLSHandshakeTimeout:   tlsTimeout,
			ResponseHeaderTimeout: responseHeaderTimeout,
			IdleConnTimeout:       idleConnTimeout,
			ForceAttemptHTTP2:     true,
		},
	}
}

// MaxRedirects is how many redirects one request may follow. It is the number that
// net/http uses by default.
const MaxRedirects = 10

// DropBearerOffHost refuses a redirect that leaves the host or the scheme that the
// request started on, and it is the redirect rule of every fleet call.
//
// A redirect to another host must be an ERROR and not a request with one header
// less. ResolveURL proves that every address a manifest names is on the paired
// server. A 302 that the device followed took that proof away. The server could
// then pick any host and any port for the device to connect to. It could read the
// answer back out of sync_error and the ops log, and so scan the network of the
// site from inside. It could also send the body of an https object over http.
//
// A redirect to the SAME host and scheme stays permitted: that is a path change
// behind a reverse proxy, which a mirror does need.
func DropBearerOffHost(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return errors.New("the request followed too many redirects")
	}
	if len(via) == 0 {
		return nil
	}
	first := via[0].URL
	if !sameHost(req.URL, first) || req.URL.Scheme != first.Scheme {
		// The header goes too, in case a caller ignores this error and reuses the
		// request.
		req.Header.Del("Authorization")
		return fmt.Errorf("the server sent this device to %s://%s, which is not %s://%s",
			req.URL.Scheme, req.URL.Host, first.Scheme, first.Host)
	}
	return nil
}

// ResolveURL gives the whole address of something that a fleet server named.
//
// The manifest carries a path, for example "/api/v1/media/<sha>" or
// "/api/v1/releases/1.5.0". A whole address is permitted too, and then it must be on
// the server that this device is paired with. A manifest is not trusted input: a
// device that followed an address to another host would carry its token there and
// take its content from a stranger.
//
// The path of the base address is kept. A server behind a reverse proxy under
// https://signage.example.com/portapixel answers on that prefix, and a join that
// dropped it made every object download answer 404 while the poll worked.
func ResolveURL(base, ref string) (string, error) {
	root, err := url.Parse(strings.TrimSpace(base))
	if err != nil || root.Scheme == "" || root.Host == "" {
		return "", fmt.Errorf("%q is not a server address that this device can use", base)
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("the server named an address that is empty")
	}
	target, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("%q is not a URL", ref)
	}
	if target.User != nil {
		return "", fmt.Errorf("%q holds a user name, which this device refuses", ref)
	}

	out := target
	if target.Scheme == "" && target.Host == "" {
		// A path that the server gave. It goes under the path of the base address,
		// so a server under a proxy prefix keeps its prefix.
		out = root.JoinPath(target.EscapedPath())
		out.RawQuery = target.RawQuery
	}
	if !sameHost(out, root) || out.Scheme != root.Scheme {
		return "", fmt.Errorf("%q is not on %s", ref, root.Host)
	}
	// JoinPath and ResolveReference both apply "..", so the result can climb above
	// the prefix of the server. The address must stay under it.
	if !underPath(out.Path, root.Path) {
		return "", fmt.Errorf("%q is not under %s", ref, root.Path+"/")
	}
	return out.String(), nil
}

// underPath reports if a path is the base path or something under it. An empty
// base path is the root of the host, and everything is under it.
func underPath(path, base string) bool {
	base = strings.TrimSuffix(base, "/")
	if base == "" {
		return true
	}
	return path == base || strings.HasPrefix(path, base+"/")
}

// SameHost reports if two addresses name one host: the same name, ignoring the
// letter case, and the same port, where the default port of the scheme counts as
// the port. The rule that decides if a request may carry the device token must be
// the same rule everywhere.
func SameHost(a, b string) bool {
	first, err := url.Parse(strings.TrimSpace(a))
	if err != nil {
		return false
	}
	second, err := url.Parse(strings.TrimSpace(b))
	if err != nil {
		return false
	}
	if first.Host == "" || second.Host == "" {
		return false
	}
	return sameHost(first, second)
}

// sameHost compares two addresses by host and port. The default port of the scheme
// counts as the port, so "https://host" and "https://host:443" are one host, and
// the comparison ignores the letter case of the name.
func sameHost(a, b *url.URL) bool { return hostKey(a) == hostKey(b) }

func hostKey(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return strings.ToLower(u.Hostname()) + ":" + port
}

// ContextUntil gives a context that ends when done closes or after the limit. The
// poll loop of the sync client and the loop of the updater both need it.
func ContextUntil(done <-chan struct{}, limit time.Duration) (context.Context, context.CancelFunc) {
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
