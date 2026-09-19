package httpguard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// okHandler answers 200 and writes a mark, so a test can see that the request
// got through the middleware.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("through"))
})

func TestHostAllowlist(t *testing.T) {
	allowed := []string{"localhost", "127.0.0.1", "192.168.1.50", "lobby.local", "portapixel-a1b2.local"}

	tests := []struct {
		name string
		host string
		want int
	}{
		{name: "localhost", host: "localhost", want: 200},
		{name: "localhost with the port", host: "localhost:80", want: 200},
		{name: "loopback address", host: "127.0.0.1", want: 200},
		{name: "loopback address with a port", host: "127.0.0.1:8080", want: 200},
		{name: "device address", host: "192.168.1.50", want: 200},
		{name: "mdns name", host: "lobby.local", want: 200},
		{name: "mdns name in capitals", host: "Lobby.Local", want: 200},
		{name: "default mdns name", host: "portapixel-a1b2.local:80", want: 200},
		{name: "another name", host: "evil.example.com", want: 421},
		{name: "another name with a port", host: "evil.example.com:80", want: 421},
		{name: "a name that only starts the same", host: "lobby.local.evil.com", want: 421},
		{name: "empty host", host: "", want: 421},
		{name: "another address", host: "10.0.0.1", want: 421},
	}

	h := HostAllowlist(func() []string { return allowed })(okHandler)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
			r.Host = tt.host
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("host %q gave %d, want %d", tt.host, w.Code, tt.want)
			}
		})
	}
}

func TestHostAllowlistIPv6(t *testing.T) {
	h := HostAllowlist(func() []string { return []string{"[fe80::1]:80"} })(okHandler)
	for _, host := range []string{"[fe80::1]:80", "[fe80::1]"} {
		r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("host %q gave %d, want 200", host, w.Code)
		}
	}
}

func TestRequireHeader(t *testing.T) {
	tests := []struct {
		name   string
		method string
		header string
		want   int
	}{
		{name: "get needs nothing", method: http.MethodGet, want: 200},
		{name: "head needs nothing", method: http.MethodHead, want: 200},
		{name: "post with the header", method: http.MethodPost, header: "1", want: 200},
		{name: "put with the header", method: http.MethodPut, header: "1", want: 200},
		{name: "delete with the header", method: http.MethodDelete, header: "1", want: 200},
		{name: "post without the header", method: http.MethodPost, want: 403},
		{name: "post with the wrong value", method: http.MethodPost, header: "0", want: 403},
		{name: "put without the header", method: http.MethodPut, want: 403},
	}

	h := RequireHeader(okHandler)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "/api/config", nil)
			if tt.header != "" {
				r.Header.Set(HeaderName, tt.header)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("%s gave %d, want %d", tt.method, w.Code, tt.want)
			}
		})
	}
}

func TestLoopbackOnly(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		want       int
	}{
		{name: "loopback", remoteAddr: "127.0.0.1:41234", want: 200},
		{name: "another loopback address", remoteAddr: "127.0.0.55:41234", want: 200},
		{name: "loopback over IPv6", remoteAddr: "[::1]:41234", want: 200},
		{name: "no port", remoteAddr: "127.0.0.1", want: 200},
		{name: "LAN address", remoteAddr: "192.168.1.9:41234", want: 403},
		{name: "not an address", remoteAddr: "somewhere", want: 403},
		{name: "empty", remoteAddr: "", want: 403},
	}

	h := LoopbackOnly(okHandler)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/player/manifest", nil)
			r.RemoteAddr = tt.remoteAddr
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("%q gave %d, want %d", tt.remoteAddr, w.Code, tt.want)
			}
		})
	}
}

func TestDenyAnswersJSON(t *testing.T) {
	h := LoopbackOnly(okHandler)
	r := httptest.NewRequest(http.MethodGet, "/api/player/manifest", nil)
	r.RemoteAddr = "192.168.1.9:41234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if !strings.HasPrefix(w.Body.String(), `{"error":"`) {
		t.Fatalf("body = %q, want a JSON error", w.Body.String())
	}
}

func TestPasswordEqual(t *testing.T) {
	tests := []struct {
		name  string
		given string
		want  string
		equal bool
	}{
		{name: "same", given: "portapixel", want: "portapixel", equal: true},
		{name: "different", given: "portapixel", want: "PortaPixel"},
		{name: "a prefix is not enough", given: "porta", want: "portapixel"},
		{name: "longer", given: "portapixel1", want: "portapixel"},
		// An empty wanted password must never open the admin UI, whatever the
		// person sent.
		{name: "both empty", given: "", want: ""},
		{name: "empty against a password", given: "", want: "portapixel"},
		{name: "a password against an empty one", given: "portapixel", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PasswordEqual(tt.given, tt.want); got != tt.equal {
				t.Fatalf("PasswordEqual(%q, %q) = %v, want %v", tt.given, tt.want, got, tt.equal)
			}
		})
	}
}
