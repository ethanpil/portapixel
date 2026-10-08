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

	UptimeSeconds   int64   `json:"uptime_seconds"`
	Load            float64 `json:"load"` // one-minute load average
	TempC           float64 `json:"temp_c"`
	RAMTotalBytes   uint64  `json:"ram_total_bytes"`
	RAMFreeBytes    uint64  `json:"ram_free_bytes"`
	MediaTotalBytes uint64  `json:"media_total_bytes"`
	MediaFreeBytes  uint64  `json:"media_free_bytes"`

	// PlayerState uses the State words of the player supervisor. VideoOutput is
	// the video output of the player. Hwdec is the decoder of the current video,
	// and "" when no video plays. An empty value means that the player did not
	// report it.
	PlayerState      string `json:"player_state"` // stopped | starting | running | waiting-for-display | disabled
	VideoOutput      string `json:"video_output"` // gpu | drm
	Hwdec            string `json:"hwdec"`
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
	// WarnMDNSNameTaken says that another device on the network already answers for
	// the mDNS name of this device, so this device announces
	// portapixel-<last4>.local instead (D20).
	WarnMDNSNameTaken = "mdns-name-taken"
	// WarnZramOff says that this device has less than 1 GiB of memory and no zram
	// swap (plan section 16, the low RAM row). The OS layer configures zram; the
	// daemon only reports it.
	WarnZramOff = "zram-off"
	// WarnAudioApplyFailed says that the device could not put [audio] into effect:
	// the ALSA mixer refused the volume. The picture is not affected, and the
	// message names the control that refused (D11).
	WarnAudioApplyFailed = "audio-apply-failed"
	// WarnRebootLoop says that the watchdog ladder rebooted this device more than
	// once in the last hour. The device stops every automatic action and shows this
	// on the fallback screen, so a person sees the state instead of a box that
	// reboots for ever (plan 3.3, rung 4).
	WarnRebootLoop = "reboot-loop"
)

// NowPlaying is what the display shows at this moment.
type NowPlaying struct {
	Playlist string    `json:"playlist"`
	Index    int       `json:"index"`
	Item     string    `json:"item"` // file name or URL
	Kind     string    `json:"kind"` // image | video | url
	Since    time.Time `json:"since"`
	// SHA256 is the hash of the file on the screen. The fleet server finds its
	// library object with it. It is empty for a URL item, and for a local file
	// that the device did not hash yet.
	SHA256 string `json:"sha256,omitempty"`
	// DroppedFrames counts the frames of the current video that the player
	// dropped. It is 0 for an image.
	DroppedFrames int `json:"dropped_frames"`
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
