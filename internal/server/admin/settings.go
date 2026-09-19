package admin

import (
	"fmt"
	"net/http"
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
	url := strings.TrimSpace(body.PublicURL)
	if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		errs = append(errs, db.FieldError{Field: "public_url", Message: "must start with http:// or https://"})
	}
	if len(errs) > 0 {
		httpjson.Fields(w, "the request has a field that this server cannot use", errs)
		return
	}

	if err := d.SaveSettings(Settings{ServerName: name, PollSeconds: body.PollSeconds, PublicURL: url}); err != nil {
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
