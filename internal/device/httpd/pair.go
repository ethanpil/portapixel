package httpd

import (
	"errors"
	"io"
	"net/http"

	"github.com/ethanpil/portapixel/internal/device/syncer"
)

// PairState is the answer of the pairing routes. It is the type of the fleet
// client, so the two ends of this route cannot drift.
type PairState = syncer.PairState

// ErrManaged is the refusal of a local edit that the fleet server owns (D48). The
// daemon gives it from SaveConfig when a body changes a managed field.
type ErrManaged struct {
	// Server is the name of the fleet server, for the sentence that a person reads.
	Server string
	// Field is the configuration field that the body changed, or "".
	Field string
}

func (e ErrManaged) Error() string { return managedMessage(e.Server) }

// managedMessage is the one sentence of every fleet refusal. The admin UI shows it
// as it is, so it says who is in charge and nothing else.
func managedMessage(server string) string {
	if server == "" {
		server = "the fleet server"
	}
	return "managed by " + server
}

// fleetGuard refuses a call that the fleet server owns while the device is paired
// (D48, plan section 13).
//
// It is one function and every locked route goes through it, so the boundary cannot
// drift route by route. PUT /api/config is not in the list: the local admin keeps
// the rotation, the audio, the network and the passwords on that route, so the
// refusal there is per field and lives in the configuration save (syncer.ManagedField).
func (d Deps) fleetGuard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Managed != nil {
			if name, paired := d.Managed(); paired {
				refuseManaged(w, r, name, "")
				return
			}
		}
		h(w, r)
	}
}

// drainOnRefusal is how much of an unread body the device reads before it answers a
// refusal. Go drains a small body by itself, and a body that is longer goes
// unread: the connection is then closed and the client sees a reset in place of the
// answer. A media upload is the case that matters, and the person must read the
// sentence that says why.
const drainOnRefusal = 1 << 20

// refuseManaged answers 403 for a route that the fleet server owns.
//
// The refusal comes before the body is read, so an upload of 500 MB is never
// written to the card. The answer says which server is in charge and which
// configuration fields belong to it, so the admin UI needs no copy of that list.
func refuseManaged(w http.ResponseWriter, r *http.Request, server, field string) {
	if r.Body != nil && r.ContentLength != 0 {
		// Enough of the body to let the client read the answer, and no more.
		io.CopyN(io.Discard, r.Body, drainOnRefusal)
		w.Header().Set("Connection", "close")
	}
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error":  managedMessage(server),
		"fields": managedFieldErrors(field),
	})
}

// managedFieldErrors names each configuration field that the fleet server owns, in
// the shape of a 422 answer. The admin UI disables exactly these.
//
// cause is the field of the body that made this refusal, or "". A field that is not
// one of the fleet fields is one of the pairing fields, which change through
// /api/pair only.
func managedFieldErrors(cause string) []fieldError {
	fields := syncer.ManagedFields()
	out := make([]fieldError, 0, len(fields)+1)
	for _, f := range fields {
		out = append(out, fieldError{Field: f, Message: "the fleet server manages this"})
	}
	if cause != "" && !syncer.ManagedField(cause) {
		out = append(out, fieldError{
			Field:   cause,
			Message: "this device is paired; unpair it to change this",
		})
	}
	return out
}

// fieldError is one field of a 422 or of a managed 403.
type fieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// GET /api/pair gives the pairing state to the admin UI.
//
// The pairing code is in the answer. This route needs a session, so the code goes
// to the person who is logged in; /api/status gives it to the device itself only
// (D46).
func (d Deps) getPair(w http.ResponseWriter, r *http.Request) {
	if d.PairState == nil {
		writeError(w, http.StatusNotImplemented, NotImplemented)
		return
	}
	writeJSON(w, http.StatusOK, d.PairState())
}

// POST /api/pair {"url": "...", "token": "..."}
//
// An empty token starts the code pairing: the device registers as pending and the
// answer carries the 6-character code (D25). The address and the token go into
// portapixel.toml, because a person asked for it on the settings page, and that is
// an explicit save.
//
// The call is synchronous. A person who presses Connect waits for the answer of the
// server, and the answer says which of the three flows happened.
func (d Deps) postPair(w http.ResponseWriter, r *http.Request) {
	if d.Pair == nil {
		writeError(w, http.StatusNotImplemented, NotImplemented)
		return
	}
	var body struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	state, err := d.Pair(r.Context(), body.URL, body.Token)
	if err != nil {
		// A bad address is a fault of the request. Everything else happened between
		// this device and the server, and 502 says which of the two to look at.
		code := http.StatusBadGateway
		var bad syncer.BadURL
		var already syncer.ErrAlreadyPaired
		switch {
		case errors.As(err, &bad):
			code = http.StatusUnprocessableEntity
		case errors.As(err, &already):
			// A device with a pairing must be unpaired first. The admin UI has the
			// button, and the token of the old server must never go to a new one.
			code = http.StatusConflict
		}
		writeError(w, code, err.Error())
		return
	}
	d.Log.Log("web.pair", "the admin paired this device with "+state.ServerURL+": "+state.Status)
	writeJSON(w, http.StatusOK, state)
}

// DELETE /api/pair unpairs the device.
//
// The cached objects stay in _fleet/media. A device that pairs again must not
// download a video of 1 GB one more time.
func (d Deps) deletePair(w http.ResponseWriter, r *http.Request) {
	if d.Unpair == nil {
		writeError(w, http.StatusNotImplemented, NotImplemented)
		return
	}
	if err := d.Unpair(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ok)
}
