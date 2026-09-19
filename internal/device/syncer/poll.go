package syncer

import (
	"context"
	"errors"
	"time"

	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/manifest"
)

// pollRound is one pass of a paired device: get the manifest, apply it, run the
// commands, send the heartbeat.
//
// The order matters. The manifest is applied first, so a rescan command works on
// the files that the same poll brought. The commands run whatever the apply did: a
// reboot must work on a device whose media partition is full. The heartbeat is
// last, because it carries the result of both.
func (s *Syncer) pollRound(ctx context.Context, tomlBase string, st identity.State) time.Duration {
	base := st.ServerURL
	if base == "" {
		base = tomlBase
	}
	if base == "" {
		s.setError("this device is paired and knows no server address")
		return standaloneWait
	}

	m, err := s.fetchManifest(ctx, base, st.DeviceToken)
	if errors.Is(err, ErrRevoked) {
		return s.dropToken()
	}
	if err != nil {
		return s.failed("sync.poll.fail", err)
	}

	s.mu.Lock()
	s.serverName = m.ServerName
	s.pollSeconds = m.PollSeconds
	s.release = m.Release
	s.mu.Unlock()

	applyErr := s.applyManifest(ctx, base, st.DeviceToken, m)
	if applyErr != nil {
		s.log("sync.apply.fail", applyErr.Error())
		s.setError(applyErr.Error())
	} else {
		s.setOK()
	}

	// The commands run after the apply and before the heartbeat, because the
	// heartbeat carries their acknowledgements.
	acks, rebooting := s.runCommands(ctx, base, st.DeviceToken, m.Commands)
	if rebooting {
		// runCommands already sent the acknowledgement and asked for the reboot.
		return s.interval()
	}

	if err := s.heartbeat(ctx, base, st.DeviceToken, acks); err != nil {
		return s.failed("sync.heartbeat.fail", err)
	}
	if applyErr != nil {
		// The manifest did not fit or an object failed. The device keeps playing
		// what it has and tries again at the next poll, not in one second.
		return s.interval()
	}
	return s.interval()
}

// heartbeat sends the report and the command acknowledgements.
//
// The status is the same struct that /api/status serves, without the fields that
// go to the device itself only. hardware_id is not in the status: it travels in the
// heartbeat body, which is the one place besides the enroll request where it leaves
// the device.
func (s *Syncer) heartbeat(ctx context.Context, base, token string, acks []int64) error {
	status := manifest.Status{}
	if s.opt.Status != nil {
		status = s.opt.Status()
	}
	// A defence in depth: the pairing code belongs to the fallback screen and to
	// the local admin, never to the network (D46).
	status.PairingCode = ""

	s.mu.Lock()
	syncError := s.syncError
	s.mu.Unlock()

	if len(acks) > MaxAcks {
		// The server refuses a longer list, and a refused heartbeat loses every
		// acknowledgement in it. The rest go out at the next poll, because the
		// server sends an unacknowledged command again.
		acks = acks[:MaxAcks]
	}

	return s.sendHeartbeat(ctx, base, token, manifest.Heartbeat{
		DeviceID:   s.opt.Identity.DeviceID,
		HardwareID: s.opt.Identity.HardwareID,
		Name:       s.opt.Config().Device.Name,
		Version:    s.opt.Version,
		Status:     status,
		Acks:       acks,
		SyncError:  syncError,
	})
}
