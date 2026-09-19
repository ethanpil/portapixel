package api

import (
	"net/http"

	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/server/db"
)

// getManifest answers GET /api/v1/manifest. db.Manifest holds the resolution
// rule; this handler only names the URL bases and writes the answer.
func (d Deps) getManifest(w http.ResponseWriter, r *http.Request) {
	dev, ok := d.device(w, r)
	if !ok {
		return
	}
	m, err := d.DB.Manifest(dev, db.ManifestOptions{
		ServerName:  d.ServerName(),
		DefaultPoll: d.DefaultPoll(),
		MediaBase:   MediaBase,
		ReleaseBase: ReleaseBase,
	})
	if err != nil {
		d.Log.Log("manifest-error", dev.ID+": "+err.Error())
		WriteError(w, http.StatusInternalServerError, "the server could not build the manifest")
		return
	}
	// A poll counts as contact. Without this a device that polls but sends no
	// heartbeat would look offline on the dashboard.
	d.DB.TouchSeen(dev.ID, peerIP(r))
	WriteJSON(w, http.StatusOK, m)
}

// postHeartbeat answers POST /api/v1/heartbeat. db.Heartbeat holds the identity
// rules of D21 and the command acknowledgements.
func (d Deps) postHeartbeat(w http.ResponseWriter, r *http.Request) {
	dev, ok := d.device(w, r)
	if !ok {
		return
	}
	var hb manifest.Heartbeat
	if !ReadJSON(w, r, &hb) {
		return
	}
	if err := d.DB.Heartbeat(dev.ID, hb, peerIP(r)); err != nil {
		d.Log.Log("heartbeat-error", dev.ID+": "+err.Error())
		WriteError(w, http.StatusInternalServerError, "the server could not write this heartbeat")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
