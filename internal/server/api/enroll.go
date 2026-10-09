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
//   - Limiter counts the wrong tokens: five a minute from one address. A request
//     that showed a good token clears the count of its address, so a screen that
//     polls with its claim secret never locks itself out. A request with NO token
//     shows nothing, so it never clears the count: a caller could otherwise send
//     one of those between two guesses and guess for ever.
//   - PendingLimiter counts the requests that make a new row in the pending list:
//     five an hour from one address. A caller can repeat such a request with a
//     new device ID each time to fill the table. A request with no token is one,
//     and so is a request with a pending-mode token, which is in clear text on
//     each card. The check happens BEFORE the write: a check after the commit can
//     only hold the answer back, and the row is then already in the table. A
//     screen that waits polls with its claim secret and makes no new row, so it
//     is not in this count.
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

	// Only the rules of db.Enroll know if this request makes a new row. So the
	// request takes a place in the count first, and db.Enroll refuses a new row when
	// there was no place. A request that made no row gives its place back.
	mayQueue := d.PendingLimiter.Allow(ip)
	res, err := d.DB.Enroll(req, ip, mayQueue)
	if mayQueue {
		if err == nil && res.Created {
			d.PendingLimiter.Fail(ip)
		} else {
			d.PendingLimiter.Done(ip)
		}
	}
	switch {
	case errors.Is(err, db.ErrQueueLimited):
		// Not a wrong token, so the wrong-token count adds nothing. The attempt
		// that Allow opened must end here, or it holds a place for a whole minute.
		d.Limiter.Done(ip)
		httpjson.Error(w, http.StatusTooManyRequests,
			"too many new screens from this address; wait a while")
		return
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
		d.Log.Log("enroll-pending", req.DeviceID+" waits with the code "+res.PairingCode)
	}
	if req.Token != "" {
		// The request showed a token that this server gave out: a device token, a
		// claim secret, or an enrollment token. The wrong-token count of the address
		// goes away.
		d.Limiter.Reset(ip)
	} else {
		// No token, so nothing was proved. The attempt that Allow counted ends here
		// and adds nothing: this request is neither a failure nor a proof.
		d.Limiter.Done(ip)
	}
	if res.Status == "paired" {
		d.Log.Log("enroll", req.DeviceID+" paired from "+ip)
	}
	httpjson.Write(w, http.StatusOK, res.EnrollResponse)
}
