package config

import (
	"reflect"
	"testing"
)

// TestRepairAlwaysGivesAGoodConfig walks every fault that Validate can report
// and makes sure that the repaired configuration breaks no rule. The daemon runs
// on the output of Repair, so a fault that stays would give the device a value
// that nothing else expects.
func TestRepairAlwaysGivesAGoodConfig(t *testing.T) {
	for _, tt := range validateCases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.change(&cfg)

			out, bad := Repair(cfg)
			if tt.wantField == "" {
				if len(bad) != 0 {
					t.Fatalf("Repair found a fault in a good configuration: %v", bad)
				}
				if !reflect.DeepEqual(out, cfg) {
					t.Fatal("Repair changed a configuration that breaks no rule")
				}
				return
			}
			if !hasField(bad, tt.wantField) {
				t.Fatalf("Repair reported %v, want a fault for %q", bad, tt.wantField)
			}
			if errs := out.Validate(); len(errs) > 0 {
				t.Fatalf("the repaired configuration still breaks a rule: %v", errs)
			}
		})
	}
}

// TestRepairKeepsEveryOtherValue is the point of the repair: one typed
// character on the stick must cost the value that holds it and nothing else.
func TestRepairKeepsEveryOtherValue(t *testing.T) {
	cfg := Default()
	cfg.Device.Name = "Lobby"
	cfg.Device.Timezone = "America/New_York"
	cfg.Network.Mode = "static"
	cfg.Network.Address = "192.168.1.50/24"
	cfg.Network.Gateway = "192.168.1.1"
	cfg.Network.WifiSSID = "Guest"
	cfg.Network.WifiPSK = "a secret"
	cfg.Web.Password = "letmein"
	cfg.Schedule = []Rule{{Playlist: "day", Start: "08:00", End: "18:00"}}
	cfg.Display.Rotation = 45 // the one fault

	out, bad := Repair(cfg)
	if len(bad) != 1 || bad[0].Field != "display.rotation" {
		t.Fatalf("Repair reported %v, want one fault for display.rotation", bad)
	}
	if out.Display.Rotation != Default().Display.Rotation {
		t.Errorf("rotation = %d, want the default", out.Display.Rotation)
	}
	if out.Device.Name != "Lobby" || out.Device.Timezone != "America/New_York" {
		t.Errorf("the repair lost a device value: %+v", out.Device)
	}
	if out.Network.Mode != "static" || out.Network.Address != "192.168.1.50/24" ||
		out.Network.Gateway != "192.168.1.1" || out.Network.WifiPSK != "a secret" {
		t.Errorf("the repair lost a network value: %+v", out.Network)
	}
	if out.Web.Password != "letmein" {
		t.Errorf("the repair lost the password: %q", out.Web.Password)
	}
	if len(out.Schedule) != 1 || out.Schedule[0].Playlist != "day" {
		t.Errorf("the repair lost the schedule: %+v", out.Schedule)
	}
	// The configuration that came in must not change.
	if cfg.Display.Rotation != 45 {
		t.Error("Repair changed the configuration that it was given")
	}
}

