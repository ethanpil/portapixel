package admin

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
	"github.com/ethanpil/portapixel/internal/server/media"
	"github.com/ethanpil/portapixel/internal/store"
)

// FilenameHeader carries the name of an upload. The body is the file itself, so
// there is no multipart parsing on either end. web/shared/api.js sends it.
const FilenameHeader = "X-Filename"

// MediaListView is the answer of GET /api/admin/media.
type MediaListView struct {
	Media []db.Media `json:"media"`
	Files int        `json:"files"`
	Bytes int64      `json:"bytes"`
	// FreeBytes is the free space where the media live. The store keeps
	// ReserveBytes of it back, so the two numbers give the limit of an upload.
	FreeBytes    uint64 `json:"free_bytes"`
	ReserveBytes int64  `json:"reserve_bytes"`
}

// getMediaList lists the library.
func (d Deps) getMediaList(w http.ResponseWriter, r *http.Request) {
	list, err := d.DB.MediaList()
	if err != nil {
		fail(w, err)
		return
	}
	files, bytes, err := d.DB.MediaTotals()
	if err != nil {
		fail(w, err)
		return
	}
	free, _ := fsutil.FreeBytes(d.Media.Root())
	httpjson.Write(w, http.StatusOK, MediaListView{
		Media: list, Files: files, Bytes: bytes,
		FreeBytes: free, ReserveBytes: media.Reserve,
	})
}

// postMedia takes one upload. The body is the file and X-Filename is its name,
// the same as the media route of the device (plan section 8).
//
// The store streams the body to the disk and names the object by its hash, so an
// upload of any size costs the same memory and a second upload of one file is one
// object (D27).
//
// The body goes through the idle guard of httpjson. There is no read timeout on
// the server, because one would cut a 1 GB upload; the guard instead ends a
// connection that sends nothing for a minute.
func (d Deps) postMedia(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	name := uploadName(r)
	if name == "" {
		httpjson.Fields(w, "the request has a field that this server cannot use",
			db.Errors{{Field: "name", Message: "the upload needs the " + FilenameHeader + " header"}})
		return
	}

	res, err := d.Media.Put(httpjson.StreamBody(w, r), name, r.ContentLength)
	switch {
	case errors.Is(err, media.ErrNoSpace):
		httpjson.Error(w, http.StatusInsufficientStorage, err.Error())
		return
	case err != nil:
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	row := db.Media{
		SHA256: res.SHA256, OrigName: name, Size: res.Size, MIME: res.MIME,
		Width: res.Width, Height: res.Height, HasThumb: res.HasThumb,
	}
	if err := d.DB.AddMedia(row); err != nil {
		// The blob is in the store and the row is not. Take the blob away again,
		// unless the store held it before this upload: then it belongs to another
		// row. A blob that stays behind is invisible to every page and counts
		// against nothing, and the sweep of the store is the second net under it.
		if !res.Duplicate {
			if delErr := d.Media.Delete(res.SHA256); delErr != nil {
				d.Log.Log("media-orphan", res.SHA256[:8]+
					" is on the disk with no row; the sweep will take it")
			}
		}
		fail(w, err)
		return
	}
	saved, err := d.DB.Media(res.SHA256)
	if err != nil {
		fail(w, err)
		return
	}
	if res.Duplicate {
		d.Log.Log("media-upload", name+" is already in the library as "+saved.OrigName)
	} else {
		d.Log.Log("media-upload", name)
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"media": saved, "duplicate": res.Duplicate})
}

// uploadName reads the file name of an upload. web/shared/api.js sends it
// percent-encoded, because a header may hold no character above 7-bit ASCII.
func uploadName(r *http.Request) string {
	raw := r.Header.Get(FilenameHeader)
	if raw == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(raw); err == nil {
		raw = decoded
	}
	// Take the last element of a path of either kind: a browser sends the plain
	// name, but a curl call may send a whole path.
	raw = raw[strings.LastIndexAny(raw, `/\`)+1:]
	return strings.TrimSpace(raw)
}

// deleteMedia removes an object. An object that a playlist holds stays, and the
// answer names the playlists so the UI can say which ones (plan section 12).
//
// The row goes first and the file after it. The other order would leave a row that
// names a file that is gone, which a device would try to download for ever. A file
// that stays behind is an orphan that the sweep of the store takes; on Windows a
// Range download that is in flight makes the delete fail, so that happens on a
// normal day and not only after a crash.
func (d Deps) deleteMedia(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha256")
	if !store.IsSHA256(sha) {
		httpjson.Error(w, http.StatusBadRequest,
			"that is not a SHA-256 value of 64 lower case hex characters")
		return
	}
	users, err := d.DB.DeleteMedia(sha)
	if errors.Is(err, db.ErrInUse) {
		httpjson.Write(w, http.StatusConflict, map[string]any{
			"error":     "this file is still in a playlist",
			"playlists": users,
		})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	if err := d.Media.Delete(sha); err != nil {
		d.Log.Log("media-orphan", sha[:8]+" is still on the disk; the sweep will take it: "+err.Error())
	}
	d.Log.Log("media-delete", sha[:8])
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// getThumb serves the thumbnail of an object.
//
// A file that we cannot decode has no thumbnail: video, WebP, AVIF and SVG all
// answer 404 here and the UI shows the icon of its own kind (D27).
func (d Deps) getThumb(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha256")
	if !store.IsSHA256(sha) {
		httpjson.Error(w, http.StatusBadRequest,
			"that is not a SHA-256 value of 64 lower case hex characters")
		return
	}
	path, err := d.Media.ThumbPath(sha)
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		httpjson.Error(w, http.StatusNotFound, "this file has no thumbnail")
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		httpjson.Error(w, http.StatusNotFound, "this file has no thumbnail")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", `"`+sha+`"`)
	// A thumbnail is named by the hash of its source, so it never changes.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, sha+".jpg", info.ModTime(), f)
}
