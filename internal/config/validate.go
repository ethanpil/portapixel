package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// FieldError names one key that holds a bad value.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// Errors is the list of bad keys. The web UI shows one message against each
// field, so the user can correct every fault in one pass.
type Errors []FieldError

func (e Errors) Error() string {
	parts := make([]string, len(e))
	for i, fe := range e {
		parts[i] = fe.Error()
	}
	return strings.Join(parts, "; ")
}

// Days are the day names that a schedule rule may use.
var Days = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// videoModePattern is the shape of a video_mode value, for example
// "1920x1080@60" or "1920x1080".
var videoModePattern = regexp.MustCompile(`^\d+x\d+(@\d+)?$`)

// Validate gives every rule that the configuration breaks. An empty list means
// that the configuration is good.
func (c Config) Validate() Errors {
	var errs Errors
	add := func(field, message string) {
		errs = append(errs, FieldError{Field: field, Message: message})
	}
	oneOf := func(field, value string, permitted ...string) {
		if !slices.Contains(permitted, value) {
			add(field, "must be one of "+strings.Join(permitted, ", "))
		}
	}
	clockTime := func(field, value string) {
		if value != "" && !isClockTime(value) {
			add(field, "must be a time in the form HH:MM")
		}
	}
	dayList := func(field string, days []string) {
		for _, d := range days {
			if !slices.Contains(Days, d) {
				add(field, "must hold day names from "+strings.Join(Days, ", "))
				return
			}
		}
	}

	// [device]
	if strings.TrimSpace(c.Device.Name) == "" {
		add("device.name", "must not be empty")
	}
	oneOf("device.tier", c.Device.Tier, "auto", "low", "high")
	if c.Device.Timezone == "" {
		add("device.timezone", "must be an IANA time zone name, for example UTC")
	} else if _, err := time.LoadLocation(c.Device.Timezone); err != nil {
		add("device.timezone", fmt.Sprintf("is not a time zone name that this device knows (%v)", err))
	}

	// [network]
	oneOf("network.mode", c.Network.Mode, "dhcp", "static")
	if c.Network.Mode == "static" && c.Network.Address == "" {
		add("network.address", "is necessary when mode is static")
	}
	if c.Network.WifiPSK != "" && c.Network.WifiSSID == "" {
		add("network.wifi_ssid", "is necessary when wifi_psk is set")
	}

	// [display]
	if !slices.Contains([]int{0, 90, 180, 270}, c.Display.Rotation) {
		add("display.rotation", "must be 0, 90, 180 or 270")
	}
	if c.Display.VideoMode != "" && !videoModePattern.MatchString(c.Display.VideoMode) {
		add("display.video_mode", "must be in the form 1920x1080 or 1920x1080@60")
	}
	oneOf("display.power_method", c.Display.PowerMethod, "auto", "cec", "dpms", "none")
	clockTime("display.on_time", c.Display.OnTime)
	clockTime("display.off_time", c.Display.OffTime)
	if (c.Display.OnTime == "") != (c.Display.OffTime == "") {
		add("display.on_time", "set both on_time and off_time, or set neither of them")
	}
	dayList("display.power_days", c.Display.PowerDays)

	// [audio]
	oneOf("audio.output", c.Audio.Output, "auto", "hdmi", "analog", "usb")
	if c.Audio.Volume < 0 || c.Audio.Volume > 100 {
		add("audio.volume", "must be from 0 to 100")
	}

	// [playback]
	if c.Playback.DefaultPlaylist == "" {
		add("playback.default_playlist", "must not be empty")
	}
	if !IsTransition(c.Playback.Transition) {
		add("playback.transition", "must be one of "+strings.Join(Transitions, ", "))
	}
	if c.Playback.TransitionMS < 0 {
		add("playback.transition_ms", "must not be less than zero")
	}
	if c.Playback.ImageDuration < 1 {
		add("playback.image_duration", "must be 1 second or more")
	}
	clockTime("playback.nightly_restart", c.Playback.NightlyRestart)

	// [[schedule]]
	for i, r := range c.Schedule {
		field := fmt.Sprintf("schedule[%d]", i)
		if r.Playlist == "" {
			add(field+".playlist", "must not be empty")
		}
		if r.Start == "" || r.End == "" {
			add(field+".start", "a rule needs a start time and an end time")
		}
		clockTime(field+".start", r.Start)
		clockTime(field+".end", r.End)
		dayList(field+".days", r.Days)
	}

	// [server]
	if c.Server.PollSeconds < 10 {
		add("server.poll_seconds", "must be 10 seconds or more")
	}

	// [web]
	if c.Web.Port < 1 || c.Web.Port > 65535 {
		add("web.port", "must be from 1 to 65535")
	}
	if c.Web.Password == "" {
		add("web.password", "must not be empty")
	}

	return errs
}

// isClockTime reports if value is a 24-hour time in the form HH:MM.
func isClockTime(value string) bool {
	if len(value) != 5 || value[2] != ':' {
		return false
	}
	h, ok := twoDigits(value[0:2])
	if !ok || h > 23 {
		return false
	}
	m, ok := twoDigits(value[3:5])
	return ok && m <= 59
}

func twoDigits(s string) (int, bool) {
	if s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return 0, false
	}
	return int(s[0]-'0')*10 + int(s[1]-'0'), true
}
