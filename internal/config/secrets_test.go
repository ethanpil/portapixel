package config

import (
	"reflect"
	"testing"
)

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
