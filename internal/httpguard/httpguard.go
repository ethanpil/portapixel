package httpguard

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// Header is the header that each state-changing request must carry. A form on
// another site cannot add it, so it stops cross-site request forgery.
const (
	HeaderName  = "X-PortaPixel"
	HeaderValue = "1"
)

// deny answers with a JSON error. The API speaks JSON everywhere, so an error
// must not be an HTML page.
func deny(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write([]byte(`{"error":` + jsonString(message) + `}`))
}

// jsonString quotes a short message for a JSON body.
func jsonString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// HostAllowlist rejects a request whose Host header is not in the allowlist.
// This stops DNS rebinding: a name that points at the device but that we do not
// know gets no answer.
//
// The list comes from a function, because the IP addresses and the mDNS name
// change while the daemon runs. Each name matches with or without a port, and
// the comparison ignores letter case.
func HostAllowlist(hosts func() []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !hostAllowed(r.Host, hosts()) {
				// 421 says "you are talking to the wrong server", which is what
				// a rebound name is.
				deny(w, http.StatusMisdirectedRequest, "this host name is not allowed")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// hostAllowed compares the Host header with the allowlist. It compares the full
// value and also the value with the port removed.
func hostAllowed(host string, allowed []string) bool {
	full := strings.ToLower(strings.TrimSpace(host))
	if full == "" {
		return false
	}
	bare := stripPort(full)
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if a == full || a == bare || stripPort(a) == bare {
			return true
		}
	}
	return false
}

// stripPort removes the port from a host value. It keeps a bare IPv6 address
// whole.
func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

// RequireHeader rejects a request that changes state and does not carry
// X-PortaPixel: 1. GET and HEAD requests pass, because they change nothing.
func RequireHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		default:
			if r.Header.Get(HeaderName) != HeaderValue {
				deny(w, http.StatusForbidden, "the "+HeaderName+" header is necessary")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// LoopbackOnly rejects a request that does not come from this device. The
// player API uses it: an external page in the browser must not be able to drive
// the player endpoints (D46).
func LoopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r.RemoteAddr) {
			deny(w, http.StatusForbidden, "this endpoint answers the device only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopback reports if addr, in the form that RemoteAddr uses, is a loopback
// address.
func isLoopback(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// PasswordEqual compares two passwords in constant time. A comparison that
// stops at the first different character tells an attacker how much of the
// password is correct.
func PasswordEqual(given, want string) bool {
	return subtle.ConstantTimeCompare([]byte(given), []byte(want)) == 1
}
