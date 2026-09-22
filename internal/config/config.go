package config

import (
	"fmt"
	"slices"

	_ "time/tzdata" // the time zone database, see doc.go

	"github.com/BurntSushi/toml"
)

// FileName is the name of the configuration file at the root of PPMEDIA.
// ShadowName is the last-known-good copy in the state directory (D38).
const (
	FileName   = "portapixel.toml"
	ShadowName = "portapixel.toml.lkg"
)

// Config is the whole of portapixel.toml.
type Config struct {
	Device   Device   `toml:"device" json:"device"`
	Network  Network  `toml:"network" json:"network"`
	Display  Display  `toml:"display" json:"display"`
	Audio    Audio    `toml:"audio" json:"audio"`
	Playback Playback `toml:"playback" json:"playback"`
	Watchdog Watchdog `toml:"watchdog" json:"watchdog"`
	Schedule []Rule   `toml:"schedule" json:"schedule"`
	Server   Server   `toml:"server" json:"server"`
	Web      Web      `toml:"web" json:"web"`
	SSH      SSH      `toml:"ssh" json:"ssh"`
	Updates  Updates  `toml:"updates" json:"updates"`
	Logging  Logging  `toml:"logging" json:"logging"`
}

// Device is the [device] table.
type Device struct {
	Name string `toml:"name" json:"name"`
	// ID comes from the hardware at each boot. The value in the file is a
	// reference copy for the person who reads the card (D21).
	ID       string `toml:"id" json:"id"`
	Timezone string `toml:"timezone" json:"timezone"`
	Tier     string `toml:"tier" json:"tier"` // auto | low | high
}

// Network is the [network] table. The daemon renders
// /etc/network/interfaces and wpa_supplicant.conf from it.
type Network struct {
	Mode     string   `toml:"mode" json:"mode"` // dhcp | static
	Address  string   `toml:"address,omitempty" json:"address,omitempty"`
	Gateway  string   `toml:"gateway,omitempty" json:"gateway,omitempty"`
	DNS      []string `toml:"dns,omitempty" json:"dns,omitempty"`
	WifiSSID string   `toml:"wifi_ssid" json:"wifi_ssid"`
	WifiPSK  string   `toml:"wifi_psk" json:"wifi_psk"`
	// WifiCountry is the two-letter regulatory domain, for example "US" or "DE".
	// An empty value leaves the domain out of wpa_supplicant.conf. Some radios
	// then permit fewer channels, and a 5 GHz network can be invisible.
	WifiCountry string `toml:"wifi_country,omitempty" json:"wifi_country,omitempty"`
}

// Display is the [display] table.
type Display struct {
	Rotation int `toml:"rotation" json:"rotation"` // 0 | 90 | 180 | 270
	// VideoMode forces the output mode of a display that gives a bad EDID
	// (D49). An empty value trusts the display.
	VideoMode   string   `toml:"video_mode,omitempty" json:"video_mode,omitempty"`
	PowerMethod string   `toml:"power_method" json:"power_method"` // auto | cec | dpms | none
	OnTime      string   `toml:"on_time,omitempty" json:"on_time,omitempty"`
	OffTime     string   `toml:"off_time,omitempty" json:"off_time,omitempty"`
	PowerDays   []string `toml:"power_days,omitempty" json:"power_days,omitempty"`
}

// Audio is the [audio] table.
type Audio struct {
	Output string `toml:"output" json:"output"` // auto | hdmi | analog | usb
	Volume int    `toml:"volume" json:"volume"` // 0 to 100
}

// Playback is the [playback] table.
type Playback struct {
	DefaultPlaylist string `toml:"default_playlist" json:"default_playlist"`
	Transition      string `toml:"transition" json:"transition"`
	TransitionMS    int    `toml:"transition_ms" json:"transition_ms"`
	ImageDuration   int    `toml:"image_duration" json:"image_duration"`
	Shuffle         bool   `toml:"shuffle" json:"shuffle"`
	// NightlyRestart is the time of the daily browser restart. An empty value
	// stops it (D30).
	NightlyRestart string `toml:"nightly_restart" json:"nightly_restart"`
}

