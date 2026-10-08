package config

import "slices"

// Class says how a changed key takes effect.
//
// The rule behind the three classes: a key is Live when the daemon reads it
// again while it runs. A key is Player when the player gets the value when it
// starts, so only a new player process can use the new value. A key is Reboot
// when a boot service or the kernel uses the value, so only a boot can apply it.
// Guessing wrong in the safe direction (a bigger class) costs a few seconds of
// black screen; guessing wrong in the other direction makes a setting that looks
// applied but is not.
type Class string

const (
	Live   Class = "live"
	Player Class = "player"
	Reboot Class = "reboot"
)

// Change is one key that is different, with the way to apply it.
type Change struct {
	Field string `json:"field"`
	Class Class  `json:"class"`
}

// ChangeClass compares two configurations and says how to apply each change.
// The settings page uses the answer: it applies the live changes at once and
// tells the user which changes need a player restart or a reboot.
//
// device.id is not in the list. The daemon takes it from the hardware at each
// boot, so an edit does nothing (D21).
func ChangeClass(old, next Config) []Change {
	var out []Change
	add := func(field string, class Class, changed bool) {
		if changed {
			out = append(out, Change{Field: field, Class: class})
		}
	}

	// [device]. mDNS announces a new name at once. The scheduler reads the time
	// zone at each evaluation.
	add("device.name", Live, old.Device.Name != next.Device.Name)
	add("device.timezone", Live, old.Device.Timezone != next.Device.Timezone)

	// [network]. A boot service renders /etc/network/interfaces and
	// wpa_supplicant.conf before the network starts. A change to the network of
	// a running device must not break the connection that the user works over.
	add("network.mode", Reboot, old.Network.Mode != next.Network.Mode)
	add("network.address", Reboot, old.Network.Address != next.Network.Address)
	add("network.gateway", Reboot, old.Network.Gateway != next.Network.Gateway)
	add("network.dns", Reboot, !slices.Equal(old.Network.DNS, next.Network.DNS))
	add("network.wifi_ssid", Reboot, old.Network.WifiSSID != next.Network.WifiSSID)
	add("network.wifi_psk", Reboot, old.Network.WifiPSK != next.Network.WifiPSK)
	add("network.wifi_country", Reboot, old.Network.WifiCountry != next.Network.WifiCountry)

	// [display]. mpv gets the rotation, the output mode and the video output on
	// its command line, so a new player process applies them. The power method
	// and the screen schedule are the daemon's work.
	add("display.rotation", Player, old.Display.Rotation != next.Display.Rotation)
	add("display.video_mode", Player, old.Display.VideoMode != next.Display.VideoMode)
	add("display.video_output", Player, old.Display.VideoOutput != next.Display.VideoOutput)
	add("display.power_method", Live, old.Display.PowerMethod != next.Display.PowerMethod)
	add("display.on_time", Live, old.Display.OnTime != next.Display.OnTime)
	add("display.off_time", Live, old.Display.OffTime != next.Display.OffTime)
	add("display.power_days", Live, !slices.Equal(old.Display.PowerDays, next.Display.PowerDays))

	// [audio]. The player reads the default device of ALSA when it starts, so only
	// a new player process uses a new output (internal/device/audio). The volume
	// goes to the ALSA mixer at once.
	add("audio.output", Player, old.Audio.Output != next.Audio.Output)
	add("audio.volume", Live, old.Audio.Volume != next.Audio.Volume)

	// [playback] and [[schedule]]. The player asks for the manifest again.
	add("playback.default_playlist", Live, old.Playback.DefaultPlaylist != next.Playback.DefaultPlaylist)
	add("playback.transition", Live, old.Playback.Transition != next.Playback.Transition)
	add("playback.transition_ms", Live, old.Playback.TransitionMS != next.Playback.TransitionMS)
	add("playback.image_duration", Live, old.Playback.ImageDuration != next.Playback.ImageDuration)
	add("playback.shuffle", Live, old.Playback.Shuffle != next.Playback.Shuffle)
	add("playback.nightly_restart", Live, old.Playback.NightlyRestart != next.Playback.NightlyRestart)
	add("schedule", Live, !sameRules(old.Schedule, next.Schedule))

	// [watchdog]. The browser supervisor reads these four values at each check
	// through a function, so a new threshold is live (D30).
	add("watchdog.enabled", Live, old.Watchdog.Enabled != next.Watchdog.Enabled)
	add("watchdog.heartbeat_timeout", Live, old.Watchdog.HeartbeatTimeout != next.Watchdog.HeartbeatTimeout)
	add("watchdog.restarts_before_reboot", Live, old.Watchdog.RestartsBeforeReboot != next.Watchdog.RestartsBeforeReboot)
	add("watchdog.restart_window", Live, old.Watchdog.RestartWindow != next.Watchdog.RestartWindow)

	// [server]. The sync client reads these values before each poll.
	add("server.url", Live, old.Server.URL != next.Server.URL)
	add("server.token", Live, old.Server.Token != next.Server.Token)
	add("server.poll_seconds", Live, old.Server.PollSeconds != next.Server.PollSeconds)

	// [web]. The daemon holds the port open, so only a restart of the daemon can
	// bind another port. A reboot is the simple and safe way to do that.
	add("web.port", Reboot, old.Web.Port != next.Web.Port)
	add("web.password", Live, old.Web.Password != next.Web.Password)

	// [ssh] and [logging]. OpenRC starts sshd at boot, and the log destination is
	// a mount that the boot makes.
	add("ssh.enabled", Reboot, old.SSH.Enabled != next.SSH.Enabled)
	add("logging.persist", Reboot, old.Logging.Persist != next.Logging.Persist)

	// [updates]. The updater reads the flag before each check.
	add("updates.auto", Live, old.Updates.Auto != next.Updates.Auto)

	return out
}

// sameRules compares two schedule lists.
func sameRules(a, b []Rule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Playlist != b[i].Playlist || a[i].Start != b[i].Start || a[i].End != b[i].End {
			return false
		}
		if !slices.Equal(a[i].Days, b[i].Days) {
			return false
		}
	}
	return true
}
