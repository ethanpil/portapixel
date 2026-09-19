package admin

import (
	"net"
	"net/http"
	"strings"

	"github.com/ethanpil/portapixel/internal/version"
)

// SessionView is the answer of GET /api/admin/session. The UI asks for it on
// every page load, so it carries the few values that every page shows.
type SessionView struct {
	LoggedIn     bool   `json:"logged_in"`
	ServerName   string `json:"server_name"`
	Version      string `json:"version"`
	PublicURL    string `json:"public_url"`
	WeakPassword bool   `json:"weak_password"`
}

// postLogin checks the password and makes a session.
func (d Deps) postLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !d.Limiter.Allow(ip) {
		writeError(w, http.StatusTooManyRequests, "too many attempts from this address; wait a minute")
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		d.Limiter.Fail(ip)
		return
	}
	if !d.CheckPassword(body.Password) {
		d.Limiter.Fail(ip)
		d.Log.Log("login-failed", "from "+ip)
		writeError(w, http.StatusUnauthorized, "that password is not right")
		return
	}
	d.Limiter.Reset(ip)
	d.Sessions.Login(w)
	d.Log.Log("login", "from "+ip)
	writeJSON(w, http.StatusOK, d.session(true))
}

// postLogout ends the session.
func (d Deps) postLogout(w http.ResponseWriter, r *http.Request) {
	d.Sessions.Logout(w, r)
	writeJSON(w, http.StatusOK, ok)
}

// getSession says if the browser holds a session. It needs no session itself:
// the answer to "am I logged in" must work when the answer is no.
func (d Deps) getSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.session(d.Sessions.Valid(r)))
}

func (d Deps) session(loggedIn bool) SessionView {
	s := d.Settings()
	return SessionView{
		LoggedIn:     loggedIn,
		ServerName:   s.ServerName,
		Version:      version.Version,
		PublicURL:    s.PublicURL,
		WeakPassword: s.WeakPassword,
	}
}

// postPassword sets a new admin password.
func (d Deps) postPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := d.SetPassword(body.Password); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	d.Log.Log("password", "the admin password changed")
	writeJSON(w, http.StatusOK, ok)
}

// clientIP gives the address of the caller with no port. httpguard cuts an
// address the same way for its own limiter, but it keeps that helper to itself.
// See the report: the helper belongs in the exported part of httpguard.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return strings.Trim(r.RemoteAddr, "[]")
}
