package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/opslog"
)

// FileName is the name of the state file in the state directory.
const FileName = "state.json"

// State is everything that the device must remember across a boot and across a
// write to the media partition (ARCHITECTURE section 3). The media partition can
// be pulled out and edited on a laptop, so nothing here belongs in the TOML.
type State struct {
	// DeviceID and HardwareID are the identity that the device used last.
	DeviceID   string `json:"device_id"`
	HardwareID string `json:"hardware_id"`
	// HardwareSource names the file that gave the last identity.
	HardwareSource string `json:"hardware_source"`
	// HardwareChanged is true after a repair, until the fleet server confirms
	// the new identity (D21). The heartbeat carries it.
	HardwareChanged bool `json:"hardware_changed"`

	// DeviceToken is the per-device fleet token that the server gave us. The
	// TOML holds only the token that the user typed, so a flashed card stays
	// clonable (D25).
	DeviceToken string `json:"device_token,omitempty"`
	// ClaimSecret polls a pending enrollment. PairingCode is the 6-character
	// code that the fallback screen shows.
	ClaimSecret string `json:"claim_secret,omitempty"`
	PairingCode string `json:"pairing_code,omitempty"`
	// ServerURL is the server that paired this device. It can be different from
	// the URL in the TOML while the user edits the TOML.
	ServerURL string `json:"server_url,omitempty"`

	// BadReleases are the releases that failed their health gate. The updater
	// never tries them again.
	BadReleases []string `json:"bad_releases,omitempty"`
}

// StatePath gives the path of the state file.
func StatePath(stateDir string) string { return filepath.Join(stateDir, FileName) }

// LoadState reads the state file. A missing file gives an empty state and no
// error, because the first boot has no file yet. A damaged file gives an empty
// state and an error: the caller writes an ops log line and goes on, because a
// device that cannot start is worse than a device that lost its pairing.
func LoadState(stateDir string) (State, error) {
	data, err := os.ReadFile(StatePath(stateDir))
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("read %s: %w", StatePath(stateDir), err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("bad %s: %w", StatePath(stateDir), err)
	}
	return s, nil
}

// Save writes the state file. The write is staged and committed with a rename,
// so a power cut cannot leave half a file (D41).
func (s State) Save(stateDir string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(StatePath(stateDir), append(data, '\n'), 0o600)
}

// Paired reports if the device has a fleet token.
func (s State) Paired() bool { return s.DeviceToken != "" }

// MarkBadRelease adds a release to the list. It does nothing when the release is
// already in the list.
func (s *State) MarkBadRelease(version string) {
	if version != "" && !slices.Contains(s.BadReleases, version) {
		s.BadReleases = append(s.BadReleases, version)
	}
}

// Resolve compares the stored identity with the hardware and gives the state
// that the daemon must use (D21).
//
// Three cases:
//
//   - No stored identity: the device writes the derived identity. This is a
//     first boot.
//   - The stored identity and the derived identity are the same: nothing
//     happens.
//   - They are different: this is field repair. The device takes the derived
//     identity, keeps the name, the configuration and the fleet token, writes an
//     ops log line, and raises HardwareChanged for the next heartbeat. A true
//     clone is the server's catch, because two hardware identities then present
//     one token.
//
// Resolve writes the file when it changes something. A failed write gives an
// error, and the caller goes on with the state in memory: a read-only state
// directory must not stop playback.
func Resolve(stateDir string, id Identity, log *opslog.Log) (State, error) {
	state, err := LoadState(stateDir)
	if err != nil {
		log.Log("identity.state.bad", err.Error()+"; the device starts with an empty state")
	}

	switch {
	case state.HardwareID == "":
		state.DeviceID = id.DeviceID
		state.HardwareID = id.HardwareID
		state.HardwareSource = id.Source
		log.Log("identity.new", fmt.Sprintf("device=%s source=%s", id.DeviceID, id.Source))
	case state.HardwareID == id.HardwareID:
		if state.HardwareSource != id.Source {
			// The same value from another file. Record it and go on.
			state.HardwareSource = id.Source
		} else {
			return state, nil
		}
	default:
		log.Log("identity.repair", fmt.Sprintf("was=%s now=%s source=%s; the device keeps its name and its pairing",
			state.DeviceID, id.DeviceID, id.Source))
		state.DeviceID = id.DeviceID
		state.HardwareID = id.HardwareID
		state.HardwareSource = id.Source
		state.HardwareChanged = true
	}

	if err := state.Save(stateDir); err != nil {
		return state, fmt.Errorf("write the state file: %w", err)
	}
	return state, nil
}
