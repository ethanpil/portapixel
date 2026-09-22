package syncer

import (
	"net"
	"net/url"
	"strings"

	"github.com/ethanpil/portapixel/internal/fleet"
)

// BadURL and CheckServerURL live in internal/fleet. The rule of a fleet server
// address is the same rule at both ends, and the fleet server cannot import a
// device package (ARCHITECTURE section 2). These two names stay so that
// internal/device/httpd keeps one import; the alias makes errors.As work across
// both spellings.
type BadURL = fleet.BadURL

// CheckServerURL gives the base address of a fleet server, or an error that names
// the fault. See fleet.CheckServerURL for the rules.
func CheckServerURL(raw string) (string, error) { return fleet.CheckServerURL(raw) }

// InsecureURL reports if the address sends the device token in clear text over a
// network that is not local.
//
// http:// to a machine on the same network is the normal small installation: a
// homelab server on 192.168.1.10 has no certificate and needs none. http:// to a
// name on the internet gives the device token to everything between the two, so
// the device says so in a warning of /api/status.
func InsecureURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !localIP(ip)
	}
	lower := strings.ToLower(host)
	// A name with no full stop in it cannot be a public DNS name: "http://fleet/"
	// and "http://signage/" are a machine on the same network, found by the search
	// domain or by the hosts file. The warning was on every one of them, and a
	// warning that is wrong is a warning that a person learns to pass over.
	if !strings.Contains(lower, ".") {
		return false
	}
	return !strings.HasSuffix(lower, ".localhost") &&
		!strings.HasSuffix(lower, ".local") && !strings.HasSuffix(lower, ".internal") &&
		!strings.HasSuffix(lower, ".home") && !strings.HasSuffix(lower, ".lan")
}

// localIP reports if an address belongs to the machine itself or to a private
// network.
func localIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}
