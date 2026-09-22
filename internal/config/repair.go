package config

import (
	"strconv"
	"strings"
)

// Repair puts the default value in each field that breaks a rule. It gives the
// repaired configuration and the faults that it found.
//
// A person edits portapixel.toml by hand on any computer, so one typed
// character is a thing that happens. That character must cost the value that
// holds it and nothing else: the WiFi key, the admin password and the schedule
// beside it all stay. Load uses Repair; Save does not, because a save from the
// web UI must tell the person what is wrong instead of changing it (D38).
//
// Some values go together. The static address, the gateway and the DNS servers
// are one unit, because a device with half of a static setup is a device that
// nobody can reach; a fault in any of them gives the whole unit back to DHCP.
// The network name and the WiFi key are a unit for the same reason, as are the
// two screen times. A [[schedule]] rule that breaks a rule goes away.
//
// The output of Repair always passes Validate.
func Repair(cfg Config) (Config, Errors) {
	bad := cfg.Validate()
	if len(bad) == 0 {
		return cfg, nil
	}

	def := Default()
	out := cfg.clone()
	dropRule := make(map[int]bool)

	for _, e := range bad {
		switch field := e.Field; {
		case field == "device.name":
			out.Device.Name = def.Device.Name
		case field == "device.tier":
			out.Device.Tier = def.Device.Tier
		case field == "device.timezone":
			out.Device.Timezone = def.Device.Timezone

		case field == "network.mode", field == "network.address",
			field == "network.gateway", strings.HasPrefix(field, "network.dns"):
			out.Network.Mode = def.Network.Mode
			out.Network.Address = def.Network.Address
			out.Network.Gateway = def.Network.Gateway
			out.Network.DNS = def.Network.DNS
		case field == "network.wifi_ssid", field == "network.wifi_psk":
			out.Network.WifiSSID = def.Network.WifiSSID
			out.Network.WifiPSK = def.Network.WifiPSK
		case field == "network.wifi_country":
			// The country stands alone. A bad code must not cost the network name
			// and the key beside it: the radio then joins nothing at all.
			out.Network.WifiCountry = def.Network.WifiCountry

		case field == "display.rotation":
			out.Display.Rotation = def.Display.Rotation
		case field == "display.video_mode":
			out.Display.VideoMode = def.Display.VideoMode
		case field == "display.power_method":
			out.Display.PowerMethod = def.Display.PowerMethod
		case field == "display.on_time", field == "display.off_time":
			out.Display.OnTime = def.Display.OnTime
			out.Display.OffTime = def.Display.OffTime
		case field == "display.power_days":
			out.Display.PowerDays = def.Display.PowerDays

		case field == "audio.output":
			out.Audio.Output = def.Audio.Output
		case field == "audio.volume":
			out.Audio.Volume = def.Audio.Volume

		case field == "playback.default_playlist":
			out.Playback.DefaultPlaylist = def.Playback.DefaultPlaylist
		case field == "playback.transition":
			out.Playback.Transition = def.Playback.Transition
		case field == "playback.transition_ms":
			out.Playback.TransitionMS = def.Playback.TransitionMS
		case field == "playback.image_duration":
			out.Playback.ImageDuration = def.Playback.ImageDuration
		case field == "playback.nightly_restart":
			out.Playback.NightlyRestart = def.Playback.NightlyRestart

		// Each watchdog value stands alone. A bad timeout must not cost the reboot
		// step beside it: half of the ladder is still a ladder.
		case field == "watchdog.heartbeat_timeout":
			out.Watchdog.HeartbeatTimeout = def.Watchdog.HeartbeatTimeout
		case field == "watchdog.restarts_before_reboot":
			out.Watchdog.RestartsBeforeReboot = def.Watchdog.RestartsBeforeReboot
		case field == "watchdog.restart_window":
			out.Watchdog.RestartWindow = def.Watchdog.RestartWindow

		case strings.HasPrefix(field, "schedule["):
			if i, ok := ruleIndex(field); ok {
				dropRule[i] = true
			}

		case field == "server.poll_seconds":
			out.Server.PollSeconds = def.Server.PollSeconds

		case field == "web.port":
			out.Web.Port = def.Web.Port
		case field == "web.password":
			out.Web.Password = def.Web.Password
		}
	}

	if len(dropRule) > 0 {
		kept := out.Schedule[:0]
		for i, r := range out.Schedule {
			if !dropRule[i] {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			out.Schedule = nil
		} else {
			out.Schedule = kept
		}
	}

	return out, bad
}

// ruleIndex takes the number out of a field name such as "schedule[2].start".
func ruleIndex(field string) (int, bool) {
	open := strings.IndexByte(field, '[')
	end := strings.IndexByte(field, ']')
	if open < 0 || end < open {
		return 0, false
	}
	i, err := strconv.Atoi(field[open+1 : end])
	if err != nil || i < 0 {
		return 0, false
	}
	return i, true
}
