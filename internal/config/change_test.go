package config

import "testing"

// classOf gives the class that ChangeClass put on a field.
func classOf(changes []Change, field string) (Class, bool) {
	for _, c := range changes {
		if c.Field == field {
			return c.Class, true
		}
	}
	return "", false
}

func TestChangeClassNoChange(t *testing.T) {
	if got := ChangeClass(Default(), Default()); len(got) != 0 {
		t.Fatalf("two equal configurations gave %v", got)
	}
	if got := ChangeClass(full(), full()); len(got) != 0 {
		t.Fatalf("two equal full configurations gave %v", got)
	}
}

func TestChangeClass(t *testing.T) {
	tests := []struct {
		name      string
		change    func(c *Config)
		wantField string
		wantClass Class
	}{
		{name: "name", change: func(c *Config) { c.Device.Name = "Lobby" }, wantField: "device.name", wantClass: Live},
		{name: "timezone", change: func(c *Config) { c.Device.Timezone = "Europe/Berlin" }, wantField: "device.timezone", wantClass: Live},
		{name: "tier", change: func(c *Config) { c.Device.Tier = "low" }, wantField: "device.tier", wantClass: Reboot},

		{name: "network mode", change: func(c *Config) { c.Network.Mode = "static" }, wantField: "network.mode", wantClass: Reboot},
		{name: "address", change: func(c *Config) { c.Network.Address = "10.0.0.5/24" }, wantField: "network.address", wantClass: Reboot},
		{name: "gateway", change: func(c *Config) { c.Network.Gateway = "10.0.0.1" }, wantField: "network.gateway", wantClass: Reboot},
		{name: "dns", change: func(c *Config) { c.Network.DNS = []string{"1.1.1.1"} }, wantField: "network.dns", wantClass: Reboot},
		{name: "wifi name", change: func(c *Config) { c.Network.WifiSSID = "Office" }, wantField: "network.wifi_ssid", wantClass: Reboot},
		{name: "wifi key", change: func(c *Config) { c.Network.WifiPSK = "secret" }, wantField: "network.wifi_psk", wantClass: Reboot},

		{name: "rotation", change: func(c *Config) { c.Display.Rotation = 90 }, wantField: "display.rotation", wantClass: Browser},
		{name: "video mode", change: func(c *Config) { c.Display.VideoMode = "1920x1080" }, wantField: "display.video_mode", wantClass: Browser},
		{name: "power method", change: func(c *Config) { c.Display.PowerMethod = "cec" }, wantField: "display.power_method", wantClass: Live},
		{name: "on time", change: func(c *Config) { c.Display.OnTime = "07:30" }, wantField: "display.on_time", wantClass: Live},
		{name: "off time", change: func(c *Config) { c.Display.OffTime = "22:00" }, wantField: "display.off_time", wantClass: Live},
		{name: "power days", change: func(c *Config) { c.Display.PowerDays = []string{"mon"} }, wantField: "display.power_days", wantClass: Live},

		{name: "audio output", change: func(c *Config) { c.Audio.Output = "analog" }, wantField: "audio.output", wantClass: Browser},
		{name: "volume", change: func(c *Config) { c.Audio.Volume = 50 }, wantField: "audio.volume", wantClass: Live},

		{name: "default playlist", change: func(c *Config) { c.Playback.DefaultPlaylist = "lobby" }, wantField: "playback.default_playlist", wantClass: Live},
		{name: "transition", change: func(c *Config) { c.Playback.Transition = "cut" }, wantField: "playback.transition", wantClass: Live},
		{name: "transition time", change: func(c *Config) { c.Playback.TransitionMS = 100 }, wantField: "playback.transition_ms", wantClass: Live},
		{name: "image duration", change: func(c *Config) { c.Playback.ImageDuration = 30 }, wantField: "playback.image_duration", wantClass: Live},
		{name: "shuffle", change: func(c *Config) { c.Playback.Shuffle = true }, wantField: "playback.shuffle", wantClass: Live},
		{name: "nightly restart", change: func(c *Config) { c.Playback.NightlyRestart = "" }, wantField: "playback.nightly_restart", wantClass: Live},
		{
			name:      "schedule",
			change:    func(c *Config) { c.Schedule = []Rule{{Playlist: "a", Start: "08:00", End: "18:00"}} },
			wantField: "schedule", wantClass: Live,
		},

		// The supervisor reads the watchdog values at each check, so every one of
		// them is live (D30).
		{name: "watchdog off", change: func(c *Config) { c.Watchdog.Enabled = false }, wantField: "watchdog.enabled", wantClass: Live},
		{
			name:      "heartbeat timeout",
			change:    func(c *Config) { c.Watchdog.HeartbeatTimeout = 60 },
			wantField: "watchdog.heartbeat_timeout", wantClass: Live,
		},
		{
			name:      "restarts before a reboot",
			change:    func(c *Config) { c.Watchdog.RestartsBeforeReboot = 0 },
			wantField: "watchdog.restarts_before_reboot", wantClass: Live,
		},
		{
			name:      "restart window",
			change:    func(c *Config) { c.Watchdog.RestartWindow = 30 },
			wantField: "watchdog.restart_window", wantClass: Live,
		},

		{name: "server url", change: func(c *Config) { c.Server.URL = "https://a" }, wantField: "server.url", wantClass: Live},
		{name: "server token", change: func(c *Config) { c.Server.Token = "t" }, wantField: "server.token", wantClass: Live},
		{name: "poll seconds", change: func(c *Config) { c.Server.PollSeconds = 30 }, wantField: "server.poll_seconds", wantClass: Live},

		{name: "web port", change: func(c *Config) { c.Web.Port = 8080 }, wantField: "web.port", wantClass: Reboot},
		{name: "web password", change: func(c *Config) { c.Web.Password = "new" }, wantField: "web.password", wantClass: Live},

		{name: "ssh", change: func(c *Config) { c.SSH.Enabled = false }, wantField: "ssh.enabled", wantClass: Reboot},
		{name: "updates", change: func(c *Config) { c.Updates.Auto = true }, wantField: "updates.auto", wantClass: Live},
		{name: "logging", change: func(c *Config) { c.Logging.Persist = true }, wantField: "logging.persist", wantClass: Reboot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := Default()
			next := Default()
			tt.change(&next)

			changes := ChangeClass(old, next)
			if len(changes) != 1 {
				t.Fatalf("got %v, want one change", changes)
			}
			class, ok := classOf(changes, tt.wantField)
			if !ok {
				t.Fatalf("got %v, want a change of %q", changes, tt.wantField)
			}
			if class != tt.wantClass {
				t.Fatalf("%s is %q, want %q", tt.wantField, class, tt.wantClass)
			}
		})
	}
}

