package api

import (
	"errors"
	"net/http"

	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/server/db"
)

// postEnroll answers POST /api/v1/enroll. The pairing rule is in db.Enroll; this
// handler reads the body, counts the attempt, and writes the answer (D25).
//
// The limiter counts the failed attempts of each address. A device that waits for
// approval polls this route every few seconds with a good claim secret, and a
// good attempt clears the count of that address, so a waiting device never locks
// itself out. An attacker that guesses tokens gets five tries a minute.
func (d Deps) postEnroll(w http.ResponseWriter, r *http.Request) {
	ip := peerIP(r)
	if !d.Limiter.Allow(ip) {
		WriteError(w, http.StatusTooManyRequests, "too many enroll attempts from this address; wait a minute")
		return
	}

	var req manifest.EnrollRequest
	if !ReadJSON(w, r, &req) {
		d.Limiter.Fail(ip)
		return
	}

	res, err := d.DB.Enroll(req, ip)
	switch {
	case errors.Is(err, db.ErrBadToken):
		d.Limiter.Fail(ip)
		WriteError(w, http.StatusUnauthorized, "this token is not valid")
		return
	case errors.Is(err, db.ErrBadDeviceID):
		d.Limiter.Fail(ip)
		WriteFields(w, "the request has a field that this server cannot use",
			db.Errors{{Field: "device_id", Message: err.Error()}})
		return
	case err != nil:
		d.Limiter.Fail(ip)
		d.Log.Log("enroll-error", req.DeviceID+": "+err.Error())
		WriteError(w, http.StatusInternalServerError, "the server could not write this enrollment")
		return
	}

	d.Limiter.Reset(ip)
	if res.Status == "paired" {
		d.Log.Log("enroll", req.DeviceID+" paired from "+ip)
	} else {
		d.Log.Log("enroll-pending", req.DeviceID+" waits with the code "+res.PairingCode)
	}
	WriteJSON(w, http.StatusOK, res)
}
