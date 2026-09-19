package httpjson

import (
	"net"
	"net/http"
	"strings"

	"github.com/ethanpil/portapixel/internal/httpguard"
)

// Proxies is the set of peer addresses whose forwarding headers the server
// believes. Each entry is an IP address or a CIDR block.
//
// Why this exists: deploy/README recommends a reverse proxy. Behind one, every
// request arrives from the address of the proxy. Then one limiter bucket holds the
// whole fleet, so a single card that loops on a revoked token stops enrollment
// for every screen, five wrong logins from anywhere lock the admin out, and
// last_ip names the proxy for each device.
//
// The header is only believed when the peer is a proxy that the admin named. A
// caller that reaches the server directly can put anything in
// X-Forwarded-For, so the header of an untrusted peer is ignored completely.
type Proxies struct {
	nets []*net.IPNet
	ips  []net.IP
}

// NewProxies reads the trusted_proxies list. An entry that is not an address and
// not a CIDR block gives a warning, and the rest of the list still works.
func NewProxies(entries []string) (*Proxies, []string) {
	p := &Proxies{}
	var warnings []string
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if _, block, err := net.ParseCIDR(entry); err == nil {
			p.nets = append(p.nets, block)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			p.ips = append(p.ips, ip)
			continue
		}
		warnings = append(warnings, "trusted_proxies holds "+entry+
			", which is not an address and not a CIDR block, so the server ignores it")
	}
	return p, warnings
}

// Trusts reports if addr is one of the trusted proxies.
func (p *Proxies) Trusts(addr string) bool {
	if p == nil {
		return false
	}
	ip := net.ParseIP(httpguard.HostOf(addr))
	if ip == nil {
		return false
	}
	for _, one := range p.ips {
		if one.Equal(ip) {
			return true
		}
	}
	for _, block := range p.nets {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP gives the address of the caller with no port.
//
// When the peer is a trusted proxy, the answer is the right-most entry of
// X-Forwarded-For that is not itself a trusted proxy: that is the address that
// the outermost proxy of the chain saw, and a client cannot put an entry there
// that a later proxy does not overwrite. When the peer is not a trusted proxy,
// the header is ignored and the answer is the peer.
func (p *Proxies) ClientIP(r *http.Request) string {
	peer := httpguard.HostOf(r.RemoteAddr)
	if !p.Trusts(r.RemoteAddr) {
		return peer
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		entry := strings.TrimSpace(parts[i])
		if entry == "" {
			continue
		}
		entry = httpguard.HostOf(entry)
		if net.ParseIP(entry) == nil {
			// A name or a damaged entry. Nothing below it can be believed either.
			return peer
		}
		if !p.Trusts(entry) {
			return entry
		}
	}
	return peer
}

// ClientIsHTTPS reports if the caller reached the server over TLS. It reads
// X-Forwarded-Proto from a trusted proxy only, so a caller cannot make the server
// send a Secure cookie over plain HTTP.
func (p *Proxies) ClientIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !p.Trusts(r.RemoteAddr) {
		return false
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	// A chain of proxies can make a list. The first entry is the outermost one.
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
