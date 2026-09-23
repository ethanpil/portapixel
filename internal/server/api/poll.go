package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"

	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
)

// getManifest answers GET /api/v1/manifest. db.Manifest holds the resolution
// rule; this handler only names the URL bases and writes the answer.
func (d Deps) getManifest(w http.ResponseWriter, r *http.Request) {
	dev, ok := d.device(w, r)
	if !ok {
		return
	}
	fleet := d.Fleet()
	m, err := d.DB.Manifest(dev, db.ManifestOptions{
		ServerName:  fleet.ServerName,
		DefaultPoll: fleet.DefaultPoll,
		MediaBase:   MediaBase,
		ReleaseBase: ReleaseBase,
		Release:     fleet.Release,
	})
	if err != nil {
		d.Log.Log("manifest-error", dev.ID+": "+err.Error())
		httpjson.Error(w, http.StatusInternalServerError, "the server could not build the manifest")
		return
	}
	// A poll counts as contact. Without this a device that polls but sends no
	// heartbeat would look offline on the dashboard.
	//
	// It is a best-effort write and the manifest goes out whatever it answers: a
	// locked database must not stop a screen from getting its content. The failure
	// still reaches the log, because a full disk shows itself here first.
	if err := d.DB.TouchSeen(dev.ID, d.clientIP(r)); err != nil {
		slog.Warn("write the last contact time", "device", dev.ID, "error", err)
	}
	httpjson.Write(w, http.StatusOK, m)
}

// postHeartbeat answers POST /api/v1/heartbeat. db.Heartbeat holds the identity
// rules of D21 and the command acknowledgements.
func (d Deps) postHeartbeat(w http.ResponseWriter, r *http.Request) {
	dev, ok := d.device(w, r)
	if !ok {
		return
	}
	var hb manifest.Heartbeat
	if !httpjson.Read(w, r, &hb) {
		return
	}
	err := d.DB.Heartbeat(dev.ID, hb, d.clientIP(r))
	var fieldErrs db.Errors
	switch {
	case errors.As(err, &fieldErrs):
		httpjson.Fields(w, "the request has a field that this server cannot use", fieldErrs)
		return
	case err != nil:
		d.Log.Log("heartbeat-error", dev.ID+": "+err.Error())
		httpjson.Error(w, http.StatusInternalServerError, "the server could not write this heartbeat")
		return
	}
	d.logName(dev, hb.Name)
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// refusedNames holds, for each device ID, the last reported name that broke the
// rule of manifest.CleanName. A device reports its name at every poll, so a bad
// name gets one log line and not one line a minute until the next restart.
var refusedNames sync.Map

// logName writes the name that a heartbeat reported to the ops log when it changed
// the row, or when db.Heartbeat refused it. dev is the row before the heartbeat.
func (d Deps) logName(dev db.Device, reported string) {
	name, ok := manifest.CleanName(reported)
	if !ok {
		if last, seen := refusedNames.Swap(dev.ID, reported); !seen || last != reported {
			// The value is not in the line: it can hold a control character.
			d.Log.Log("device-name-refused", fmt.Sprintf(
				"%s reports a name that breaks the rule (1 to %d characters, no control character); the name stays %s",
				dev.ID, manifest.MaxNameLength, strconv.Quote(dev.Name)))
		}
		return
	}
	refusedNames.Delete(dev.ID)
	if name != dev.Name {
		d.Log.Log("device-rename", dev.ID+" reports the name "+strconv.Quote(name))
	}
}
