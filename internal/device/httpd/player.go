package httpd

import (
	"net/http"

	"github.com/ethanpil/portapixel/internal/device/browser"
)

// GET /api/player/manifest gives the active playlist (ARCHITECTURE 7a). The
// daemon applies every default and does the shuffle, so the SPA plays the list in
// the order that it receives.
func (d Deps) getPlayerManifest(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.PlayerManifest())
}

// POST /api/player/heartbeat, every 5 seconds.
//
// The frame counter in the body is what makes a frozen picture visible: the
// timers of a dead page still fire, but the frame clock stops (D45).
func (d Deps) postHeartbeat(w http.ResponseWriter, r *http.Request) {
	var body browser.Heartbeat
	if !readJSON(w, r, &body) {
		return
	}
	d.Heartbeat(body)
	writeJSON(w, http.StatusOK, ok)
}

// POST /api/player/ready is the answer of the player to a grace request: it has
// reached an item boundary and the browser may restart now (plan 3.3).
func (d Deps) postPlayerReady(w http.ResponseWriter, r *http.Request) {
	d.PlayerReady()
	writeJSON(w, http.StatusOK, ok)
}

// GET /api/player/qr.svg gives the QR code of the admin address for the fallback
// screen (D18).
func (d Deps) getQR(w http.ResponseWriter, r *http.Request) {
	svg, err := QRCodeSVG(d.AdminURL())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(svg)
}
