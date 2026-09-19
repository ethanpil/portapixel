package admin

import (
	"net/http"
	"os"
	"time"

	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
	"github.com/ethanpil/portapixel/internal/server/media"
	"github.com/ethanpil/portapixel/internal/version"
)

// HealthView is the answer of GET /api/admin/health. It is the Server Health page
// of D27: disk, database, media store, mirror state and device contacts.
type HealthView struct {
	ServerVersion string `json:"server_version"`
	Arch          string `json:"arch"`
	UptimeSeconds int64  `json:"uptime_seconds"`

	DataDir        string `json:"data_dir"`
	DiskFreeBytes  uint64 `json:"disk_free_bytes"`
	DiskTotalBytes uint64 `json:"disk_total_bytes"`
	// ReserveBytes is the free space that an upload may not take.
	ReserveBytes int64 `json:"reserve_bytes"`

	DatabaseBytes int64 `json:"database_bytes"`
	// Integrity is the answer of the last integrity check, or an empty value when
	// nobody ran one yet. The check reads the whole file, so it runs on demand.
	Integrity   string    `json:"integrity"`
	IntegrityAt time.Time `json:"integrity_at"`

	// MediaWritable is the answer of a real write into the media store.
	MediaWritable bool   `json:"media_writable"`
	MediaError    string `json:"media_error,omitempty"`
	MediaFiles    int    `json:"media_files"`
	MediaBytes    int64  `json:"media_bytes"`

	// Mirror is the state of the release mirror.
	ApprovedVersion string `json:"approved_version"`
	MirrorState     string `json:"mirror_state"`
	MirrorError     string `json:"mirror_error,omitempty"`
	HasReleaseKey   bool   `json:"has_release_key"`

	Contacts db.ContactStats `json:"contacts"`
}

// The keys that hold the last integrity check. The check reads the whole
// database file and takes seconds, so the page shows the stored answer and a
// button runs a new one.
//
// The answer lives in the settings table and not in a value of this package. A
// package value would be state that two servers in one process share, and it
// would go away at every restart for no reason.
const (
	settingIntegrity   = "integrity_result"
	settingIntegrityAt = "integrity_at"
)

// getHealth builds the health page.
func (d Deps) getHealth(w http.ResponseWriter, r *http.Request) {
	view := HealthView{
		ServerVersion: version.Version,
		Arch:          version.Arch(),
		UptimeSeconds: int64(time.Since(d.StartedAt).Seconds()),
		DataDir:       d.DataDir,
		ReserveBytes:  media.Reserve,
		HasReleaseKey: d.Mirror.PublicKey != "",
	}
	view.DiskFreeBytes, _ = fsutil.FreeBytes(d.DataDir)
	view.DiskTotalBytes, _ = fsutil.TotalBytes(d.DataDir)
	view.DatabaseBytes = databaseBytes(d.DB.Path())

	view.Integrity = d.DB.Setting(settingIntegrity, "")
	view.IntegrityAt = parseStamp(d.DB.Setting(settingIntegrityAt, ""))

	if err := d.probeMedia(); err != nil {
		view.MediaError = err.Error()
	} else {
		view.MediaWritable = true
	}
	files, bytes, err := d.DB.MediaTotals()
	if err != nil {
		fail(w, err)
		return
	}
	view.MediaFiles, view.MediaBytes = files, bytes

	if rel, err := d.DB.ApprovedRelease(); err == nil {
		view.ApprovedVersion = rel.Version
		view.MirrorState = rel.MirrorState
		view.MirrorError = rel.MirrorError
	} else {
		view.MirrorState = db.MirrorIdle
	}
	// The pending list is not a device row, so Stats counts it on its own.
	if working := d.Mirror.Working(); working != "" {
		view.MirrorState = db.MirrorWorking
	}

	if view.Contacts, err = d.DB.Stats(time.Now()); err != nil {
		fail(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, view)
}

// postIntegrity runs PRAGMA integrity_check and keeps the answer.
func (d Deps) postIntegrity(w http.ResponseWriter, r *http.Request) {
	result, err := d.DB.IntegrityCheck()
	if err != nil {
		result = "the check failed: " + err.Error()
	}
	now := time.Now().UTC()
	d.DB.SetSetting(settingIntegrity, result)
	d.DB.SetSetting(settingIntegrityAt, now.Format(time.RFC3339))

	d.Log.Log("integrity-check", result)
	httpjson.Write(w, http.StatusOK, map[string]any{"integrity": result, "integrity_at": now})
}

// parseStamp reads a time that a setting holds. A missing or bad value gives the
// zero time, which the UI reads as "nobody ran it yet".
func parseStamp(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// probeMedia writes a file into the media store and removes it. A store that a
// person mounted read-only, or a full disk, is the fault that this page exists to
// find, and only a real write finds it.
func (d Deps) probeMedia() error {
	f, err := os.CreateTemp(d.Media.Root(), "probe*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)

	if _, err := f.Write([]byte("probe")); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// databaseBytes gives the size of the database, its write-ahead log included. The
// log can be larger than the database file after a busy hour, so a number that
// left it out would surprise somebody who looks at the directory.
func databaseBytes(path string) int64 {
	var total int64
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if info, err := os.Stat(name); err == nil {
			total += info.Size()
		}
	}
	return total
}
