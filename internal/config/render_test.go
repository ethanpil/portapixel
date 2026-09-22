package config

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// full is a configuration in which every optional key has a value.
func full() Config {
	cfg := Default()
	cfg.Device.Name = "Lobby screen"
	cfg.Device.ID = "px-12345678"
	cfg.Device.Timezone = "America/New_York"
	cfg.Device.Tier = "low"
	cfg.Network.Mode = "static"
	cfg.Network.Address = "192.168.1.50/24"
	cfg.Network.Gateway = "192.168.1.1"
	cfg.Network.DNS = []string{"192.168.1.1", "1.1.1.1"}
	cfg.Network.WifiSSID = "Office"
	cfg.Network.WifiPSK = "a secret"
	cfg.Display.Rotation = 270
	cfg.Display.VideoMode = "1920x1080@60"
	cfg.Display.PowerMethod = "cec"
	cfg.Display.OnTime = "07:30"
	cfg.Display.OffTime = "22:00"
	cfg.Display.PowerDays = []string{"mon", "tue", "wed", "thu", "fri"}
	cfg.Audio.Output = "hdmi"
	cfg.Audio.Volume = 40
	cfg.Playback.DefaultPlaylist = "lobby"
	cfg.Playback.Transition = "push-left"
	cfg.Playback.TransitionMS = 300
	cfg.Playback.ImageDuration = 20
	cfg.Playback.Shuffle = true
	cfg.Playback.NightlyRestart = "04:15"
	cfg.Watchdog.Enabled = false
	cfg.Watchdog.HeartbeatTimeout = 45
	cfg.Watchdog.RestartsBeforeReboot = 0
	cfg.Watchdog.RestartWindow = 120
	cfg.Schedule = []Rule{
		{Playlist: "weekday", Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "08:00", End: "18:00"},
		{Playlist: "weekend", Start: "09:00", End: "17:00"},
	}
	cfg.Server.URL = "https://fleet.example.com"
	cfg.Server.Token = "enroll-token"
	cfg.Server.PollSeconds = 120
	cfg.Web.Port = 8080
	cfg.Web.Password = "a new password"
	cfg.SSH.Enabled = false
	cfg.Updates.Auto = true
	cfg.Logging.Persist = true
	return cfg
}

func TestRenderDefaultParsesBackToDefault(t *testing.T) {
	data := Render(Default())
	got, err := Parse(data)
	if err != nil {
		t.Fatalf("the rendered default file does not parse: %v\n%s", err, data)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Fatalf("the rendered default file parses to another value:\n got %+v\nwant %+v", got, Default())
	}
	if errs := got.Validate(); len(errs) > 0 {
		t.Fatalf("the rendered default file is not valid: %v", errs)
	}
}

func TestRenderRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "default", cfg: Default()},
		{name: "every optional key set", cfg: full()},
		{
			name: "one schedule rule",
			cfg: func() Config {
				c := Default()
				c.Schedule = []Rule{{Playlist: "a", Start: "08:00", End: "18:00"}}
				return c
			}(),
		},
		{
			name: "values with quotation marks and backslashes",
			cfg: func() Config {
				c := Default()
				c.Device.Name = `He said "hello" \ goodbye`
				c.Web.Password = `back\slash"quote`
				c.Network.WifiSSID = `Guest "WiFi"`
				c.Network.WifiPSK = `p\a"s"s`
				c.Server.Token = `to\ken`
				return c
			}(),
		},
		{
			name: "a value with a tab and other control characters",
			cfg: func() Config {
				c := Default()
				c.Device.Name = "a\tb\x01c"
				return c
			}(),
		},
		{
			name: "a name of other alphabets",
			cfg: func() Config {
				c := Default()
				c.Device.Name = "Empfang café 東京"
				return c
			}(),
		},
		{
			name: "empty optional strings",
			cfg: func() Config {
				c := Default()
				c.Playback.NightlyRestart = ""
				c.Network.WifiSSID = ""
				return c
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := Render(tt.cfg)
			parsed, err := Parse(first)
			if err != nil {
				t.Fatalf("the output does not parse: %v\n%s", err, first)
			}
			if !reflect.DeepEqual(parsed, tt.cfg) {
				t.Fatalf("parse gave another value:\n got %+v\nwant %+v", parsed, tt.cfg)
			}
			second := Render(parsed)
			if !bytes.Equal(first, second) {
				t.Fatalf("render is not stable:\n first:\n%s\nsecond:\n%s", first, second)
			}
		})
	}
}

func TestRenderHoldsEveryKey(t *testing.T) {
	// Every key of the plan template must be in the output, as a live key or as
	// a comment. If a key is not in this file, it does not exist.
	keys := []string{
		"name", "id", "timezone", "tier",
		"mode", "address", "gateway", "dns", "wifi_ssid", "wifi_psk",
		"rotation", "video_mode", "power_method", "on_time", "off_time", "power_days",
		"output", "volume",
		"default_playlist", "transition", "transition_ms", "image_duration", "shuffle", "nightly_restart",
		"heartbeat_timeout", "restarts_before_reboot", "restart_window",
		"playlist", "days", "start", "end",
		"url", "token", "poll_seconds",
		"port", "password",
		"enabled", "auto", "persist",
	}
	tables := []string{
		"[device]", "[network]", "[display]", "[audio]", "[playback]", "[watchdog]",
		"[[schedule]]", "[server]", "[web]", "[ssh]", "[updates]", "[logging]",
	}
	out := string(Render(Default()))
	for _, k := range keys {
		if !strings.Contains(out, k+" = ") {
			t.Errorf("the template has no key %q", k)
		}
	}
	for _, table := range tables {
		if !strings.Contains(out, table) {
			t.Errorf("the template has no table %q", table)
		}
	}
}

func TestRenderCommentsOutOptionalKeys(t *testing.T) {
	out := string(Render(Default()))
	for _, line := range []string{
		`# address = "192.168.1.50/24"`,
		`# gateway = "192.168.1.1"`,
		`# dns = ["192.168.1.1", "1.1.1.1"]`,
		`# video_mode = "1920x1080@60"`,
		`# on_time = "07:30"`,
		`# off_time = "22:00"`,
		"# [[schedule]]",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("the default template must hold the comment %q", line)
		}
	}

	// With values, the same keys are live keys and the example comments are gone.
	out = string(Render(full()))
	for _, line := range []string{
		`address = "192.168.1.50/24"`,
		`dns = ["192.168.1.1", "1.1.1.1"]`,
		`video_mode = "1920x1080@60"`,
		`on_time = "07:30"`,
		"[[schedule]]",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("the output must hold the live key %q", line)
		}
	}
	if strings.Contains(out, "# [[schedule]]") {
		t.Error("the example schedule must go away when real rules exist")
	}
	if strings.Contains(out, `# address =`) {
		t.Error("the example address must go away when an address is set")
	}
}

func TestRenderEndsEachLineWithOneNewline(t *testing.T) {
	out := Render(full())
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Error("the file must end with a line end")
	}
	if bytes.Contains(out, []byte("\r")) {
		t.Error("the file must use Unix line ends")
	}
}
