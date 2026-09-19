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
	var hb browser.Heartbeat
	if !readJSON(w, r, &hb) {
		return
	}
	d.Heartbeat(hb)
	writeJSON(w, http.StatusOK, ok)
}

// POST /api/player/url-item {"index": 2}
//
// The player stops and the daemon takes the browser. An answer of {"skip": true}
// means that the page does not answer, and the player goes to the next item
// (D19).
func (d Deps) postURLItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Index int `json:"index"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	skip, err := d.URLItem(body.Index)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if skip {
		writeJSON(w, http.StatusOK, map[string]bool{"skip": true})
		return
	}
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
