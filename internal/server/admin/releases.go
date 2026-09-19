package admin

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
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
	d.listReleases(w, r, false)
}

// refreshReleases asks GitHub again and skips the cache.
func (d Deps) refreshReleases(w http.ResponseWriter, r *http.Request) {
	d.listReleases(w, r, true)
}

// listReleases is the body of the two routes above.
//
// The context is the life of the server and not the context of this request. An
// admin who leaves the page while the fetch runs would otherwise write "context
// canceled" into the error text that every later page reads.
//
// The table takes a write only when the lister really reached GitHub. A write for
// each of thirty releases on every page view would fight every heartbeat of the
// fleet for the one write connection.
func (d Deps) listReleases(w http.ResponseWriter, r *http.Request, force bool) {
	list, listErr := d.Mirror.Lister.List(d.Background(), force)
	if d.Mirror.Lister.Fresh() {
		notes := make([]db.ReleaseNote, 0, len(list))
		for _, rel := range list {
			if releases.ValidVersion(rel.Version) {
				notes = append(notes, db.ReleaseNote{
					Version: rel.Version, Notes: rel.Notes, PublishedAt: rel.PublishedAt,
				})
			}
		}
		if err := d.DB.NoteReleases(notes); err != nil {
			fail(w, err)
			return
		}
	}

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
	httpjson.Write(w, http.StatusOK, view)
}

// approveRelease makes one version the approved version and starts the mirror.
// A device only ever installs the approved version, and only from the mirror
// (D26, D28).
func (d Deps) approveRelease(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	if !releases.ValidVersion(version) {
		httpjson.Error(w, http.StatusBadRequest, "that is not a version name")
		return
	}
	if err := d.DB.ApproveRelease(version); err != nil {
		fail(w, err)
		return
	}
	d.FleetChanged()
	d.Log.Log("release-approve", version)

	// A release that the mirror already holds and verified needs nothing more. A
	// second download would only risk a good mirror, and on a closed network,
	// where the files came from an uploaded bundle, it would fail and clear the
	// mirror state.
	if rel, err := d.DB.Release(version); err == nil && rel.MirrorState == db.MirrorDone {
		httpjson.Write(w, http.StatusOK, map[string]any{"ok": true, "mirror_error": ""})
		return
	}

	// The mirror runs in the background: it downloads two binaries.
	mirrorError := ""
	if err := d.Mirror.Start(d.Background(), version); err != nil {
		mirrorError = err.Error()
		d.DB.SetMirrorState(version, db.MirrorFailed, mirrorError)
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"ok": true, "mirror_error": mirrorError})
}

// unapproveRelease takes the approval away. The devices then stay where they are.
func (d Deps) unapproveRelease(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	if !releases.ValidVersion(version) {
		httpjson.Error(w, http.StatusBadRequest, "that is not a version name")
		return
	}
	if err := d.DB.UnapproveRelease(version); err != nil {
		fail(w, err)
		return
	}
	d.FleetChanged()
	d.Log.Log("release-unapprove", version)
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// mirrorRelease starts the mirror again. The admin uses it after a failure.
func (d Deps) mirrorRelease(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	if !releases.ValidVersion(version) {
		httpjson.Error(w, http.StatusBadRequest, "that is not a version name")
		return
	}
	if _, err := d.DB.Release(version); err != nil {
		fail(w, err)
		return
	}
	if err := d.Mirror.Start(d.Background(), version); err != nil {
		if errors.Is(err, releases.ErrBusy) {
			httpjson.Error(w, http.StatusConflict, "a mirror already runs for "+d.Mirror.Working())
			return
		}
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// uploadBundle takes a release bundle for a network that cannot reach GitHub.
// The body is the archive and X-Filename gives its name, so the route knows if it
// is a .tar.gz or a .zip.
func (d Deps) uploadBundle(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	version := r.PathValue("version")
	if !releases.ValidVersion(version) {
		httpjson.Error(w, http.StatusBadRequest, "that is not a version name")
		return
	}
	name := uploadName(r)
	if name == "" {
		httpjson.Fields(w, "the request has a field that this server cannot use",
			db.Errors{{Field: "name", Message: "the upload needs the " + FilenameHeader + " header"}})
		return
	}

	if err := d.Mirror.InstallBundle(version, httpjson.StreamBody(w, r), name); err != nil {
		d.Log.Log("release-bundle-failed", version+": "+err.Error())
		switch {
		case errors.Is(err, releases.ErrNoKey):
			httpjson.Error(w, http.StatusPreconditionFailed, err.Error())
		case errors.Is(err, releases.ErrBusy):
			httpjson.Error(w, http.StatusConflict, "a mirror already runs for "+d.Mirror.Working())
		case errors.Is(err, releases.ErrBadBundle):
			httpjson.Error(w, http.StatusUnprocessableEntity, err.Error())
		default:
			// A full disk, a read-only directory or a damaged signature file are
			// faults of the server and not of the upload. An answer of 422 would
			// tell the admin to look at the file that is in fact good.
			fail(w, err)
		}
		return
	}
	d.Log.Log("release-bundle", version+" came from a bundle and it verifies")
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}
