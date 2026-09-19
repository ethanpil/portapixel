package config

import (
	"strings"
	"testing"
)

// validateCase is one fault in a good configuration.
type validateCase struct {
	name string
	// change makes the fault.
	change func(c *Config)
	// wantField is the field that Validate must name. An empty value means that
	// the configuration is good.
	wantField string
}

// validateCases is the list of faults that Validate must find. TestValidate and
// TestRepairAlwaysGivesAGoodConfig both walk it, so every rule that Validate can
// report has a repair as well.
var validateCases = []validateCase{
	{name: "the default is good", change: func(c *Config) {}},

	{name: "empty name", change: func(c *Config) { c.Device.Name = " " }, wantField: "device.name"},
	{name: "bad tier", change: func(c *Config) { c.Device.Tier = "medium" }, wantField: "device.tier"},
	{name: "empty timezone", change: func(c *Config) { c.Device.Timezone = "" }, wantField: "device.timezone"},
	{name: "bad timezone", change: func(c *Config) { c.Device.Timezone = "Mars/Olympus" }, wantField: "device.timezone"},
	{name: "a real timezone is good", change: func(c *Config) { c.Device.Timezone = "America/New_York" }},
	{name: "another real timezone is good", change: func(c *Config) { c.Device.Timezone = "Europe/Berlin" }},

	{name: "bad network mode", change: func(c *Config) { c.Network.Mode = "pppoe" }, wantField: "network.mode"},
	{
		name:      "static mode needs an address",
		change:    func(c *Config) { c.Network.Mode = "static" },
		wantField: "network.address",
	},
	{
		name: "static mode with an address is good",
		change: func(c *Config) {
			c.Network.Mode = "static"
			c.Network.Address = "192.168.1.50/24"
		},
	},
	{
		name:      "a wifi key with no network name",
		change:    func(c *Config) { c.Network.WifiPSK = "secret" },
		wantField: "network.wifi_ssid",
	},
	// The daemon renders the five network values into files that belong to
	// root. A value with a line break in it would add a directive of its own,
	// so each value must have the shape of its field.
	{
		name: "an address that carries a second directive",
		change: func(c *Config) {
			c.Network.Mode = "static"
			c.Network.Address = "192.168.1.50/24\n\tup /bin/sh -c id"
		},
		wantField: "network.address",
	},
	{
		name: "an address that is not an address",
		change: func(c *Config) {
			c.Network.Mode = "static"
			c.Network.Address = "the lobby switch"
		},
		wantField: "network.address",
	},
	{
		name: "a bare address is good",
		change: func(c *Config) {
			c.Network.Mode = "static"
			c.Network.Address = "192.168.1.50"
		},
	},
	{
		name: "an IPv6 address with a prefix is good",
		change: func(c *Config) {
			c.Network.Mode = "static"
			c.Network.Address = "2001:db8::5/64"
		},
	},
	{
		name: "a gateway that carries a second directive",
		change: func(c *Config) {
			c.Network.Mode = "static"
			c.Network.Address = "192.168.1.50/24"
			c.Network.Gateway = "192.168.1.1\n\tup /bin/sh -c id"
		},
		wantField: "network.gateway",
	},
	{
		name: "a gateway with a prefix length",
		change: func(c *Config) {
			c.Network.Mode = "static"
			c.Network.Address = "192.168.1.50/24"
			c.Network.Gateway = "192.168.1.1/24"
		},
		wantField: "network.gateway",
	},
	{
		name: "a good gateway and good dns servers",
		change: func(c *Config) {
			c.Network.Mode = "static"
			c.Network.Address = "192.168.1.50/24"
			c.Network.Gateway = "192.168.1.1"
			c.Network.DNS = []string{"192.168.1.1", "1.1.1.1", "2606:4700:4700::1111"}
		},
	},
	{
		name: "a dns server that is a name",
		change: func(c *Config) {
			c.Network.DNS = []string{"1.1.1.1", "dns.example.com"}
		},
		wantField: "network.dns[1]",
	},
	{
		name: "a network name with a line break",
		change: func(c *Config) {
			c.Network.WifiSSID = "Lobby\n\tkey_mgmt=NONE"
		},
		wantField: "network.wifi_ssid",
	},
	{
		name: "a wifi key with a line break",
		change: func(c *Config) {
			c.Network.WifiSSID = "Lobby"
			c.Network.WifiPSK = "secret\n\tkey_mgmt=NONE"
		},
		wantField: "network.wifi_psk",
	},
	{
		name: "a wifi key with a quotation mark is good",
		change: func(c *Config) {
			c.Network.WifiSSID = "Lobby \"guest\""
			c.Network.WifiPSK = `it"s a secret`
		},
	},

	{name: "bad rotation", change: func(c *Config) { c.Display.Rotation = 45 }, wantField: "display.rotation"},
	{name: "negative rotation", change: func(c *Config) { c.Display.Rotation = -90 }, wantField: "display.rotation"},
	{name: "rotation 270 is good", change: func(c *Config) { c.Display.Rotation = 270 }},
	{name: "bad video mode", change: func(c *Config) { c.Display.VideoMode = "1080p" }, wantField: "display.video_mode"},
	{name: "video mode with no refresh is good", change: func(c *Config) { c.Display.VideoMode = "1920x1080" }},
	{name: "video mode with a refresh is good", change: func(c *Config) { c.Display.VideoMode = "3840x2160@30" }},
	{
		name:      "video mode with letters",
		change:    func(c *Config) { c.Display.VideoMode = "1920x1080@60Hz" },
		wantField: "display.video_mode",
	},
	{name: "bad power method", change: func(c *Config) { c.Display.PowerMethod = "hdmi" }, wantField: "display.power_method"},
	{
		name: "a screen schedule is good",
		change: func(c *Config) {
			c.Display.OnTime = "07:30"
			c.Display.OffTime = "22:00"
			c.Display.PowerDays = []string{"mon", "sun"}
		},
	},
	{
		name: "a bad on time",
		change: func(c *Config) {
			c.Display.OnTime = "7:30"
			c.Display.OffTime = "22:00"
		},
		wantField: "display.on_time",
	},
	{
		name: "an hour that is too big",
		change: func(c *Config) {
			c.Display.OnTime = "24:00"
			c.Display.OffTime = "22:00"
		},
		wantField: "display.on_time",
	},
	{
		name: "a minute that is too big",
		change: func(c *Config) {
			c.Display.OnTime = "07:60"
			c.Display.OffTime = "22:00"
		},
		wantField: "display.on_time",
	},
	{
		name:      "one time only",
		change:    func(c *Config) { c.Display.OffTime = "22:00" },
		wantField: "display.on_time",
	},
	{
		name: "a bad day name",
		change: func(c *Config) {
			c.Display.PowerDays = []string{"mon", "funday"}
		},
		wantField: "display.power_days",
	},

	{name: "bad audio output", change: func(c *Config) { c.Audio.Output = "spdif" }, wantField: "audio.output"},
	{name: "volume too high", change: func(c *Config) { c.Audio.Volume = 101 }, wantField: "audio.volume"},
	{name: "volume below zero", change: func(c *Config) { c.Audio.Volume = -1 }, wantField: "audio.volume"},
	{name: "volume 0 is good", change: func(c *Config) { c.Audio.Volume = 0 }},
	{name: "volume 100 is good", change: func(c *Config) { c.Audio.Volume = 100 }},

	{
		name:      "empty default playlist",
		change:    func(c *Config) { c.Playback.DefaultPlaylist = "" },
		wantField: "playback.default_playlist",
	},
	{
		name:      "bad transition",
		change:    func(c *Config) { c.Playback.Transition = "wipe" },
		wantField: "playback.transition",
	},
	{
		name:      "negative transition time",
		change:    func(c *Config) { c.Playback.TransitionMS = -1 },
		wantField: "playback.transition_ms",
	},
	{
		name:      "image duration of zero",
		change:    func(c *Config) { c.Playback.ImageDuration = 0 },
		wantField: "playback.image_duration",
	},
	{
		name:      "bad nightly restart time",
		change:    func(c *Config) { c.Playback.NightlyRestart = "3:30am" },
		wantField: "playback.nightly_restart",
	},
	{
		name:   "an empty nightly restart stops the restart",
		change: func(c *Config) { c.Playback.NightlyRestart = "" },
	},

	{
		name: "a good schedule rule",
		change: func(c *Config) {
			c.Schedule = []Rule{{Playlist: "a", Days: []string{"mon"}, Start: "08:00", End: "18:00"}}
		},
	},
	{
		name: "a rule with no days is good",
		change: func(c *Config) {
			c.Schedule = []Rule{{Playlist: "a", Start: "08:00", End: "18:00"}}
		},
	},
	{
		name: "a rule with no playlist",
		change: func(c *Config) {
			c.Schedule = []Rule{{Start: "08:00", End: "18:00"}}
		},
		wantField: "schedule[0].playlist",
	},
	{
		name: "a rule with no end time",
		change: func(c *Config) {
			c.Schedule = []Rule{{Playlist: "a", Start: "08:00"}}
		},
		wantField: "schedule[0].end",
	},
	{
		name: "a rule with a bad end time",
		change: func(c *Config) {
			c.Schedule = []Rule{{Playlist: "a", Start: "08:00", End: "6pm"}}
		},
		wantField: "schedule[0].end",
	},
	{
		// Both times empty is the whole day. A rule of "weekends: this playlist"
		// needs no hours.
		name: "a rule with no times at all",
		change: func(c *Config) {
			c.Schedule = []Rule{{Playlist: "a", Days: []string{"sat", "sun"}}}
		},
	},
	{
		name: "a rule with no start time",
		change: func(c *Config) {
			c.Schedule = []Rule{{Playlist: "a", End: "18:00"}}
		},
		wantField: "schedule[0].start",
	},
	{
		name: "a rule with a bad day",
		change: func(c *Config) {
			c.Schedule = []Rule{{Playlist: "a", Days: []string{"montag"}, Start: "08:00", End: "18:00"}}
		},
		wantField: "schedule[0].days",
	},
	{
		name: "the second rule is bad",
		change: func(c *Config) {
			c.Schedule = []Rule{
				{Playlist: "a", Start: "08:00", End: "18:00"},
				{Playlist: "b", Start: "18:00", End: "25:00"},
			}
		},
		wantField: "schedule[1].end",
	},

	{name: "poll too fast", change: func(c *Config) { c.Server.PollSeconds = 9 }, wantField: "server.poll_seconds"},
	{name: "poll of ten seconds is good", change: func(c *Config) { c.Server.PollSeconds = 10 }},

	{name: "port zero", change: func(c *Config) { c.Web.Port = 0 }, wantField: "web.port"},
	{name: "port too high", change: func(c *Config) { c.Web.Port = 65536 }, wantField: "web.port"},
	{name: "port 65535 is good", change: func(c *Config) { c.Web.Port = 65535 }},
	{name: "empty password", change: func(c *Config) { c.Web.Password = "" }, wantField: "web.password"},
}

