package manifest

import "time"

// Status is the health report of one device. The device serves it at
// /api/status, sends it in each heartbeat, and shows parts of it on the
// fallback screen and the dashboard (plan section 8, health).
type Status struct {
	DeviceID string   `json:"device_id"`
	Name     string   `json:"name"`
	MDNSName string   `json:"mdns_name"`
	IPs      []string `json:"ips"`

	Version             string `json:"version"`               // daemon release
	ImageVersion        string `json:"image_version"`         // image release (D50)
	PackageManifestHash string `json:"package_manifest_hash"` // hash of the installed-packages manifest (D50)
	Arch                string `json:"arch"`
	Tier                string `json:"tier"` // low | high

	UptimeSeconds   int64   `json:"uptime_seconds"`
	Load            float64 `json:"load"` // one-minute load average
	TempC           float64 `json:"temp_c"`
	RAMTotalBytes   uint64  `json:"ram_total_bytes"`
	RAMFreeBytes    uint64  `json:"ram_free_bytes"`
	MediaTotalBytes uint64  `json:"media_total_bytes"`
	MediaFreeBytes  uint64  `json:"media_free_bytes"`

	// The words come from internal/device/browser: the State constants and the
	// Name of the navigator that the ladder chose (contract section 7).
	BrowserState     string `json:"browser_state"`   // stopped | starting | running | waiting-for-display | disabled
	NavigationRung   string `json:"navigation_rung"` // cdp | relaunch
	DisplayConnected bool   `json:"display_connected"`
	ScreenOn         bool   `json:"screen_on"`

	NowPlaying *NowPlaying `json:"now_playing,omitempty"`

	Paired         bool      `json:"paired"`
	ServerURL      string    `json:"server_url"`
	LastSync       time.Time `json:"last_sync"`        // zero when the device never synced
	LastSyncResult string    `json:"last_sync_result"` // ok | error | never
	SyncError      string    `json:"sync_error,omitempty"`

	ClockSynced bool      `json:"clock_synced"` // chrony reports a synchronised clock (D40)
	Timezone    string    `json:"timezone"`
	Warnings    []Warning `json:"warnings"` // change-me nags and other loud messages

	// HardwareChanged is true after a hardware repair, until the fleet server
	// confirms the new identity (D21).
	HardwareChanged bool `json:"hardware_changed"`

	// Codecs is what the player found out about the video formats of this
	// device. It is empty until the player sent its first heartbeat.
	Codecs CodecReport `json:"codecs,omitempty"`

	// PairingCode goes only to loopback callers, which is the fallback screen
	// (D46). The LAN copy of Status leaves it out.
	PairingCode string `json:"pairing_code,omitempty"`

	// ConfigFromShadow is true when the device runs from the shadow copy of the
	// configuration because the PPMEDIA copy is missing or bad (D38).
	ConfigFromShadow bool `json:"config_from_shadow"`

	Update UpdateState `json:"update"`
}

// Warning is one loud message for the dashboard and the fallback screen.
//
// The code is what a program tests. The message is the sentence that a person
// reads. The two admin UIs matched the first words of the message before this
// type existed, so one better sentence broke a banner.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// The warning codes. The list is the contract between the daemon and the two
// admin UIs.
const (
	WarnWebPassword    = "default-web-password"
	WarnRootPassword   = "default-root-password"
	WarnTimezoneUTC    = "timezone-utc"
	WarnClockUnsynced  = "clock-unsynced"
	WarnConfigShadow   = "config-shadow"
	WarnConfigRepaired = "config-repaired"
	WarnConfigBadEdit  = "config-bad-edit"
	// WarnConfigManagedIgnored says that a hand edit of portapixel.toml changed a
	// field that the fleet server owns while this device is paired (D48). The
	// running value stays and the other edited fields apply as they are.
	WarnConfigManagedIgnored = "config-managed-ignored"
	WarnPlaylistProblem      = "playlist-problem"
	WarnHardwareChanged      = "hardware-changed"
	WarnUpdateRolledBack     = "update-rolled-back"
	// WarnServerInsecure says that the fleet server address is http:// on an
	// address that is not local, so the device token goes over the internet in
	// clear text.
	WarnServerInsecure = "server-insecure"
)

// CodecReport says which video formats a device decodes (D12). The player probes
// MediaCapabilities one time and sends the answer with its first heartbeat.
//
// The outer key is the codec name, for example "h264". The inner key is the
// picture height as a word, "1080" or "2160". The field names are the names of
// the browser API, so the admin UI needs no translation step.
type CodecReport map[string]map[string]CodecSupport

// CodecSupport is the answer of MediaCapabilities for one codec and one size.
// PowerEfficient is a pointer, because "the browser does not know" is a third
// answer that matters: it means hardware decode is unknown, not absent.
type CodecSupport struct {
	Supported      bool  `json:"supported"`
	Smooth         bool  `json:"smooth"`
	PowerEfficient *bool `json:"powerEfficient"`
}

// NowPlaying is what the display shows at this moment.
type NowPlaying struct {
	Playlist string    `json:"playlist"`
	Index    int       `json:"index"`
	Item     string    `json:"item"` // file name or URL
	Kind     string    `json:"kind"` // image | video | url
	Since    time.Time `json:"since"`
}

// UpdateState is the state of the self-update (section 15).
type UpdateState struct {
	// State is one of the words below.
	State string `json:"state"`
	// Current is the release that runs now.
	Current string `json:"current"`
	// Available is the release that the device can install, or "".
	Available string `json:"available,omitempty"`
	// Source says where the release comes from: "github" or "fleet" or
	// "sideload".
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}

// The words of UpdateState.State.
const (
	UpdateIdle        = "idle"
	UpdateChecking    = "checking"
	UpdateAvailable   = "available"
	UpdateDownloading = "downloading"
	UpdateVerifying   = "verifying"
	UpdateApplying    = "applying"
	UpdateRestarting  = "restarting"
	UpdateFailed      = "failed"
	UpdateRolledBack  = "rolled-back"
)