// TestRepairUnits covers the values that go together. Half of a static setup,
// or a WiFi key with no network name, is worse than none of it.
func TestRepairUnits(t *testing.T) {
	tests := []struct {
		name  string
		make  func(c *Config)
		check func(t *testing.T, out Config)
	}{
		{
			name: "a bad static address gives DHCP back and keeps the wifi",
			make: func(c *Config) {
				c.Network.Mode = "static"
				c.Network.Address = "the lobby switch"
				c.Network.Gateway = "192.168.1.1"
				c.Network.DNS = []string{"1.1.1.1"}
				c.Network.WifiSSID = "Guest"
				c.Network.WifiPSK = "a secret"
			},
			check: func(t *testing.T, out Config) {
				if out.Network.Mode != "dhcp" || out.Network.Address != "" ||
					out.Network.Gateway != "" || len(out.Network.DNS) != 0 {
					t.Errorf("the static unit stayed: %+v", out.Network)
				}
				if out.Network.WifiSSID != "Guest" || out.Network.WifiPSK != "a secret" {
					t.Errorf("the repair lost the wifi settings: %+v", out.Network)
				}
			},
		},
		{
			name: "a bad network name clears the wifi and keeps the address",
			make: func(c *Config) {
				c.Network.Mode = "static"
				c.Network.Address = "192.168.1.50/24"
				c.Network.WifiSSID = "Lobby\n\tkey_mgmt=NONE"
				c.Network.WifiPSK = "a secret"
			},
			check: func(t *testing.T, out Config) {
				if out.Network.WifiSSID != "" || out.Network.WifiPSK != "" {
					t.Errorf("the wifi unit stayed: %+v", out.Network)
				}
				if out.Network.Mode != "static" || out.Network.Address != "192.168.1.50/24" {
					t.Errorf("the repair lost the static address: %+v", out.Network)
				}
			},
		},
		{
			name: "one bad screen time clears both of them",
			make: func(c *Config) {
				c.Display.OnTime = "7:30"
				c.Display.OffTime = "22:00"
			},
			check: func(t *testing.T, out Config) {
				if out.Display.OnTime != "" || out.Display.OffTime != "" {
					t.Errorf("the screen times stayed: %+v", out.Display)
				}
			},
		},
		{
			name: "a bad schedule rule goes away and the good rules stay",
			make: func(c *Config) {
				c.Schedule = []Rule{
					{Playlist: "morning", Start: "08:00", End: "12:00"},
					{Playlist: "afternoon", Start: "12:00", End: "25:00"},
					{Playlist: "evening", Start: "18:00", End: "22:00"},
				}
			},
			check: func(t *testing.T, out Config) {
				if len(out.Schedule) != 2 {
					t.Fatalf("the schedule holds %d rules, want 2: %+v", len(out.Schedule), out.Schedule)
				}
				if out.Schedule[0].Playlist != "morning" || out.Schedule[1].Playlist != "evening" {
					t.Errorf("the repair dropped the wrong rule: %+v", out.Schedule)
				}
			},
		},
		{
			name: "the last bad rule leaves an empty schedule",
			make: func(c *Config) {
				c.Schedule = []Rule{{Playlist: "", Start: "08:00", End: "12:00"}}
			},
			check: func(t *testing.T, out Config) {
				if len(out.Schedule) != 0 {
					t.Errorf("the schedule holds %+v, want nothing", out.Schedule)
				}
			},
		},
		{
			// Each watchdog value stands alone: half of the ladder is still a ladder.
			name: "a bad heartbeat timeout keeps the reboot step",
			make: func(c *Config) {
				c.Watchdog.HeartbeatTimeout = 2
				c.Watchdog.RestartsBeforeReboot = 0
				c.Watchdog.RestartWindow = 30
			},
			check: func(t *testing.T, out Config) {
				if out.Watchdog.HeartbeatTimeout != Default().Watchdog.HeartbeatTimeout {
					t.Errorf("heartbeat_timeout = %d, want the default", out.Watchdog.HeartbeatTimeout)
				}
				if out.Watchdog.RestartsBeforeReboot != 0 || out.Watchdog.RestartWindow != 30 {
					t.Errorf("the repair changed another watchdog value: %+v", out.Watchdog)
				}
			},
		},
		{
			name: "an empty password takes the default password",
			make: func(c *Config) { c.Web.Password = "" },
			check: func(t *testing.T, out Config) {
				if out.Web.Password != Default().Web.Password {
					t.Errorf("password = %q, want the default", out.Web.Password)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.make(&cfg)
			out, bad := Repair(cfg)
			if len(bad) == 0 {
				t.Fatal("Repair found no fault")
			}
			if errs := out.Validate(); len(errs) > 0 {
				t.Fatalf("the repaired configuration still breaks a rule: %v", errs)
			}
			tt.check(t, out)
		})
	}
}
