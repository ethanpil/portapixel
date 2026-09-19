package config

import (
	"reflect"
	"testing"
)

// TestMaskedAndMergeMaskedDoNotShareSlices covers the copy that the two
// functions give back. A plain copy of a struct keeps the slices of the
// original, so a change to the copy would reach the configuration that the
// daemon runs on.
func TestMaskedAndMergeMaskedDoNotShareSlices(t *testing.T) {
	base := func() Config {
		c := Default()
		c.Network.DNS = []string{"192.168.1.1", "1.1.1.1"}
		c.Display.PowerDays = []string{"mon", "tue"}
		c.Schedule = []Rule{{Playlist: "day", Days: []string{"mon", "tue"}, Start: "08:00", End: "18:00"}}
		return c
	}

	tests := []struct {
		name string
		make func(Config) Config
	}{
		{name: "Masked", make: func(c Config) Config { return c.Masked() }},
		{name: "MergeMasked", make: func(c Config) Config { return MergeMasked(Default(), c) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			out := tt.make(cfg)

			out.Network.DNS[0] = "9.9.9.9"
			out.Display.PowerDays[0] = "sun"
			out.Schedule[0].Days[0] = "sun"
			out.Schedule[0].Playlist = "night"

			if cfg.Network.DNS[0] != "192.168.1.1" {
				t.Errorf("the copy shares network.dns: %q", cfg.Network.DNS[0])
			}
			if cfg.Display.PowerDays[0] != "mon" {
				t.Errorf("the copy shares display.power_days: %q", cfg.Display.PowerDays[0])
			}
			if cfg.Schedule[0].Days[0] != "mon" {
				t.Errorf("the copy shares the days of a schedule rule: %q", cfg.Schedule[0].Days[0])
			}
			if cfg.Schedule[0].Playlist != "day" {
				t.Errorf("the copy shares the schedule: %q", cfg.Schedule[0].Playlist)
			}
		})
	}
}

func TestMasked(t *testing.T) {
	cfg := Default()
	cfg.Network.WifiPSK = "wifi secret"
	cfg.Web.Password = "admin secret"
	cfg.Server.Token = "fleet token"

	got := cfg.Masked()
	for name, value := range map[string]string{
		"network.wifi_psk": got.Network.WifiPSK,
		"web.password":     got.Web.Password,
		"server.token":     got.Server.Token,
	} {
		if value != Mask {
			t.Errorf("%s = %q, want the mask", name, value)
		}
	}
	// The other keys must come through.
	if got.Device.Name != cfg.Device.Name || got.Web.Port != cfg.Web.Port {
		t.Error("Masked must change the secrets only")
	}
	// The original must not change.
	if cfg.Web.Password != "admin secret" {
		t.Error("Masked must give a copy")
	}
}

func TestMaskedKeepsEmptySecretsEmpty(t *testing.T) {
	cfg := Default()
	cfg.Network.WifiPSK = ""
	cfg.Server.Token = ""
	got := cfg.Masked()
	if got.Network.WifiPSK != "" || got.Server.Token != "" {
		t.Fatal("an empty secret must stay empty, so the UI can see that there is no value")
	}
}

func TestMergeMasked(t *testing.T) {
	old := Default()
	old.Network.WifiPSK = "old wifi"
	old.Web.Password = "old admin"
	old.Server.Token = "old token"

	tests := []struct {
		name      string
		incoming  func(c *Config)
		wantWifi  string
		wantPass  string
		wantToken string
	}{
		{
			name: "the mask keeps the old secrets",
			incoming: func(c *Config) {
				c.Network.WifiPSK = Mask
				c.Web.Password = Mask
				c.Server.Token = Mask
			},
			wantWifi:  "old wifi",
			wantPass:  "old admin",
			wantToken: "old token",
		},
		{
			name: "a new value replaces the secret",
			incoming: func(c *Config) {
				c.Network.WifiPSK = "new wifi"
				c.Web.Password = "new admin"
				c.Server.Token = "new token"
			},
			wantWifi:  "new wifi",
			wantPass:  "new admin",
			wantToken: "new token",
		},
		{
			name: "an empty value clears the secret",
			incoming: func(c *Config) {
				c.Network.WifiPSK = ""
				c.Web.Password = ""
				c.Server.Token = ""
			},
		},
		{
			name: "one masked field and one new field",
			incoming: func(c *Config) {
				c.Network.WifiPSK = Mask
				c.Web.Password = "new admin"
				c.Server.Token = Mask
			},
			wantWifi:  "old wifi",
			wantPass:  "new admin",
			wantToken: "old token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			incoming := Default()
			tt.incoming(&incoming)
			got := MergeMasked(old, incoming)
			if got.Network.WifiPSK != tt.wantWifi {
				t.Errorf("wifi_psk = %q, want %q", got.Network.WifiPSK, tt.wantWifi)
			}
			if got.Web.Password != tt.wantPass {
				t.Errorf("web.password = %q, want %q", got.Web.Password, tt.wantPass)
			}
			if got.Server.Token != tt.wantToken {
				t.Errorf("server.token = %q, want %q", got.Server.Token, tt.wantToken)
			}
		})
	}
}

func TestMaskThenMergeKeepsTheConfiguration(t *testing.T) {
	// This is the path of the web UI: GET gives the masked copy, the user changes
	// one field, PUT sends it back.
	cfg := Default()
	cfg.Network.WifiPSK = "wifi secret"
	cfg.Web.Password = "admin secret"
	cfg.Server.Token = "fleet token"

	incoming := cfg.Masked()
	incoming.Device.Name = "Lobby"

	got := MergeMasked(cfg, incoming)
	want := cfg
	want.Device.Name = "Lobby"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
