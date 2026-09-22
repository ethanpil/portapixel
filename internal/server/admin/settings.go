package admin

import (
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"

	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
)

// getSettings gives the settings of the server.
func (d Deps) getSettings(w http.ResponseWriter, r *http.Request) {
	httpjson.Write(w, http.StatusOK, d.Settings())
}

// putSettings writes the settings.
//
// Every value here takes effect at once. The Host allowlist comes from a function
// that runs on each request, so a new public URL is live on the next one. Only the
// listen address and the two TLS paths need a restart, and the admin UI cannot
// change those.
func (d Deps) putSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServerName  string `json:"server_name"`
		PollSeconds int    `json:"default_poll_seconds"`
		PublicURL   string `json:"public_url"`
	}
	if !httpjson.Read(w, r, &body) {
		return
	}

	var errs db.Errors
	name := strings.TrimSpace(body.ServerName)
	if name == "" {
		errs = append(errs, db.FieldError{Field: "server_name", Message: "the server needs a name"})
	}
	if body.PollSeconds < db.MinPollSeconds || body.PollSeconds > db.MaxPollSeconds {
		errs = append(errs, db.FieldError{Field: "default_poll_seconds",
			Message: fmt.Sprintf("must be between %d and %d seconds",
				db.MinPollSeconds, db.MaxPollSeconds)})
	}
	public, urlErr := checkPublicURL(body.PublicURL)
	if urlErr != "" {
		errs = append(errs, db.FieldError{Field: "public_url", Message: urlErr})
	}
	if len(errs) > 0 {
		httpjson.Fields(w, "the request has a field that this server cannot use", errs)
		return
	}

	if err := d.SaveSettings(Settings{ServerName: name, PollSeconds: body.PollSeconds, PublicURL: public}); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("settings", "the server settings changed")
	httpjson.Write(w, http.StatusOK, map[string]any{
		"ok": true,
		// Nothing here needs a restart. The field stays in the answer, because
		// web/shared reads it.
		"restart_needed": false,
		"settings":       d.Settings(),
	})
}

// checkPublicURL gives the public URL in its stored form, or the sentence that says
// why a device could not use it. An empty value is permitted: then the admin UI
// answers on the loopback names only.
//
// The rules are the rules of the device, internal/device/syncer.CheckServerURL. This
// is the one field where the server hands an address to a device: the enrollment
// token page pastes it into the [server] block of portapixel.toml. A value that only
// had to start with http:// or https:// could carry a query, a fragment or a user
// name, and every card that got that block would then refuse its own configuration
// at first boot, on a screen that nobody stands in front of.
//
// The two packages may not import each other (ARCHITECTURE section 2), so the rules
// are written twice today. They belong in one shared package beside
// fleet.ResolveURL; a test of this package holds the two in step until then.
func checkPublicURL(raw string) (string, string) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", ""
	}
	u, err := neturl.Parse(text)
	if err != nil {
		return "", "that is not a web address"
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", "must start with http:// or https://"
	case u.Host == "":
		return "", "it names no server"
	case u.User != nil:
		return "", "it must hold no user name and no password"
	case u.Fragment != "" || strings.Contains(text, "#"):
		return "", "it must hold no # part"
	case u.RawQuery != "":
		return "", "it must hold no ? part"
	}
	// The slash at the end goes now, once, and not on every device.
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String(), ""
}
