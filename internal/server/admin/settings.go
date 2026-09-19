package admin

import (
	"net/http"
	"strings"

	"github.com/ethanpil/portapixel/internal/server/db"
)

// getSettings gives the settings of the server.
func (d Deps) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Settings())
}

// putSettings writes the settings.
//
// The server name and the poll interval go into the settings table and take
// effect at once. The public URL goes into server.toml, because the Host
// allowlist and the pairing block are built from it. The answer says which
// values need a restart, so the UI can say it too.
func (d Deps) putSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServerName  string `json:"server_name"`
		PollSeconds int    `json:"default_poll_seconds"`
		PublicURL   string `json:"public_url"`
	}
	if !readJSON(w, r, &body) {
		return
	}

	var errs db.Errors
	name := strings.TrimSpace(body.ServerName)
	if name == "" {
		errs = append(errs, db.FieldError{Field: "server_name", Message: "the server needs a name"})
	}
	if body.PollSeconds < 5 || body.PollSeconds > 86400 {
		errs = append(errs, db.FieldError{Field: "default_poll_seconds",
			Message: "must be between 5 and 86400 seconds"})
	}
	url := strings.TrimSpace(body.PublicURL)
	if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		errs = append(errs, db.FieldError{Field: "public_url", Message: "must start with http:// or https://"})
	}
	if len(errs) > 0 {
		writeFields(w, "the request has a field that this server cannot use", errs)
		return
	}

	old := d.Settings()
	if err := d.SaveSettings(Settings{ServerName: name, PollSeconds: body.PollSeconds, PublicURL: url}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	d.Log.Log("settings", "the server settings changed")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		// A change of the public URL changes the Host allowlist, which is built
		// when the server starts.
		"restart_needed": url != old.PublicURL,
		"settings":       d.Settings(),
	})
}
