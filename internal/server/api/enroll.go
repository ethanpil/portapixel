package api

import (
	"errors"
	"net/http"

	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
)

// postEnroll answers POST /api/v1/enroll. The pairing rules are in db.Enroll; this
// handler reads the body, counts the attempt, and writes the answer (D25).
//
// There are two limiters, because there are two kinds of abuse on one open route.
//
//   - Limiter counts the wrong tokens: five a minute from one address. A device
//     that polls with a good claim secret clears its own count, so a screen that
//     waits for approval never locks itself out.
//   - PendingLimiter counts the requests that make a row in the pending list.
//     Without it an unauthenticated caller could loop over device IDs and fill the
//     table, because every one of those requests succeeds and would clear the
//     first limiter. A poll of a request that already waits does not count.
func (d Deps) postEnroll(w http.ResponseWriter, r *http.Request) {
	ip := d.clientIP(r)
	if !d.Limiter.Allow(ip) {
		httpjson.Error(w, http.StatusTooManyRequests,
			"too many enroll attempts from this address; wait a minute")
		return
	}

	var req manifest.EnrollRequest
	if !httpjson.Read(w, r, &req) {
		d.Limiter.Fail(ip)
		return
	}

	res, err := d.DB.Enroll(req, ip)
	switch {
	case errors.Is(err, db.ErrBadToken):
		d.Limiter.Fail(ip)
		httpjson.Revoked(w, "this token is not valid")
		return
	case errors.Is(err, db.ErrBadDeviceID):
		d.Limiter.Fail(ip)
		httpjson.Fields(w, "the request has a field that this server cannot use",
			db.Errors{{Field: "device_id", Message: err.Error()}})
		return
	case errors.Is(err, db.ErrTooManyPending):
		d.Limiter.Fail(ip)
		httpjson.Error(w, http.StatusTooManyRequests,
			"too many screens wait for approval; the admin must answer the list first")
		return
	case err != nil:
		d.Limiter.Fail(ip)
		d.Log.Log("enroll-error", req.DeviceID+": "+err.Error())
		httpjson.Error(w, http.StatusInternalServerError, "the server could not write this enrollment")
		return
	}

	if res.Created {
		// A new row in the pending list. This one counts, and the count does not
		// go away because the request worked.
		if !d.PendingLimiter.Allow(ip) {
			httpjson.Error(w, http.StatusTooManyRequests,
				"too many new screens from this address; wait a while")
			return
		}
		d.PendingLimiter.Fail(ip)
		d.Log.Log("enroll-pending", req.DeviceID+" waits with the code "+res.PairingCode)
		httpjson.Write(w, http.StatusOK, res.EnrollResponse)
		return
	}

	// A good token or a poll of a request that waits. The wrong-token count of
	// this address goes away.
	d.Limiter.Reset(ip)
	if res.Status == "paired" {
		d.Log.Log("enroll", req.DeviceID+" paired from "+ip)
	}
	httpjson.Write(w, http.StatusOK, res.EnrollResponse)
}
