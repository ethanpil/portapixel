package db

import "time"

// Device is one row of the devices table. The admin API sends it as JSON, so the
// field names here are the names that the UI reads.
//
// A request that waits for approval has no row of this kind: it lives in
// pending_enrollments, so that an enroll request can never change a screen that
// works. The admin API sends such a request in the same list with Pending set, so
// the UI keeps one shape for "every screen that I must look at".
type Device struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	GroupID     int64     `json:"group_id"`
	GroupName   string    `json:"group_name"`
	HardwareID  string    `json:"hardware_id"`
	Version     string    `json:"version"`
	LastIP      string    `json:"last_ip"`
	PollSeconds int       `json:"poll_seconds"`
	CreatedAt   time.Time `json:"created_at"`
	PairedAt    time.Time `json:"paired_at"`
	LastSeen    time.Time `json:"last_seen"`

	Pending     bool   `json:"pending"`
	PendingCode string `json:"pending_code,omitempty"`
	// PendingID names the row of pending_enrollments, for the approve and reject
	// routes. It is zero for a screen that does not wait.
	PendingID int64 `json:"pending_id,omitempty"`
	// CollidesWith names a screen that is already paired and that this request
	// asks to be. The UI warns before the admin approves it.
	CollidesWith string `json:"collides_with,omitempty"`

	// NeedsConfirm is true while a machine that is not the stored one asks to be
	// this screen (D21). Then HardwareID is the machine that asks and
	// PrevHardwareID is the machine that the server knows. The server stores the
	// change at the confirmation and not before.
	NeedsConfirm       bool   `json:"needs_confirm"`
	PrevHardwareID     string `json:"prev_hardware_id,omitempty"`
	Conflict           bool   `json:"conflict"`
	ConflictHardwareID string `json:"conflict_hardware_id,omitempty"`

	SyncError string `json:"sync_error,omitempty"`
	// Status is the raw last heartbeat status. It stays a raw JSON string,
	// because the admin UI reads the same shape that the device serves at
	// /api/status and the server adds nothing to it.
	Status string `json:"status,omitempty"`

	// The per-device overrides.
	DefaultPlaylistID int64  `json:"default_playlist_id"`
	ScreenOn          string `json:"screen_on"`
	ScreenOff         string `json:"screen_off"`
	ScreenDays        string `json:"screen_days"`

	// State is computed, not stored. See State.
	State string `json:"state"`
}

// The states of a device. The admin UI colours a row by this word.
const (
	// StatePending waits for the admin to let it in.
	StatePending = "pending"
	// StateConflict means two hardware IDs used one token (D21).
	StateConflict = "conflict"
	// StateNeedsConfirm means that another machine asks to be this screen (D21).
	StateNeedsConfirm = "needs_confirm"
	// StateOnline means the device called inside 2.5 poll intervals.
	StateOnline = "online"
	// StateQuiet means it called today but not recently.
	StateQuiet = "quiet"
	// StateOffline means it did not call for a day, or it never called.
	StateOffline = "offline"
)

// Group is one row of the groups table.
type Group struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	DefaultPlaylistID int64     `json:"default_playlist_id"`
	ScreenOn          string    `json:"screen_on"`
	ScreenOff         string    `json:"screen_off"`
	ScreenDays        string    `json:"screen_days"`
	CreatedAt         time.Time `json:"created_at"`
	Devices           int       `json:"devices"`
}

// EnrollToken is one row of the enrollment_tokens table. The token value itself
// is not here: the admin sees it one time, at creation.
type EnrollToken struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	Mode      string    `json:"mode"`
	GroupID   int64     `json:"group_id"`
	ExpiresAt time.Time `json:"expires_at"`
	MaxUses   int       `json:"max_uses"`
	Uses      int       `json:"uses"`
	Revoked   bool      `json:"revoked"`
	CreatedAt time.Time `json:"created_at"`
}