func TestChangeClassIgnoresTheDeviceID(t *testing.T) {
	old := Default()
	next := Default()
	next.Device.ID = "px-12345678"
	if got := ChangeClass(old, next); len(got) != 0 {
		t.Fatalf("got %v, want no change: the hardware gives the device ID", got)
	}
}

func TestChangeClassManyChanges(t *testing.T) {
	old := Default()
	next := Default()
	next.Device.Name = "Lobby"    // live
	next.Display.Rotation = 90    // browser
	next.Network.Mode = "static"  // reboot
	next.Network.Address = "a/24" // reboot
	next.Audio.Volume = 10        // live

	changes := ChangeClass(old, next)
	if len(changes) != 5 {
		t.Fatalf("got %d changes, want 5: %v", len(changes), changes)
	}
	count := map[Class]int{}
	for _, c := range changes {
		count[c.Class]++
	}
	if count[Live] != 2 || count[Browser] != 1 || count[Reboot] != 2 {
		t.Fatalf("the classes are %v", count)
	}
}

func TestSameRules(t *testing.T) {
	a := []Rule{{Playlist: "a", Days: []string{"mon"}, Start: "08:00", End: "18:00"}}
	tests := []struct {
		name string
		b    []Rule
		same bool
	}{
		{name: "the same list", b: []Rule{{Playlist: "a", Days: []string{"mon"}, Start: "08:00", End: "18:00"}}, same: true},
		{name: "another playlist", b: []Rule{{Playlist: "b", Days: []string{"mon"}, Start: "08:00", End: "18:00"}}},
		{name: "another day", b: []Rule{{Playlist: "a", Days: []string{"tue"}, Start: "08:00", End: "18:00"}}},
		{name: "one more day", b: []Rule{{Playlist: "a", Days: []string{"mon", "tue"}, Start: "08:00", End: "18:00"}}},
		{name: "another time", b: []Rule{{Playlist: "a", Days: []string{"mon"}, Start: "09:00", End: "18:00"}}},
		{name: "an empty list", b: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameRules(a, tt.b); got != tt.same {
				t.Fatalf("sameRules = %v, want %v", got, tt.same)
			}
		})
	}
}
