package httpd

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ethanpil/portapixel/internal/device/installer"
	"github.com/ethanpil/portapixel/internal/updater"
	"github.com/ethanpil/portapixel/web"
)

// POST /api/update/check asks the release source for a newer release.
//
// The answer carries the release that runs, so the About page can say "1.4.0 is the
// newest release" with no second call. A device that has nothing newer answers with
// an empty "available" and not with an error: nothing is wrong.
func (d Deps) postUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if d.CheckUpdate == nil {
		writeError(w, http.StatusNotImplemented, NotImplemented)
		return
	}
	rel, err := d.CheckUpdate(r.Context())
	switch {
	case errors.Is(err, updater.ErrNoRelease):
		writeJSON(w, http.StatusOK, map[string]any{"current": d.Status(true, true).Version})
	case errors.Is(err, updater.ErrNoKey):
		// A development build. It is not a fault of the request, so the answer is a
		// sentence and not a 500.
		writeJSON(w, http.StatusOK, map[string]any{
			"current": d.Status(true, true).Version,
			"blocked": err.Error(),
		})
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"current":   d.Status(true, true).Version,
			"available": rel.Version,
			"source":    rel.Source,
			"notes":     rel.Notes,
		})
	}
}

// POST /api/update/apply installs the release that the last check found.
//
// It answers at once and the work goes on in the background: the download and the
// check take minutes, and the browser of the person must not wait for them. The
// state is in /api/status, which the About page already polls.
func (d Deps) postUpdateApply(w http.ResponseWriter, r *http.Request) {
	if d.ApplyUpdate == nil {
		writeError(w, http.StatusNotImplemented, NotImplemented)
		return
	}
	if err := d.ApplyUpdate(r.Context()); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, updater.ErrBusy) {
			code = http.StatusServiceUnavailable
		}
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ok)
}

// GET /api/disks gives the disks that an install can write to (D54).
func (d Deps) getDisks(w http.ResponseWriter, r *http.Request) {
	if d.Disks == nil {
		writeError(w, http.StatusNotImplemented, NotImplemented)
		return
	}
	disks, err := d.Disks()
	if err != nil {
		// A machine with no PortaPixel layout cannot install onto a disk. That is a
		// state and not a fault, so the list is empty and the reason is in words.
		writeJSON(w, http.StatusOK, map[string]any{
			"disks": []installer.Disk{}, "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disks": emptyIfNil(disks)})
}

// POST /api/install-to-disk {"device": "/dev/sda", "confirm": "/dev/sda"}
//
// confirm must be the device path, character for character. The admin UI asks the
// person to type it, and the device checks it again: a request from a script must
// pass the same test as a person.
func (d Deps) postInstallToDisk(w http.ResponseWriter, r *http.Request) {
	if d.StartInstall == nil {
		writeError(w, http.StatusNotImplemented, NotImplemented)
		return
	}
	var body struct {
		Device  string `json:"device"`
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := d.StartInstall(body.Device, body.Confirm); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, installer.ErrBusy) {
			code = http.StatusServiceUnavailable
		}
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ok)
}

// GET /licenses gives the licence list (D33).
//
// The copy under /usr/share/portapixel wins. It is the copy of the image, so it
// matches the packages that this image holds. The copy in the binary is the fallback
// for a development machine and for an install where the file is missing.
func (d Deps) getLicenses(w http.ResponseWriter, r *http.Request) {
	var data []byte
	if d.ShareRoot != "" {
		if body, err := os.ReadFile(filepath.Join(d.ShareRoot, web.LicensesName)); err == nil {
			data = body
		}
	}
	if data == nil {
		body, err := web.Licenses()
		if err != nil {
			writeError(w, http.StatusNotFound, "this build carries no licence list")
			return
		}
		data = body
	}
	// text/plain and not text/markdown: a browser shows the first and downloads the
	// second, and a person who clicks a link wants to read it.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}
