package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	if errs := Default().Validate(); len(errs) > 0 {
		t.Fatalf("the default configuration is not valid: %v", errs)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		toml  string
		check func(t *testing.T, cfg Config)
	}{
		{
			name: "an empty file gives the defaults",
			toml: "",
			check: func(t *testing.T, cfg Config) {
				if !reflect.DeepEqual(cfg, Default()) {
					t.Fatalf("got %+v, want the defaults", cfg)
				}
			},
		},
		{
			name: "a key that is not in the file keeps its default",
			toml: "[device]\nname = \"Lobby\"\n",
			check: func(t *testing.T, cfg Config) {
				if cfg.Device.Name != "Lobby" {
					t.Errorf("name = %q", cfg.Device.Name)
				}
				if cfg.Device.Timezone != "UTC" {
					t.Errorf("timezone = %q, want the default UTC", cfg.Device.Timezone)
				}
				if cfg.Web.Port != 80 {
					t.Errorf("web.port = %d, want the default 80", cfg.Web.Port)
				}
			},
		},
		{
			name: "every table",
			toml: `
[device]
name = "Lobby"
id = "px-12345678"
timezone = "America/New_York"
tier = "low"
[network]
mode = "static"
address = "192.168.1.50/24"
gateway = "192.168.1.1"
dns = ["192.168.1.1", "1.1.1.1"]
wifi_ssid = "Office"
wifi_psk = "secret"
[display]
rotation = 90
video_mode = "1920x1080@60"
power_method = "cec"
on_time = "07:30"
off_time = "22:00"
power_days = ["mon", "tue"]
[audio]
output = "hdmi"
volume = 50
[playback]
default_playlist = "lobby"
transition = "cut"
transition_ms = 250
image_duration = 20
shuffle = true
nightly_restart = ""
[[schedule]]
playlist = "weekday"
days = ["mon", "fri"]
start = "08:00"
end = "18:00"
[server]
url = "https://fleet.example.com"
token = "tok"
poll_seconds = 120
[web]
port = 8080
password = "pw"
[ssh]
enabled = false
[updates]
auto = true
[logging]
persist = true
`,
			check: func(t *testing.T, cfg Config) {
				if cfg.Device.Tier != "low" || cfg.Network.Mode != "static" {
					t.Errorf("device or network is wrong: %+v", cfg)
				}
				if len(cfg.Network.DNS) != 2 || cfg.Network.DNS[1] != "1.1.1.1" {
					t.Errorf("dns = %v", cfg.Network.DNS)
				}
				if cfg.Display.Rotation != 90 || cfg.Display.VideoMode != "1920x1080@60" {
					t.Errorf("display is wrong: %+v", cfg.Display)
				}
				if len(cfg.Schedule) != 1 || cfg.Schedule[0].Playlist != "weekday" {
					t.Errorf("schedule = %v", cfg.Schedule)
				}
				if cfg.SSH.Enabled {
					t.Error("ssh.enabled = true, want false")
				}
				if !cfg.Updates.Auto || !cfg.Logging.Persist || !cfg.Playback.Shuffle {
					t.Error("a true value did not arrive")
				}
				if cfg.Playback.NightlyRestart != "" {
					t.Errorf("nightly_restart = %q, want the empty value", cfg.Playback.NightlyRestart)
				}
			},
		},
		{
			name: "a key that we do not know is ignored",
			toml: "[device]\nname = \"Lobby\"\nfuture_key = 1\n",
			check: func(t *testing.T, cfg Config) {
				if cfg.Device.Name != "Lobby" {
					t.Errorf("name = %q", cfg.Device.Name)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tt.toml))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			tt.check(t, cfg)
		})
	}
}

func TestParseBadFiles(t *testing.T) {
	tests := []struct {
		name string
		toml string
	}{
		{name: "not toml at all", toml: "this is not a configuration"},
		{name: "a table that never ends", toml: "[device\nname = \"a\""},
		{name: "a string that never ends", toml: "[device]\nname = \"a\n"},
		{name: "a number where a string belongs", toml: "[device]\nname = 3\n"},
		{name: "a string where a number belongs", toml: "[web]\nport = \"eighty\"\n"},
		{name: "a table where a value belongs", toml: "[device]\n[device.name]\na = 1\n"},
		{name: "the same key twice", toml: "[device]\nname = \"a\"\nname = \"b\"\n"},
		{name: "random bytes", toml: "\x00\x01\x02\xff\xfe"},
		{name: "a very deep array", toml: "[device]\nname = " + strings.Repeat("[", 50)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tt.toml))
			if err == nil {
				t.Fatalf("want an error, got the configuration %+v", cfg)
			}
			// A bad file must still give a usable value, never a broken one.
			if !reflect.DeepEqual(cfg, Default()) {
				t.Fatalf("a bad file must give the defaults, got %+v", cfg)
			}
		})
	}
}

func TestIsTransition(t *testing.T) {
	for _, name := range Transitions {
		if !IsTransition(name) {
			t.Errorf("IsTransition(%q) = false", name)
		}
	}
	for _, name := range []string{"", "fade", "CUT", "push", "push-left "} {
		if IsTransition(name) {
			t.Errorf("IsTransition(%q) = true", name)
		}
	}
}
