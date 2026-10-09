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
	VideoMode string `toml:"video_mode,omitempty" json:"video_mode,omitempty"`
	// VideoOutput is the video output of mpv: "gpu" (OpenGL on the GPU), "drm"
	// (direct to the display, no GPU) or "auto" (gpu when the graphics card has a
	// hardware OpenGL driver, else drm). See internal/device/player.
	VideoOutput string   `toml:"video_output" json:"video_output"` // auto | gpu | drm
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
	// Motion permits the moving crossfade: in a crossfade from a video, both
	// items move. "auto" is on for an x86_64 device and a Raspberry Pi 5 or
	// Compute Module 5, and off for every other board. "off" keeps the frozen
	// last frame of the video. See internal/device/player.
	Motion string `toml:"motion" json:"motion"` // auto | on | off
	// NightlyRestart is the time of the daily player restart. An empty value
	// stops it (D30).
	NightlyRestart string `toml:"nightly_restart" json:"nightly_restart"`
}

// Watchdog is the [watchdog] table: the recovery ladder of the player (D30,
// plan 3.3). Every step of the ladder is tunable here, and the whole ladder can
// be switched off.
//
// The nightly player restart is the fourth step, and it is not here: it is
// playback.nightly_restart, because it is a time of day and not a threshold.
type Watchdog struct {
	// Enabled switches the whole ladder off when it is false. The player still
	// starts again when it ends, and a player that freezes then stays on the
	// screen.
	Enabled bool `toml:"enabled" json:"enabled"`
	// HeartbeatTimeout is the time, in seconds, that the player may stay silent or
	// frozen. The daemon asks mpv for its state every 2 seconds. No answer, a
	// video that does not move, or an image that stays longer than its duration
	// plus this time, restarts the player.
	HeartbeatTimeout int `toml:"heartbeat_timeout" json:"heartbeat_timeout"`
	// RestartsBeforeReboot is how many player restarts inside RestartWindow make
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

// Transitions are the transition names that a playlist may use (D14). "fade"
// goes through black and "fade-white" through white. A wipe, a push, a slide-in
// and a slide-out move in the direction of their word. A push moves both
// items, a slide-in moves the new item over the old one, and a slide-out moves
// the old item away. "zoom-out" shrinks the old item into the centre, and
// "split" opens it from the centre like a barn door.
// playback.transition_ms is the length of each transition except "cut". The
// player script internal/device/player/transitions.lua and the editor
// web/shared/playlist-editor.js know the same words.
var Transitions = []string{
	"cut", "fade", "fade-white", "crossfade",
	"wipe-left", "wipe-right", "wipe-up", "wipe-down",
	"push-left", "push-right", "push-up", "push-down",
	"slide-in-left", "slide-in-right", "slide-in-up", "slide-in-down",
	"slide-out-left", "slide-out-right", "slide-out-up", "slide-out-down",
	"zoom-out", "split",
}

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
		},
		Network: Network{
			Mode: "dhcp",
		},
		Display: Display{
			Rotation:    0,
			VideoOutput: "auto",
			PowerMethod: "auto",
		},
		Audio: Audio{
			Output: "auto",
			Volume: 100,
		},
		Playback: Playback{
			DefaultPlaylist: "default",
			Transition:      "fade",
			TransitionMS:    500,
			ImageDuration:   10,
			Shuffle:         false,
			Motion:          "auto",
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
