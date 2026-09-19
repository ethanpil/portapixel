package syncer

import (
	"context"
	"errors"
	"time"

	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/manifest"
)

// PairState is the answer of GET /api/pair and of POST /api/pair.
type PairState struct {
	// Status is "unpaired", "pending" or "paired".
	Status     string `json:"status"`
	ServerURL  string `json:"server_url"`
	ServerName string `json:"server_name,omitempty"`
	// PairingCode is the 6-character code that the server admin approves. It goes
	// to the logged-in admin and to the fallback screen on the device (D46).
	PairingCode string    `json:"pairing_code,omitempty"`
	LastSync    time.Time `json:"last_sync,omitempty"`
	SyncError   string    `json:"sync_error,omitempty"`
	// Insecure is true when the address is http:// on a network that is not local.
	Insecure bool `json:"insecure,omitempty"`
}

// The three words of PairState.Status.
const (
	StatusUnpaired = "unpaired"
	StatusPending  = "pending"
	StatusPaired   = "paired"
)

// State gives the pairing state for GET /api/pair.
func (s *Syncer) State() PairState {
	st := s.opt.State()
	cfg := s.opt.Config()

	s.mu.Lock()
	out := PairState{
		ServerName: s.serverName,
		LastSync:   s.lastSync,
		SyncError:  s.syncError,
	}
	s.mu.Unlock()

	out.ServerURL = st.ServerURL
	if out.ServerURL == "" {
		out.ServerURL = cfg.Server.URL
	}
	out.Insecure = InsecureURL(out.ServerURL)
	switch {
	case st.Paired():
		out.Status = StatusPaired
	case st.ClaimSecret != "":
		out.Status = StatusPending
		out.PairingCode = st.PairingCode
	default:
		out.Status = StatusUnpaired
	}
	return out
}

// Pair starts a pairing. It is POST /api/pair and it is also the first pass of the
// poll loop on a device that a person configured in the TOML.
//
// save is true for the call of the admin UI: the address and the token that the
// person typed go into portapixel.toml through the normal save path, which is an
// explicit save. The poll loop passes false, because the values came out of that
// file already.
//
// All three flows of D25 land here:
//
//   - An enrollment token or a device token gives the status "paired" and a
//     per-device token. The token goes into state.json and never into the TOML, so
//     a flashed card stays clonable.
//   - No token gives the status "pending" with a 6-character code and a claim
//     secret. The loop then polls with the claim secret until the admin approves.
//   - A device that already waits keeps its claim secret over a reboot, because the
//     secret is in state.json.
func (s *Syncer) Pair(ctx context.Context, rawURL, token string, save bool) (PairState, error) {
	base, err := CheckServerURL(rawURL)
	if err != nil {
		return PairState{}, err
	}
	if save {
		if s.opt.SaveServer == nil {
			return PairState{}, errors.New("this device cannot write its configuration")
		}
		if err := s.opt.SaveServer(base, token); err != nil {
			return PairState{}, err
		}
		// A person who typed an address and a token wants an attempt now, whatever
		// the last answer of the server was.
		s.mu.Lock()
		s.revoked = false
		s.nextEnroll = time.Time{}
		s.mu.Unlock()
	}

	// A device that waits for approval on this server polls with its claim secret.
	// A second click on Connect must not make a second pending request.
	st := s.opt.State()
	if token == "" && st.ClaimSecret != "" && st.ServerURL == base {
		token = st.ClaimSecret
	}

	res, err := s.enroll(ctx, base, token)
	if err != nil {
		return PairState{}, err
	}
	if err := s.takeEnrollment(base, res); err != nil {
		return PairState{}, err
	}
	return s.State(), nil
}

// takeEnrollment writes what the server answered into the state file.
func (s *Syncer) takeEnrollment(base string, res manifest.EnrollResponse) error {
	now := s.opt.Now()
	paired := res.Status == StatusPaired
	// A screen can wait for approval for hours, and it asks again every 10 seconds.
	// One line for each poll would fill the whole ops log, so the line goes out when
	// the code is a new one.
	newCode := !paired && s.opt.State().PairingCode != res.PairingCode

	err := s.opt.SaveState(func(st *identity.State) {
		st.ServerURL = base
		if paired {
			st.DeviceToken = res.DeviceToken
			st.ClaimSecret = ""
			st.PairingCode = ""
			st.PendingSince = time.Time{}
			// The last applied manifest goes away with the pairing. A device that
			// pairs again must apply the whole manifest, even when the server sends
			// the same one: the fleet playlists and the schedule have to come back.
			// The objects stay on the card, so nothing is downloaded twice.
			st.Fleet = nil
			return
		}
		st.ClaimSecret = res.ClaimSecret
		st.PairingCode = res.PairingCode
		if st.PendingSince.IsZero() {
			st.PendingSince = now
		}
	})
	if err != nil {
		return err
	}
	switch {
	case paired:
		s.log("sync.paired", "the server "+base+" paired this device")
	case newCode:
		s.log("sync.pending", "this device waits for approval on "+base+" with the code "+res.PairingCode)
	}
	return nil
}

