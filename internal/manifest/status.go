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

	BrowserState     string `json:"browser_state"`   // starting | running | restarting | stopped | waiting-for-display
	NavigationRung   string `json:"navigation_rung"` // dbus | webdriver | relaunch
	DisplayConnected bool   `json:"display_connected"`
	ScreenOn         bool   `json:"screen_on"`

	NowPlaying *NowPlaying `json:"now_playing,omitempty"`

	Paired         bool      `json:"paired"`
	ServerURL      string    `json:"server_url"`
	LastSync       time.Time `json:"last_sync"`        // zero when the device never synced
	LastSyncResult string    `json:"last_sync_result"` // ok | error | never
	SyncError      string    `json:"sync_error,omitempty"`

	ClockSynced bool     `json:"clock_synced"` // chrony reports a synchronised clock (D40)
	Timezone    string   `json:"timezone"`
	Warnings    []string `json:"warnings"` // change-me nags and other loud messages

	// PairingCode goes only to loopback callers, which is the fallback screen
	// (D46). The LAN copy of Status leaves it out.
	PairingCode string `json:"pairing_code,omitempty"`

	// ConfigFromShadow is true when the device runs from the shadow copy of the
	// configuration because the PPMEDIA copy is missing or bad (D38).
	ConfigFromShadow bool `json:"config_from_shadow"`

	Update UpdateState `json:"update"`
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
	State     string `json:"state"`               // idle | checking | downloading | verifying | staged | applying | failed
	Available string `json:"available,omitempty"` // version that the device can install
	Error     string `json:"error,omitempty"`
}
