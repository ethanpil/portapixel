package syncer

// The settings boundary of D48, derived from one list.
//
// The rule: the fleet server owns exactly what its manifest carries. The manifest
// holds the default playlist, the schedule rules and the three screen power fields,
// so those are the fields that a paired device does not take from its local admin.
//
// Everything else stays local while the device is paired, and that is a decision and
// not an oversight:
//
//   - The other [playback] fields are the local defaults of this screen. A fleet
//     playlist carries its own transition and its own shuffle, so the server already
//     says how its content plays.
//   - updates.auto says whether this device installs the approved release by itself
//     or waits for a person. The server gates WHICH version (D28); the owner of the
//     screen decides when it lands.
//   - The rotation, the audio, the network, video_mode, the device name, the time
//     zone, the web password, SSH and logging are hardware and access. Pushing them
//     fleet-wide is how a person bricks a screen that they cannot see.
//
// managed_test.go compares the list with the manifest struct, so a new manifest
// field cannot be added without a decision here.
var managedFields = []string{
	// The playlist that plays when no rule matches (manifest.default_playlist).
	"playback.default_playlist",
	// The schedule rules (manifest.schedule).
	"schedule",
	// The screen power times (manifest.screen). The method stays local: it is
	// hardware.
	"display.on_time",
	"display.off_time",
	"display.power_days",
}

// ManagedFields gives the fields that the fleet server owns, in order. The admin UI
// reads the list from GET /api/pair, so the page holds no copy of it.
func ManagedFields() []string {
	out := make([]string, len(managedFields))
	copy(out, managedFields)
	return out
}

// ManagedField reports if the fleet server owns a configuration field while the
// device is paired (D48). field is a field name of config.ChangeClass.
func ManagedField(field string) bool {
	for _, f := range managedFields {
		if f == field {
			return true
		}
	}
	return false
}
