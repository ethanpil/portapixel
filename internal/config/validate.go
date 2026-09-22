package config

import (
	"fmt"
	"net/netip"
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

// hasField reports if the error list names this field.
func hasField(errs Errors, field string) bool {
	for _, e := range errs {
		if e.Field == field {
			return true
		}
	}
	return false
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
	// The daemon renders these five values into /etc/network/interfaces and into
	// wpa_supplicant.conf. Both files belong to root, and each line of them is a
	// directive. A value that holds a line break would add a directive of its
	// own, so each value must have the shape that its file expects.
	if c.Network.Address != "" && !isIPPrefix(c.Network.Address) {
		add("network.address", "must be an IP address with a prefix length, for example 192.168.1.50/24")
	}
	if c.Network.Gateway != "" && !isIP(c.Network.Gateway) {
		add("network.gateway", "must be an IP address, for example 192.168.1.1")
	}
	for i, server := range c.Network.DNS {
		if !isIP(server) {
			add(fmt.Sprintf("network.dns[%d]", i), "must be an IP address, for example 1.1.1.1")
		}
	}
	if hasControl(c.Network.WifiSSID) {
		add("network.wifi_ssid", "must not hold a control character")
	}
	if hasControl(c.Network.WifiPSK) {
		add("network.wifi_psk", "must not hold a control character")
	}
	if c.Network.WifiPSK != "" && c.Network.WifiSSID == "" {
		add("network.wifi_ssid", "is necessary when wifi_psk is set")
	}
	if !isCountryCode(c.Network.WifiCountry) {
		add("network.wifi_country", "must be two letters, for example US, or empty")
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

	// [watchdog]. The lower bound of the timeout is six heartbeats; under that, a
	// device on a slow card would restart the browser while it still draws. The
	// upper bounds keep a typed value from making the ladder inert without saying
	// so: 20 restarts and a window of one day are both far outside any real use.
	if c.Watchdog.HeartbeatTimeout < 10 || c.Watchdog.HeartbeatTimeout > 600 {
		add("watchdog.heartbeat_timeout", "must be from 10 to 600 seconds")
	}
	if c.Watchdog.RestartsBeforeReboot < 0 || c.Watchdog.RestartsBeforeReboot > 20 {
		add("watchdog.restarts_before_reboot", "must be from 0 to 20. 0 means that the device never reboots by itself")
	}
	if c.Watchdog.RestartWindow < 1 || c.Watchdog.RestartWindow > 1440 {
		add("watchdog.restart_window", "must be from 1 to 1440 minutes")
	}

	// [[schedule]]
	for i, r := range c.Schedule {
		field := fmt.Sprintf("schedule[%d]", i)
		if r.Playlist == "" {
			add(field+".playlist", "must not be empty")
		}
		// The two times are both-or-neither. Both empty means the whole day, which
		// is what a rule of "weekends: this playlist" needs. One empty time is a
		// half-written rule, and a rule that matches nothing is worse than an error:
		// the person sees the default playlist and nothing says why.
		switch {
		case r.Start == "" && r.End == "":
		case r.Start == "":
			add(field+".start", "is necessary when there is an end time. Leave both out for the whole day.")
		case r.End == "":
			add(field+".end", "is necessary when there is a start time. Leave both out for the whole day.")
		default:
			clockTime(field+".start", r.Start)
			clockTime(field+".end", r.End)
		}
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

// isIP reports if value is one IP address and nothing else.
func isIP(value string) bool {
	_, err := netip.ParseAddr(value)
	return err == nil
}

// isIPPrefix reports if value is an IP address with a prefix length, for example
// "192.168.1.50/24". A bare address is also good: ifupdown-ng then takes the
// default prefix length.
func isIPPrefix(value string) bool {
	if _, err := netip.ParsePrefix(value); err == nil {
		return true
	}
	return isIP(value)
}

// isCountryCode reports if value is a two-letter regulatory domain, or empty.
// The value goes into wpa_supplicant.conf as a directive, so nothing else may
// pass. Either case is good: netcfg writes the code in capital letters.
func isCountryCode(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 2 {
		return false
	}
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

// hasControl reports if value holds a character that would change the meaning of
// a line in a rendered file, for example a line break or a tab.
func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// ParseClock reads a 24-hour time in the form HH:MM and gives the minutes after
// midnight. It is the one clock format of the product: the schedule rules, the
// screen times and the nightly browser restart all use it. Each of them asked
// the same question with its own code before, and the answers were different.
//
// The format is exact. Five characters, two digits, a colon, two digits. A value
// such as "9:5" is not accepted, because the renderer never writes one and a
// person who types one must see the fault in the admin UI.
func ParseClock(value string) (int, bool) {
	if len(value) != 5 || value[2] != ':' {
		return 0, false
	}
	h, ok := twoDigits(value[0:2])
	if !ok || h > 23 {
		return 0, false
	}
	m, ok := twoDigits(value[3:5])
	if !ok || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// isClockTime reports if value is a 24-hour time in the form HH:MM.
func isClockTime(value string) bool {
	_, ok := ParseClock(value)
	return ok
}

func twoDigits(s string) (int, bool) {
	if s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return 0, false
	}
	return int(s[0]-'0')*10 + int(s[1]-'0'), true
}
