package admin

import (
	"net/http"

	"github.com/ethanpil/portapixel/internal/server/db"
)

// getPlaylists lists the playlists with their items.
func (d Deps) getPlaylists(w http.ResponseWriter, r *http.Request) {
	list, err := d.DB.Playlists()
	if err != nil {
		fail(w, err)
		return
	}
	if list == nil {
		list = []db.Playlist{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlists": list})
}

// getPlaylist gives one playlist in the shape of the shared editor.
func (d Deps) getPlaylist(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r, "id")
	if !okID {
		return
	}
	p, err := d.DB.Playlist(id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// createPlaylist makes a playlist.
func (d Deps) createPlaylist(w http.ResponseWriter, r *http.Request) {
	d.savePlaylist(w, r, 0)
}

// updatePlaylist replaces a playlist and all its items.
func (d Deps) updatePlaylist(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r, "id")
	if !okID {
		return
	}
	d.savePlaylist(w, r, id)
}

// savePlaylist reads the editor body and writes it. db.SavePlaylist checks the
// items with the rules of internal/playlist, so the server cannot accept a
// playlist that a device would then refuse.
func (d Deps) savePlaylist(w http.ResponseWriter, r *http.Request, id int64) {
	var body struct {
		Name       string            `json:"name"`
		Title      string            `json:"title"`
		Transition string            `json:"transition"`
		Shuffle    *bool             `json:"shuffle"`
		Items      []db.PlaylistItem `json:"items"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	newID, err := d.DB.SavePlaylist(db.Playlist{
		ID: id, Name: body.Name, Title: body.Title,
		Transition: body.Transition, Shuffle: body.Shuffle, Items: body.Items,
	})
	if err != nil {
		fail(w, err)
		return
	}
	saved, err := d.DB.Playlist(newID)
	if err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("playlist-save", saved.Name)
	writeJSON(w, http.StatusOK, saved)
}

// deletePlaylist removes a playlist. A playlist that a rule or a default names
// stays: a screen must not change what it plays because a list went away.
func (d Deps) deletePlaylist(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r, "id")
	if !okID {
		return
	}
	if err := d.DB.DeletePlaylist(id); err != nil {
		if err == db.ErrInUse {
			writeError(w, http.StatusConflict,
				"a group, a screen or a time rule still uses this playlist")
			return
		}
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ok)
}

// getPlaylistDevices answers "which devices get this playlist". The editor asks
// before a save, because a save reaches every one of them.
func (d Deps) getPlaylistDevices(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r, "id")
	if !okID {
		return
	}
	if _, err := d.DB.Playlist(id); err != nil {
		fail(w, err)
		return
	}
	n, err := d.DB.PlaylistDeviceCount(id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": n})
}
