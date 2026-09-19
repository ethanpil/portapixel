package admin

import (
	"net/http"

	"github.com/ethanpil/portapixel/internal/server/db"
	"github.com/ethanpil/portapixel/internal/server/httpjson"
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
	ip := d.clientIP(r)
	if !d.Limiter.Allow(ip) {
		httpjson.Error(w, http.StatusTooManyRequests, "too many attempts from this address; wait a minute")
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if !httpjson.Read(w, r, &body) {
		d.Limiter.Fail(ip)
		return
	}
	if !d.CheckPassword(body.Password) {
		d.Limiter.Fail(ip)
		d.Log.Log("login-failed", "from "+ip)
		httpjson.Error(w, http.StatusUnauthorized, "that password is not right")
		return
	}
	d.Limiter.Reset(ip)
	d.Sessions.Login(w, r)
	d.Log.Log("login", "from "+ip)
	httpjson.Write(w, http.StatusOK, d.session(true))
}

// postLogout ends the session.
func (d Deps) postLogout(w http.ResponseWriter, r *http.Request) {
	d.Sessions.Logout(w, r)
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}

// getSession says if the browser holds a session. It needs no session itself:
// the answer to "am I logged in" must work when the answer is no.
func (d Deps) getSession(w http.ResponseWriter, r *http.Request) {
	httpjson.Write(w, http.StatusOK, d.session(d.Sessions.Valid(r)))
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
//
// The route needs the password that is in use. A session cookie alone is not
// enough: a browser that somebody left open, or one cross-site request that got
// through, would otherwise change the one credential of the server. bcrypt does
// the comparison, so a wrong answer costs the same time as a right one, and the
// login limiter counts the wrong answers of this route as well.
//
// A password change drops every other session. "Change the password" is what
// somebody does after a laptop went missing, and a session that stayed open would
// make the change worth nothing.
func (d Deps) postPassword(w http.ResponseWriter, r *http.Request) {
	ip := d.clientIP(r)
	if !d.Limiter.Allow(ip) {
		httpjson.Error(w, http.StatusTooManyRequests, "too many attempts from this address; wait a minute")
		return
	}

	var body struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if !httpjson.Read(w, r, &body) {
		d.Limiter.Fail(ip)
		return
	}
	if !d.CheckPassword(body.Current) {
		d.Limiter.Fail(ip)
		d.Log.Log("password-failed", "a password change from "+ip+" gave the wrong current password")
		// 403 and not 401. web/shared/api.js signs the admin out at a 401, and a
		// wrong value in one field of a form is not a session that ended.
		httpjson.Write(w, http.StatusForbidden, map[string]any{
			"error": "the request has a field that this server cannot use",
			"fields": db.Errors{{Field: "current",
				Message: "that is not the password that this server uses now"}},
		})
		return
	}
	d.Limiter.Reset(ip)

	if err := d.SetPassword(body.Password); err != nil {
		httpjson.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// The browser that made the change keeps its session. Every other one goes.
	keep := d.Sessions.Login(w, r)
	d.Sessions.DropAllExcept(keep)
	d.Log.Log("password", "the admin password changed and the other sessions ended")
	httpjson.Write(w, http.StatusOK, httpjson.OK)
}
