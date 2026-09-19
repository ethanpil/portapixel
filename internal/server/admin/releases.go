package admin

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/releases"
)

// ReleaseListView is the answer of GET /api/admin/releases.
type ReleaseListView struct {
	Releases []db.Release `json:"releases"`
	// Approved is the version that the devices install, or an empty value.
	Approved string `json:"approved"`
	// Fleet says how many devices run each version.
	Fleet []VersionCount `json:"fleet"`
	// ListError is what the GitHub release list said when it failed. The page
	// still shows the releases that the server knows.
	ListError string `json:"list_error,omitempty"`
	// HasKey is false in a build with no minisign public key. Then the server
	// cannot verify a release and it never marks one as mirrored.
	HasKey bool `json:"has_key"`
	// Mirroring is the version that the mirror works on now.
	Mirroring string `json:"mirroring,omitempty"`
}

// VersionCount is one bar of the "where the fleet is" card.
type VersionCount struct {
	Version string `json:"version"`
	Devices int    `json:"devices"`
}

// getReleases lists the releases. It asks GitHub through the cache of the lister,
// writes what it learned into the table, and then reads the table: the approval
// and the mirror state live here and not at GitHub.
func (d Deps) getReleases(w http.ResponseWriter, r *http.Request) {
	list, listErr := d.Mirror.Lister.List(r.Context(), false)
	for _, rel := range list {
		if releases.ValidVersion(rel.Version) {
			d.DB.NoteRelease(rel.Version, rel.Notes, rel.PublishedAt)
		}
	}
	d.writeReleases(w, r, listErr)
}

// refreshReleases asks GitHub again and skips the cache.
func (d Deps) refreshReleases(w http.ResponseWriter, r *http.Request) {
	list, listErr := d.Mirror.Lister.List(r.Context(), true)
	for _, rel := range list {
		if releases.ValidVersion(rel.Version) {
			d.DB.NoteRelease(rel.Version, rel.Notes, rel.PublishedAt)
		}
	}
	d.writeReleases(w, r, listErr)
}

func (d Deps) writeReleases(w http.ResponseWriter, r *http.Request, listErr string) {
	rows, err := d.DB.Releases()
	if err != nil {
		fail(w, err)
		return
	}
	view := ReleaseListView{
		Releases:  rows,
		ListError: listErr,
		HasKey:    d.Mirror.PublicKey != "",
		Mirroring: d.Mirror.Working(),
		Fleet:     []VersionCount{},
	}
	for _, rel := range rows {
		if rel.Approved {
			view.Approved = rel.Version
		}
	}
	stats, err := d.DB.Stats(time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	for v, n := range stats.Versions {
		view.Fleet = append(view.Fleet, VersionCount{Version: v, Devices: n})
	}
	sort.Slice(view.Fleet, func(i, j int) bool {
		if view.Fleet[i].Devices != view.Fleet[j].Devices {
			return view.Fleet[i].Devices > view.Fleet[j].Devices
		}
		return view.Fleet[i].Version > view.Fleet[j].Version
	})
	writeJSON(w, http.StatusOK, view)
}

// approveRelease makes one version the approved version and starts the mirror.
// A device only ever installs the approved version, and only from the mirror
// (D26, D28).
func (d Deps) approveRelease(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	if !releases.ValidVersion(version) {
		writeError(w, http.StatusBadRequest, "that is not a version name")
		return
	}
	if err := d.DB.ApproveRelease(version); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("release-approve", version)

	// A release that the mirror already holds and verified needs nothing more. A
	// second download would only risk a good mirror, and on a closed network,
	// where the files came from an uploaded bundle, it would fail and clear the
	// mirrored flag.
	if rel, err := d.DB.Release(version); err == nil && rel.Mirrored {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mirror_error": ""})
		return
	}

	// The mirror runs in the background: it downloads two binaries.
	mirrorError := ""
	if err := d.Mirror.Start(version); err != nil {
		mirrorError = err.Error()
		d.DB.SetMirrorState(version, db.MirrorFailed, mirrorError)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mirror_error": mirrorError})
}

// unapproveRelease takes the approval away. The devices then stay where they are.
func (d Deps) unapproveRelease(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	if !releases.ValidVersion(version) {
		writeError(w, http.StatusBadRequest, "that is not a version name")
		return
	}
	if err := d.DB.UnapproveRelease(version); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("release-unapprove", version)
	writeJSON(w, http.StatusOK, ok)
}

// mirrorRelease starts the mirror again. The admin uses it after a failure.
func (d Deps) mirrorRelease(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	if !releases.ValidVersion(version) {
		writeError(w, http.StatusBadRequest, "that is not a version name")
		return
	}
	if _, err := d.DB.Release(version); err != nil {
		fail(w, err)
		return
	}
	if err := d.Mirror.Start(version); err != nil {
		if errors.Is(err, releases.ErrBusy) {
			writeError(w, http.StatusConflict, "a mirror already runs for "+d.Mirror.Working())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ok)
}

// uploadBundle takes a release bundle for a network that cannot reach GitHub.
// The body is the archive and X-Filename gives its name, so the route knows if it
// is a .tar.gz or a .zip.
func (d Deps) uploadBundle(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	version := r.PathValue("version")
	if !releases.ValidVersion(version) {
		writeError(w, http.StatusBadRequest, "that is not a version name")
		return
	}
	name := uploadName(r)
	if name == "" {
		writeFields(w, "the request has a field that this server cannot use",
			db.Errors{{Field: "name", Message: "the upload needs the " + FilenameHeader + " header"}})
		return
	}

	if err := d.Mirror.InstallBundle(version, r.Body, name); err != nil {
		d.Log.Log("release-bundle-failed", version+": "+err.Error())
		switch {
		case errors.Is(err, releases.ErrNoKey):
			writeError(w, http.StatusPreconditionFailed, err.Error())
		case errors.Is(err, releases.ErrBadBundle):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		default:
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return
	}
	d.Log.Log("release-bundle", version+" came from a bundle and it verifies")
	writeJSON(w, http.StatusOK, ok)
}
