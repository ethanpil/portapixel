package httpd

import (
	"errors"
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
				writeError(w, http.StatusForbidden, managedMessage(name))
				return
			}
		}
		h(w, r)
	}
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
		if errors.As(err, &bad) {
			code = http.StatusUnprocessableEntity
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
