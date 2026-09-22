package fleet

import (
	"fmt"
	"net/url"
	"strings"
)

// BadURL is the fault of a fleet server address that cannot be used. An API answers
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
// file, and a device sends its token to whatever this names:
//
//   - http:// or https:// only. A device does not talk to a file or to an FTP
//     server.
//   - No user name and no password in the address. A token in a URL goes into
//     every log of every proxy on the way.
//   - No fragment. A fragment never reaches a server, so an address with one is an
//     address that somebody pasted wrong.
//
// This rule lives here and not in internal/device/syncer, because the fleet server
// checks the same value when an admin types its public address, and the server
// cannot import a device package (ARCHITECTURE section 2). Two copies of one rule
// drift.
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