// Media is one row of the media table.
type Media struct {
	SHA256     string    `json:"sha256"`
	OrigName   string    `json:"orig_name"`
	Size       int64     `json:"size"`
	MIME       string    `json:"mime"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	HasThumb   bool      `json:"has_thumb"`
	UploadedAt time.Time `json:"uploaded_at"`
	// Playlists names the playlists that use this object. The media page shows
	// it, and the delete route refuses an object that is in use.
	Playlists []string `json:"playlists,omitempty"`
}

// Playlist is one row of the playlists table with its items. The JSON shape is
// the shape of the shared playlist editor.
type Playlist struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Title      string         `json:"title"`
	Transition string         `json:"transition"`
	Shuffle    *bool          `json:"shuffle"`
	UpdatedAt  time.Time      `json:"updated_at"`
	Items      []PlaylistItem `json:"items"`
	// Devices counts the devices that get this playlist. The editor shows it.
	Devices int `json:"devices"`
}

// PlaylistItem is one row of the playlist_items table.
type PlaylistItem struct {
	SHA256         string `json:"sha256,omitempty"`
	URL            string `json:"url,omitempty"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Duration       int    `json:"duration,omitempty"`
	Mute           bool   `json:"mute,omitempty"`
	MaxDuration    int    `json:"max_duration,omitempty"`
	RefreshSeconds int    `json:"refresh_seconds,omitempty"`
	// Thumb is the URL of the thumbnail, or an empty value when there is none.
	Thumb string `json:"thumb,omitempty"`
	// Size is the length of the media object in bytes, or zero for a url item.
	// The manifest needs it, and one query gives it with the item.
	Size int64 `json:"-"`
	// MediaRow is true when the media table still holds this hash. The manifest
	// leaves out an item whose media row went away: a device cannot name, size or
	// fetch such an item.
	MediaRow bool `json:"-"`
	// MediaName is the original name of the media row. The manifest sends it, so
	// that the device can write a file name that a person reads.
	MediaName string `json:"-"`
}

// Assignment is one row of the assignments table.
type Assignment struct {
	ID           int64    `json:"id"`
	GroupID      int64    `json:"group_id,omitempty"`
	DeviceID     string   `json:"device_id,omitempty"`
	PlaylistID   int64    `json:"playlist_id"`
	PlaylistName string   `json:"playlist_name"`
	Days         []string `json:"days"`
	Start        string   `json:"start"`
	End          string   `json:"end"`
	Priority     int      `json:"priority"`
}

// Command is one row of the commands table.
type Command struct {
	ID          int64             `json:"id"`
	DeviceID    string            `json:"device_id"`
	Type        string            `json:"type"`
	Args        map[string]string `json:"args,omitempty"`
	QueuedAt    time.Time         `json:"queued_at"`
	DeliveredAt time.Time         `json:"delivered_at"`
	AckedAt     time.Time         `json:"acked_at"`
	// Deliveries counts how often the command went out.
	Deliveries int `json:"deliveries"`
	// State is computed: queued, delivered, acked or expired.
	State string `json:"state"`
}

// Release is one row of the releases table.
//
// There is no Mirrored flag. A device installs a release when MirrorState is
// MirrorDone, and one value cannot disagree with itself.
type Release struct {
	Version     string    `json:"version"`
	Approved    bool      `json:"approved"`
	Notes       string    `json:"notes"`
	PublishedAt time.Time `json:"published_at"`
	ApprovedAt  time.Time `json:"approved_at"`
	MirrorState string    `json:"mirror_state"`
	MirrorError string    `json:"mirror_error,omitempty"`
	// Mirrored is computed from MirrorState. The admin UI reads it, so the field
	// stays in the JSON; it is never a column.
	Mirrored bool `json:"mirrored"`
}

// The mirror states of a release.
const (
	MirrorIdle    = "idle"
	MirrorWorking = "working"
	MirrorDone    = "done"
	MirrorFailed  = "failed"
)
