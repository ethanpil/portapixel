package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
	"github.com/ethanpil/portapixel/internal/server/releases"
	"github.com/ethanpil/portapixel/internal/store"
)

// getMedia answers GET /api/v1/media/{sha256}.
//
// http.ServeContent gives the Range answers, so a device that lost its connection
// halfway through a 1 GB video continues from the byte that it has (D24). The ETag
// is the hash: the content of an object can never change, so a device that already
// holds the object gets 304 with no body.
func (d Deps) getMedia(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.device(w, r); !ok {
		return
	}
	sha := r.PathValue("sha256")
	// The hash becomes a file path, so the shape is checked before anything opens
	// a file. A value such as "../../server.toml" stops here. The store checks it
	// again, because the check belongs to the thing that builds the path.
	if !store.IsSHA256(sha) {
		httpjson.Error(w, http.StatusBadRequest,
			"that is not a SHA-256 value of 64 lower case hex characters")
		return
	}
	// The download needs the row for two values: that the object is in the library,
	// and the media type that the upload decided. MediaRow is one query with no
	// join; d.DB.Media would also name every playlist that holds the object, which
	// no device reads.
	row, err := d.DB.MediaRow(sha)
	if errors.Is(err, db.ErrNotFound) {
		httpjson.Error(w, http.StatusNotFound, "the media library holds no object with this hash")
		return
	}
	if err != nil {
		d.Log.Log("media-error", sha[:8]+": "+err.Error())
		httpjson.Error(w, http.StatusInternalServerError, "the server could not read the media library")
		return
	}

	path, err := d.Media.Path(sha)
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		httpjson.Error(w, http.StatusNotFound, "the media store holds no file for this object")
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		httpjson.Error(w, http.StatusNotFound, "the media store holds no file for this object")
		return
	}
	w.Header().Set("ETag", `"`+sha+`"`)
	// The bytes of an object never change, so the device may keep it for a year.
	//
	// It is "private" and not "public". A shared cache with "public" would keep the
	// object and then serve it to a caller that holds no device token, and the
	// content of a screen is not public by default. A device holds its own copy in
	// its object store, so a proxy cache would save it nothing anyway.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	// The type comes from the row and not from the first bytes of the file. The name
	// in the URL is a hash with no extension, so http.ServeContent would guess, and a
	// file that a person uploaded as a picture but that holds HTML would then be
	// served as HTML. internal/server/media decided the type at the upload.
	w.Header().Set("Content-Type", row.MIME)
	SetDisposition(w, row.MIME, row.OrigName)
	http.ServeContent(w, r, sha, info.ModTime(), f)
}

// SetDisposition tells the browser to save a stored object instead of showing it,
// unless the object is a picture or a video. The object route of the admin API
// uses it too, so that the two routes have one rule.
//
// A picture and a video are what the admin UI draws, and the headers of
// internal/server.secureHeaders already make an object inert. Everything else -
// an SVG, an HTML file that somebody named .png, a release binary - is a download
// and nothing else.
func SetDisposition(w http.ResponseWriter, mime, name string) {
	if strings.HasPrefix(mime, "image/") && mime != "image/svg+xml" {
		return
	}
	if strings.HasPrefix(mime, "video/") {
		return
	}
	// The name goes in the quoted form with the quotes and the backslashes taken
	// out, so that a name cannot add a second header field.
	safe := strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if safe == "" {
		safe = "download"
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+safe+`"`)
}

// getRelease answers GET /api/v1/releases/{version}/{file}.
//
// It serves the files of a mirror that is complete and verified, and nothing else.
// The name of the file must be one of the names of a release: a path from a URL
// must never choose which file of the disk goes out.
func (d Deps) getRelease(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.device(w, r); !ok {
		return
	}
	version := r.PathValue("version")
	name := r.PathValue("file")
	if !releases.ValidVersion(version) {
		httpjson.Error(w, http.StatusBadRequest, "that is not a version name")
		return
	}
	if !releases.IsMirrorFile(name) {
		httpjson.Error(w, http.StatusNotFound, "a release holds no file with this name")
		return
	}

	rel, err := d.DB.Release(version)
	if err != nil || rel.MirrorState != db.MirrorDone {
		// A release that the mirror did not finish is not there as far as a device
		// is concerned. A half-mirrored release must never reach a card.
		httpjson.Error(w, http.StatusNotFound, "this server does not mirror this version")
		return
	}

	path := filepath.Join(d.Mirror.VersionDir(version), name)
	f, err := os.Open(path)
	if err != nil {
		httpjson.Error(w, http.StatusNotFound, "the mirror holds no file with this name")
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		httpjson.Error(w, http.StatusNotFound, "the mirror holds no file with this name")
		return
	}
	// A release file is a binary and a signature. Neither is content that a browser
	// should show, so both go out as a download of unknown bytes.
	w.Header().Set("Content-Type", "application/octet-stream")
	SetDisposition(w, "application/octet-stream", name)
	http.ServeContent(w, r, name, info.ModTime(), f)
}
