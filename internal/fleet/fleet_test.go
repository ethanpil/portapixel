package fleet

import (
	"net/http"
	"testing"
)

// One rule turns an address of the manifest into a whole address. The path of the
// server base must stay: a server behind a reverse proxy under /portapixel answers
// on that prefix, and a join that dropped it made every object download answer 404
// while the poll worked.
func TestResolveURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		ref  string
		want string
	}{
		{
			name: "a path of the server", base: "https://s.example.com",
			ref: "/api/v1/media/abc", want: "https://s.example.com/api/v1/media/abc",
		},
		{
			name: "a base with a trailing slash", base: "https://s.example.com/",
			ref: "/api/v1/media/abc", want: "https://s.example.com/api/v1/media/abc",
		},
		{
			name: "a base with a path prefix", base: "https://s.example.com/portapixel",
			ref: "/api/v1/media/abc", want: "https://s.example.com/portapixel/api/v1/media/abc",
		},
		{
			name: "a base with a prefix and a trailing slash", base: "https://s.example.com/portapixel/",
			ref: "/api/v1/media/abc", want: "https://s.example.com/portapixel/api/v1/media/abc",
		},
		{
			name: "a path with no leading slash", base: "https://s.example.com/portapixel",
			ref: "api/v1/media/abc", want: "https://s.example.com/portapixel/api/v1/media/abc",
		},
		{
			name: "the whole address of the server", base: "https://s.example.com",
			ref: "https://s.example.com/api/v1/media/abc", want: "https://s.example.com/api/v1/media/abc",
		},
		{
			name: "the whole address with the name in capital letters", base: "https://s.example.com",
			ref: "https://S.EXAMPLE.COM/api/v1/media/abc", want: "https://S.EXAMPLE.COM/api/v1/media/abc",
		},
		{
			name: "the default port written out", base: "https://s.example.com",
			ref: "https://s.example.com:443/media/abc", want: "https://s.example.com:443/media/abc",
		},
		{
			name: "a port that the base names", base: "http://192.168.1.10:8099",
			ref: "/media/abc", want: "http://192.168.1.10:8099/media/abc",
		},
		{name: "another host", base: "https://s.example.com", ref: "https://attacker.example.net/x"},
		{name: "a user name in the address", base: "https://s.example.com",
			ref: "https://s.example.com@attacker.example.net/x"},
		{name: "another scheme", base: "https://s.example.com", ref: "http://s.example.com/x"},
		{name: "another port", base: "https://s.example.com", ref: "https://s.example.com:8443/x"},
		{name: "a path that climbs out of the prefix", base: "https://s.example.com/portapixel",
			ref: "/../etc/passwd"},
		{name: "an empty address", base: "https://s.example.com", ref: "  "},
		{name: "a base that is not an address", base: "not a url", ref: "/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveURL(tt.base, tt.ref)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("ResolveURL(%q, %q) = %q, want an error", tt.base, tt.ref, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ResolveURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The device token goes to the host that the request started on and to no other.
func TestDropBearerOffHost(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		keep bool
	}{
		{name: "the same host", from: "https://s.example.com/a", to: "https://s.example.com/b", keep: true},
		{name: "the same host with the default port", from: "https://s.example.com/a",
			to: "https://s.example.com:443/b", keep: true},
		{name: "a subdomain", from: "https://s.example.com/a", to: "https://files.s.example.com/b"},
		{name: "another host", from: "https://s.example.com/a", to: "https://cdn.example.net/b"},
		{name: "another port", from: "https://s.example.com/a", to: "https://s.example.com:8443/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, err := http.NewRequest(http.MethodGet, tt.from, nil)
			if err != nil {
				t.Fatal(err)
			}
			next, err := http.NewRequest(http.MethodGet, tt.to, nil)
			if err != nil {
				t.Fatal(err)
			}
			next.Header.Set("Authorization", "Bearer t")
			if err := DropBearerOffHost(next, []*http.Request{first}); err != nil {
				t.Fatal(err)
			}
			kept := next.Header.Get("Authorization") != ""
			if kept != tt.keep {
				t.Errorf("the token was kept = %v, want %v", kept, tt.keep)
			}
		})
	}
}

// A chain of redirects with no end must stop.
func TestDropBearerOffHostStopsALongChain(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://s.example.com/a", nil)
	if err != nil {
		t.Fatal(err)
	}
	via := make([]*http.Request, MaxRedirects)
	for i := range via {
		via[i] = req
	}
	if err := DropBearerOffHost(req, via); err == nil {
		t.Error("DropBearerOffHost() took a chain of MaxRedirects redirects")
	}
}

// The client of the fleet calls has no overall timeout. http.Client.Timeout covers
// the body read, so a client with one aborts every large download.
func TestNewClientHasNoOverallTimeout(t *testing.T) {
	if got := NewClient().Timeout; got != 0 {
		t.Errorf("Timeout = %v, want no overall timeout", got)
	}
}
