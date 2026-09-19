package server_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func TestMediaUploadAndDedupe(t *testing.T) {
	f := newFleet(t)
	f.login()

	body := imageBytes(t, 600, 400)
	want := sha256.Sum256(body)

	res := f.mustOK(f.upload("/api/admin/media", "welcome.png", body), "the first upload")
	var first struct {
		Media struct {
			SHA256   string `json:"sha256"`
			OrigName string `json:"orig_name"`
			Size     int64  `json:"size"`
			MIME     string `json:"mime"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			HasThumb bool   `json:"has_thumb"`
		} `json:"media"`
		Duplicate bool `json:"duplicate"`
	}
	res.json(t, &first)

	if first.Media.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("the hash is %s", first.Media.SHA256)
	}
	if first.Media.Size != int64(len(body)) || first.Media.Width != 600 || first.Media.Height != 400 {
		t.Fatalf("the row is %+v", first.Media)
	}
	if !strings.Contains(first.Media.MIME, "png") {
		t.Fatalf("the media type is %q", first.Media.MIME)
	}
	if !first.Media.HasThumb {
		t.Fatal("the image got no thumbnail")
	}
	if first.Duplicate {
		t.Fatal("the first upload says duplicate")
	}

	// The same bytes under another name are the same object.
	res = f.mustOK(f.upload("/api/admin/media", "another-name.png", body), "the second upload")
	var second struct {
		Media struct {
			SHA256   string `json:"sha256"`
			OrigName string `json:"orig_name"`
		} `json:"media"`
		Duplicate bool `json:"duplicate"`
	}
	res.json(t, &second)
	if second.Media.SHA256 != first.Media.SHA256 || !second.Duplicate {
		t.Fatalf("the second upload gave %+v", second)
	}
	if second.Media.OrigName != "welcome.png" {
		t.Fatalf("the object lost its first name: %q", second.Media.OrigName)
	}

	// The library holds one file.
	list := f.mustOK(f.adminCall(http.MethodGet, "/api/admin/media", nil), "the media list")
	var view struct {
		Media []struct {
			SHA256 string `json:"sha256"`
		} `json:"media"`
		Files int   `json:"files"`
		Bytes int64 `json:"bytes"`
	}
	list.json(t, &view)
	if view.Files != 1 || len(view.Media) != 1 || view.Bytes != int64(len(body)) {
		t.Fatalf("the list is %+v", view)
	}

	// The thumbnail serves as a JPEG.
	thumb := f.mustOK(f.adminCall(http.MethodGet,
		"/api/admin/media/"+first.Media.SHA256+"/thumb", nil), "the thumbnail")
	if kind := thumb.header.Get("Content-Type"); kind != "image/jpeg" {
		t.Fatalf("the thumbnail type is %q", kind)
	}
	if len(thumb.body) == 0 {
		t.Fatal("the thumbnail has no bytes")
	}
}

func TestMediaUploadNeedsTheFilenameHeader(t *testing.T) {
	f := newFleet(t)
	f.login()
	res := f.call(http.MethodPost, "/api/admin/media", nil, nil)
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("an upload with no name answered %d: %s", res.status, res.body)
	}
	if fields := res.fields(t); len(fields) != 1 || fields[0].Field != "name" {
		t.Fatalf("the fields are %+v", fields)
	}
}

func TestMediaThumbnailOfAFileThatIsNotAnImage(t *testing.T) {
	f := newFleet(t)
	f.login()
	sha := f.uploadMedia("clip.mp4", []byte("this is not a picture at all"))

	// A video, a WebP or an SVG has no thumbnail. The UI shows its own icon.
	res := f.adminCall(http.MethodGet, "/api/admin/media/"+sha+"/thumb", nil)
	if res.status != http.StatusNotFound {
		t.Fatalf("the thumbnail of a video answered %d: %s", res.status, res.body)
	}
	if res.errorText(t) == "" {
		t.Fatalf("the 404 holds no error text: %s", res.body)
	}
}

func TestMediaDeleteInUseGives409WithThePlaylists(t *testing.T) {
	f := newFleet(t)
	f.login()

	sha := f.uploadMedia("welcome.png", imageBytes(t, 30, 20))
	item := map[string]any{"sha256": sha, "name": "welcome.png", "duration": 10}
	f.makePlaylist("Lobby loop", item)
	f.makePlaylist("Evening loop", item)

	res := f.adminCall(http.MethodDelete, "/api/admin/media/"+sha, nil)
	if res.status != http.StatusConflict {
		t.Fatalf("the delete of a file in use answered %d: %s", res.status, res.body)
	}
	var out struct {
		Error     string   `json:"error"`
		Playlists []string `json:"playlists"`
	}
	res.json(t, &out)
	if out.Error == "" {
		t.Fatal("the 409 holds no error text")
	}
	if len(out.Playlists) != 2 {
		t.Fatalf("the answer names %v, want the two playlists", out.Playlists)
	}

	// The file is still there.
	if !f.store.Has(sha) {
		t.Fatal("the refused delete removed the file anyway")
	}

	// A file that no playlist holds goes away, and so does its thumbnail.
	free := f.uploadMedia("free.png", imageBytes(t, 20, 20))
	f.mustOK(f.adminCall(http.MethodDelete, "/api/admin/media/"+free, nil), "the delete")
	if f.store.Has(free) {
		t.Fatal("the delete left the file on the disk")
	}
}

func TestMediaHashInThePathIsChecked(t *testing.T) {
	f := newFleet(t)
	f.login()
	for _, bad := range []string{"not-a-hash", strings.Repeat("g", 64), strings.Repeat("A", 64)} {
		res := f.adminCall(http.MethodDelete, "/api/admin/media/"+bad, nil)
		if res.status != http.StatusBadRequest {
			t.Errorf("the delete of %q answered %d", bad, res.status)
		}
		res = f.adminCall(http.MethodGet, "/api/admin/media/"+bad+"/thumb", nil)
		if res.status != http.StatusBadRequest {
			t.Errorf("the thumbnail of %q answered %d", bad, res.status)
		}
	}
}

func TestPlaylistValidationOverTheAPI(t *testing.T) {
	f := newFleet(t)
	f.login()
	sha := f.uploadMedia("a.png", imageBytes(t, 10, 10))

	cases := []struct {
		name  string
		body  map[string]any
		field string
	}{
		{
			name:  "no items",
			body:  map[string]any{"title": "empty", "items": []any{}},
			field: "items",
		},
		{
			name: "a url with no scheme",
			body: map[string]any{"title": "url", "items": []any{
				map[string]any{"url": "dash.example.com", "duration": 30},
			}},
			field: "items[0].url",
		},
		{
			name: "mute on a url item",
			body: map[string]any{"title": "mute", "items": []any{
				map[string]any{"url": "https://dash.example.com", "duration": 30, "mute": true},
			}},
			field: "items[0].mute",
		},
		{
			name: "a file and a url",
			body: map[string]any{"title": "both", "items": []any{
				map[string]any{"sha256": sha, "url": "https://a.example", "name": "a.png"},
			}},
			field: "items[0]",
		},
		{
			name: "a hash that the library does not hold",
			body: map[string]any{"title": "missing", "items": []any{
				map[string]any{"sha256": strings.Repeat("a", 64), "name": "a.png"},
			}},
			field: "items[0].sha256",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := f.adminCall(http.MethodPost, "/api/admin/playlists", c.body)
			if res.status != http.StatusUnprocessableEntity {
				t.Fatalf("the save answered %d: %s", res.status, res.body)
			}
			for _, fe := range res.fields(t) {
				if fe.Field == c.field {
					return
				}
			}
			t.Fatalf("no field error names %q: %s", c.field, res.body)
		})
	}
}

func TestPlaylistSaveAndDeviceCount(t *testing.T) {
	f := newFleet(t)
	f.login()

	sha := f.uploadMedia("welcome.png", imageBytes(t, 30, 20))
	id := f.makePlaylist("Safety loop",
		map[string]any{"sha256": sha, "name": "welcome.png", "duration": 12},
		map[string]any{"url": "https://dash.example.com/board", "duration": 60, "refresh_seconds": 300},
	)

	res := f.mustOK(f.adminCall(http.MethodGet,
		"/api/admin/playlists/"+itoa(id), nil), "the playlist")
	var p struct {
		Name       string `json:"name"`
		Title      string `json:"title"`
		Transition string `json:"transition"`
		Items      []struct {
			SHA256         string `json:"sha256"`
			URL            string `json:"url"`
			Kind           string `json:"kind"`
			Duration       int    `json:"duration"`
			RefreshSeconds int    `json:"refresh_seconds"`
			Thumb          string `json:"thumb"`
		} `json:"items"`
		Devices int `json:"devices"`
	}
	res.json(t, &p)

	if p.Name != "safety-loop" || p.Title != "Safety loop" || p.Transition != "crossfade" {
		t.Fatalf("the playlist is %+v", p)
	}
	if len(p.Items) != 2 {
		t.Fatalf("the playlist holds %d items", len(p.Items))
	}
	if p.Items[0].Kind != "image" || p.Items[0].Thumb == "" {
		t.Fatalf("the first item is %+v", p.Items[0])
	}
	if p.Items[1].Kind != "url" || p.Items[1].RefreshSeconds != 300 {
		t.Fatalf("the second item is %+v", p.Items[1])
	}

	// Nobody plays it yet.
	count := f.mustOK(f.adminCall(http.MethodGet,
		"/api/admin/playlists/"+itoa(id)+"/devices", nil), "the device count")
	var out struct {
		Devices int `json:"devices"`
	}
	count.json(t, &out)
	if out.Devices != 0 {
		t.Fatalf("the count is %d, want 0", out.Devices)
	}

	// A group that uses it, with two screens in it.
	group := f.makeGroup("Warehouse")
	for _, id := range []string{"px-wh000001", "px-wh000002"} {
		f.pairDevice(id)
		f.mustOK(f.adminCall(http.MethodPost, "/api/admin/devices/"+id+"/group",
			map[string]int64{"group_id": group}), "move the device")
	}
	f.mustOK(f.adminCall(http.MethodPut, "/api/admin/groups/"+itoa(group), map[string]any{
		"name": "Warehouse", "default_playlist_id": id,
	}), "set the group default")

	count = f.mustOK(f.adminCall(http.MethodGet,
		"/api/admin/playlists/"+itoa(id)+"/devices", nil), "the device count")
	count.json(t, &out)
	if out.Devices != 2 {
		t.Fatalf("the count is %d, want 2", out.Devices)
	}

	// A playlist that a group default names cannot go away.
	res = f.adminCall(http.MethodDelete, "/api/admin/playlists/"+itoa(id), nil)
	if res.status != http.StatusConflict {
		t.Fatalf("the delete of a playlist in use answered %d: %s", res.status, res.body)
	}
}

func TestPlaylistNameClash(t *testing.T) {
	f := newFleet(t)
	f.login()
	sha := f.uploadMedia("a.png", imageBytes(t, 10, 10))
	item := map[string]any{"sha256": sha, "name": "a.png", "duration": 5}

	f.makePlaylist("Lobby loop", item)
	res := f.adminCall(http.MethodPost, "/api/admin/playlists", map[string]any{
		"title": "LOBBY  LOOP", "items": []any{item},
	})
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("the clash answered %d: %s", res.status, res.body)
	}
	if fields := res.fields(t); len(fields) != 1 || fields[0].Field != "name" {
		t.Fatalf("the fields are %+v", fields)
	}
}

// itoa gives the decimal form of a row number, for a URL path.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
