package api

import (
	"errors"
	"net/http"

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
	d.DB.TouchSeen(dev.ID, d.clientIP(r))
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
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}
