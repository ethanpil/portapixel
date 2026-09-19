package syncer

// The settings boundary of D48, in one table.
//
// The fleet server owns the content, the playlists, the schedules, the screen power
// times and the updates. The hardware-adjacent settings stay with the local admin
// even while the device is paired: rotation, audio, the network, video_mode, the
// device name, the time zone, the web password, SSH and logging. Pushing display
// settings fleet-wide is how a person bricks a screen that they cannot see.
//
// The field names are the names of config.ChangeClass, so the comparison that the
// settings page already makes is the comparison that this table answers.
var managedFields = map[string]bool{
	// The playlist that plays and how it plays.
	"playback.default_playlist": true,
	"playback.transition":       true,
	"playback.transition_ms":    true,
	"playback.image_duration":   true,
	"playback.shuffle":          true,
	"playback.nightly_restart":  true,
	// The schedule rules.
	"schedule": true,
	// The screen power times. The method stays local: it is hardware.
	"display.on_time":    true,
	"display.off_time":   true,
	"display.power_days": true,
	// The updates.
	"updates.auto": true,
}

// ManagedField reports if the fleet server owns a configuration field while the
// device is paired (D48). field is a field name of config.ChangeClass.
func ManagedField(field string) bool { return managedFields[field] }