// Watchdog is the [watchdog] table: the recovery ladder of the browser (D30,
// plan 3.3). Every step of the ladder is tunable here, and the whole ladder can
// be switched off.
//
// The nightly browser restart is the fourth step, and it is not here: it is
// playback.nightly_restart, because it is a time of day and not a threshold.
type Watchdog struct {
	// Enabled switches the whole ladder off when it is false. The browser still
	// starts again when it dies, and a page that stops sending heartbeats then
	// stays on the screen.
	Enabled bool `toml:"enabled" json:"enabled"`
	// HeartbeatTimeout is the silence of the player, in seconds, that means "the
	// page is dead". The player sends a heartbeat every 5 seconds.
	HeartbeatTimeout int `toml:"heartbeat_timeout" json:"heartbeat_timeout"`
	// RestartsBeforeReboot is how many browser restarts inside RestartWindow make
	// the device reboot. 0 means that the device never reboots by itself.
	RestartsBeforeReboot int `toml:"restarts_before_reboot" json:"restarts_before_reboot"`
	// RestartWindow is the length of that window, in minutes.
	RestartWindow int `toml:"restart_window" json:"restart_window"`
}

// Rule is one [[schedule]] table. The first rule that matches wins (D17).
type Rule struct {
	Playlist string   `toml:"playlist" json:"playlist"`
	Days     []string `toml:"days,omitempty" json:"days,omitempty"` // empty means every day
	Start    string   `toml:"start" json:"start"`                   // "HH:MM"
	End      string   `toml:"end" json:"end"`
}

// Server is the [server] table.
type Server struct {
	URL string `toml:"url" json:"url"`
	// Token is the token that the user typed: an enrollment token or a device
	// token. The per-device token that the server gives back lives in
	// state.json, so that a flashed card stays clonable (D25).
	Token       string `toml:"token" json:"token"`
	PollSeconds int    `toml:"poll_seconds" json:"poll_seconds"`
}

// Web is the [web] table.
type Web struct {
	Port     int    `toml:"port" json:"port"`
	Password string `toml:"password" json:"password"`
}

// SSH is the [ssh] table.
type SSH struct {
	Enabled bool `toml:"enabled" json:"enabled"`
}

// Updates is the [updates] table.
type Updates struct {
	Auto bool `toml:"auto" json:"auto"`
}

// Logging is the [logging] table.
type Logging struct {
	Persist bool `toml:"persist" json:"persist"`
}

// Transitions are the transition names that a playlist may use (D14).
var Transitions = []string{"crossfade", "push-left", "push-right", "push-up", "push-down", "cut"}

// IsTransition reports if name is a transition that the player knows.
func IsTransition(name string) bool { return slices.Contains(Transitions, name) }

// Default gives the configuration of a new device. It is the same set of values
// that the template in the plan shows.
func Default() Config {
	return Config{
		Device: Device{
			Name:     "PortaPixel",
			ID:       "px-00000000",
			Timezone: "UTC",
			Tier:     "auto",
		},
		Network: Network{
			Mode: "dhcp",
		},
		Display: Display{
			Rotation:    0,
			PowerMethod: "auto",
		},
		Audio: Audio{
			Output: "auto",
			Volume: 100,
		},
		Playback: Playback{
			DefaultPlaylist: "default",
			Transition:      "crossfade",
			TransitionMS:    500,
			ImageDuration:   10,
			Shuffle:         false,
			NightlyRestart:  "03:30",
		},
		Watchdog: Watchdog{
			Enabled:              true,
			HeartbeatTimeout:     30,
			RestartsBeforeReboot: 4,
			RestartWindow:        60,
		},
		Server: Server{
			PollSeconds: 60,
		},
		Web: Web{
			Port:     80,
			Password: "portapixel",
		},
		SSH:     SSH{Enabled: true},
		Updates: Updates{Auto: false},
		Logging: Logging{Persist: false},
	}
}

// Parse reads a portapixel.toml. A key that the file does not hold keeps its
// default value, so an old or a short file still gives a complete
// configuration. Parse does not check the values: use Validate for that.
func Parse(data []byte) (Config, error) {
	cfg := Default()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Default(), fmt.Errorf("bad configuration file: %w", err)
	}
	return cfg, nil
}
