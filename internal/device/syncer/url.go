package syncer

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// BadURL is the fault of a server address that a device cannot use. The API answers
// 422 for it, because the request is the thing that is wrong.
type BadURL struct{ Reason string }

func (e BadURL) Error() string { return e.Reason }

// badURL makes a BadURL with a sentence for a person.
func badURL(format string, args ...any) error {
	return BadURL{Reason: fmt.Sprintf(format, args...)}
}

// CheckServerURL gives the base address of a fleet server, or an error that names
// the fault. The base carries no slash at its end, so a path is base + "/api/...".
//
// The rules are strict, because the value comes from a form and from a hand-edited
// file, and the device sends its token to whatever this names:
//
//   - http:// or https:// only. A device does not talk to a file or to an FTP
//     server.
//   - No user name and no password in the address. A token in a URL goes into
//     every log of every proxy on the way.
//   - No fragment. A fragment never reaches a server, so an address with one is an
//     address that somebody pasted wrong.
func CheckServerURL(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", badURL("the server address is empty")
	}
	u, err := url.Parse(text)
	if err != nil {
		return "", badURL("%q is not a web address", text)
	}
	switch u.Scheme {
	case "http", "https":
	case "":
		return "", badURL("the server address must start with http:// or https://")
	default:
		return "", badURL("%q is not an address that a device can use; use http:// or https://", u.Scheme+"://")
	}
	if u.Host == "" {
		return "", badURL("the server address names no server")
	}
	if u.User != nil {
		return "", badURL("the server address must hold no user name and no password")
	}
	if u.Fragment != "" || strings.Contains(text, "#") {
		return "", badURL("the server address must hold no # part")
	}
	if u.RawQuery != "" {
		return "", badURL("the server address must hold no ? part")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String(), nil
}

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