func TestValidate(t *testing.T) {
	for _, tt := range validateCases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.change(&cfg)
			errs := cfg.Validate()
			if tt.wantField == "" {
				if len(errs) > 0 {
					t.Fatalf("want no error, got %v", errs)
				}
				return
			}
			if !hasField(errs, tt.wantField) {
				t.Fatalf("want an error for %q, got %v", tt.wantField, errs)
			}
		})
	}
}

func TestValidateReportsEveryFault(t *testing.T) {
	cfg := Default()
	cfg.Device.Tier = "medium"
	cfg.Audio.Volume = 200
	cfg.Web.Port = 0
	errs := cfg.Validate()
	if len(errs) != 3 {
		t.Fatalf("got %d errors, want 3: %v", len(errs), errs)
	}
	text := errs.Error()
	for _, want := range []string{"device.tier", "audio.volume", "web.port"} {
		if !strings.Contains(text, want) {
			t.Errorf("the message does not name %q: %s", want, text)
		}
	}
}

func TestIsClockTime(t *testing.T) {
	good := []string{"00:00", "07:30", "23:59", "12:00"}
	bad := []string{"", "7:30", "07:3", "07:300", "0730", "24:00", "07:60", "aa:bb", "07-30", " 07:30"}
	for _, v := range good {
		if !isClockTime(v) {
			t.Errorf("isClockTime(%q) = false, want true", v)
		}
	}
	for _, v := range bad {
		if isClockTime(v) {
			t.Errorf("isClockTime(%q) = true, want false", v)
		}
	}
}