// claimRound asks the server again about a request that waits for approval.
func (s *Syncer) claimRound(ctx context.Context, st identity.State) time.Duration {
	res, err := s.enroll(ctx, st.ServerURL, st.ClaimSecret)
	if err != nil {
		if errors.Is(err, ErrRevoked) {
			// The admin refused the request, or it expired. Forget the claim and
			// start again with the address that the person gave.
			s.log("sync.pending.gone", "the server no longer holds the request of this device; the code is not valid")
			s.opt.SaveState(func(st *identity.State) {
				st.ClaimSecret = ""
				st.PairingCode = ""
				st.PendingSince = time.Time{}
			})
			return claimPoll
		}
		return s.failed("sync.pending.fail", err)
	}
	if err := s.takeEnrollment(st.ServerURL, res); err != nil {
		return s.failed("sync.state.write.fail", err)
	}
	if res.Status == StatusPaired {
		return 0 // poll at once
	}
	// A screen can wait for approval for days. The first ten minutes are the time
	// in which a person stands in front of it.
	if since := st.PendingSince; !since.IsZero() && s.opt.Now().Sub(since) > claimPatience {
		return claimSlow
	}
	return claimPoll
}

// Unpair forgets the fleet server (D25).
//
// The objects stay in _fleet/media. A device that pairs again must not download a
// video of 1 GB one more time, and the card has the space either way.
//
// There is no route on the server that a device can call to unpair itself, so
// nothing is sent. The admin removes the row, or revokes the token, and the device
// already stopped using it.
func (s *Syncer) Unpair() error {
	st := s.opt.State()
	if !st.Paired() && st.ClaimSecret == "" && s.opt.Config().Server.URL == "" {
		return nil
	}

	err := s.opt.SaveState(func(st *identity.State) {
		st.DeviceToken = ""
		st.ClaimSecret = ""
		st.PairingCode = ""
		st.PendingSince = time.Time{}
		st.ServerURL = ""
		st.Fleet = nil
	})
	if err != nil {
		return err
	}

	// The address and the token go out of portapixel.toml too. Without that the
	// poll loop would pair this device again inside a minute, and the Unpair button
	// would do nothing that lasts.
	if s.opt.SaveServer != nil {
		if err := s.opt.SaveServer("", ""); err != nil {
			s.log("sync.unpair.config.fail", err.Error())
		}
	}

	s.mu.Lock()
	s.serverName = ""
	s.pollSeconds = 0
	s.release = nil
	s.applied = false
	s.syncError = ""
	s.lastResult = "never"
	s.lastSync = time.Time{}
	s.revoked = false
	s.nextEnroll = time.Time{}
	s.mu.Unlock()

	if s.opt.ClearFleetRules != nil {
		s.opt.ClearFleetRules()
	}
	// The library stops reading _fleet as soon as the token is gone, so the rescan
	// is what takes the fleet playlists off the screen.
	if s.opt.Rescan != nil {
		s.opt.Rescan()
	}
	s.log("sync.unpair", "this device is standalone again; the cached objects stay on the card")
	return nil
}

// dropToken answers a server that refused the device token.
//
// The token was revoked, or the admin removed the row. The device becomes
// unpaired and configured: the fleet playlists stop, the rules of the TOML come
// back, and one enroll attempt with the token of the TOML follows every five
// minutes. A loop that tried every second would be a denial of service against the
// server of the person who revoked the token.
func (s *Syncer) dropToken() time.Duration {
	err := s.opt.SaveState(func(st *identity.State) {
		st.DeviceToken = ""
		st.ClaimSecret = ""
		st.PairingCode = ""
		// The next pairing must apply the whole manifest again.
		st.Fleet = nil
	})
	if err != nil {
		s.log("sync.state.write.fail", err.Error())
	}

	if s.opt.ClearFleetRules != nil {
		s.opt.ClearFleetRules()
	}
	// The library stops reading _fleet as soon as the token is gone.
	if s.opt.Rescan != nil {
		s.opt.Rescan()
	}
	return s.refuseToken()
}

// refuseToken holds the next enroll attempt back for reEnrollGap and writes one
// ops log line. Two calls in a row write one line: a device with a token that
// nobody will accept again must not fill the log.
func (s *Syncer) refuseToken() time.Duration {
	token := s.opt.Config().Server.Token

	s.mu.Lock()
	first := !s.revoked
	s.revoked = true
	s.revokedToken = token
	s.nextEnroll = s.opt.Now().Add(reEnrollGap)
	s.applied = false
	s.syncError = ErrRevoked.Error()
	s.lastResult = "error"
	s.mu.Unlock()

	if first {
		s.log("sync.token.revoked", "the server refused the token of this device; the device is unpaired and keeps playing")
	}
	return reEnrollGap
}
