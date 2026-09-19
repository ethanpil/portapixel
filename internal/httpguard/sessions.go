package httpguard

import (
	"net/http"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/rnd"
)

// CookieName is the name of the session cookie.
const CookieName = "pp_session"

// sessionLife is how long a session lives after its last use. The expiry
// slides: each request that uses the session moves it forward.
const sessionLife = 7 * 24 * time.Hour

// cookieRefresh is how often a live session gets its cookie again. The browser
// drops the cookie at the MaxAge of the last Set-Cookie, however long the
// session lives on the server, so the cookie must slide with it. One hour keeps
// Set-Cookie off nearly every request.
const cookieRefresh = time.Hour

// session is one logged-in browser.
type session struct {
	// expires is when the session dies if nobody uses it.
	expires time.Time
	// issued is when the browser last got the cookie of this session.
	issued time.Time
}

// Sessions holds the logged-in sessions in memory. A restart of the daemon logs
// everybody out, which is correct for an appliance: there is one admin user and
// no state worth keeping.
type Sessions struct {
	// Secure says if the cookie takes the Secure attribute. It is a function of
	// the request, because a TLS-terminating proxy makes the answer depend on a
	// header and not on r.TLS. The device daemon leaves it nil: the local API is
	// plain HTTP by design (D22), and a Secure cookie would never come back.
	Secure func(*http.Request) bool

	mu       sync.Mutex
	sessions map[string]session
	now      func() time.Time
}

// NewSessions makes an empty session store.
func NewSessions() *Sessions {
	return &Sessions{sessions: make(map[string]session), now: time.Now}
}

// Login makes a session and sets the cookie. The caller checks the password
// first. It gives the token of the new session, so that a caller can drop the
// other sessions of the same admin.
func (s *Sessions) Login(w http.ResponseWriter, r *http.Request) string {
	token := randomToken()

	s.mu.Lock()
	now := s.now()
	s.sessions[token] = session{expires: now.Add(sessionLife), issued: now}
	s.mu.Unlock()

	s.setSessionCookie(w, r, token)
	return token
}

// setSessionCookie writes the session cookie. MaxAge is the life of a session,
// and Require writes the cookie again while the session is in use, so the life
// in the browser and the life on the server stay together.
func (s *Sessions) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureFor(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionLife / time.Second),
	})
}

// secureFor answers if the cookie of this request takes the Secure attribute.
func (s *Sessions) secureFor(r *http.Request) bool {
	return s.Secure != nil && r != nil && s.Secure(r)
}

// Logout removes the session of this request and clears the cookie.
func (s *Sessions) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureFor(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// DropAllExcept ends every session but one. The password route calls it: a
// person who changes the password wants the other browsers out, because "change
// the password" is what somebody does after a stolen laptop.
func (s *Sessions) DropAllExcept(keep string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for token := range s.sessions {
		if token != keep {
			delete(s.sessions, token)
		}
	}
}

// Valid reports if the request carries a live session. It also moves the expiry
// forward, so that a user who works every day never has to log in again.
func (s *Sessions) Valid(r *http.Request) bool {
	_, ok := s.use(r)
	return ok
}

// use checks the session of a request and moves its expiry forward. It gives
// the token and says if the session is live.
func (s *Sessions) use(r *http.Request) (string, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return "", false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[c.Value]
	if !ok {
		return "", false
	}
	now := s.now()
	if now.After(sess.expires) {
		delete(s.sessions, c.Value)
		return "", false
	}
	sess.expires = now.Add(sessionLife)
	s.sessions[c.Value] = sess
	return c.Value, true
}

// Require rejects a request that carries no live session. It also gives the
// browser the cookie again from time to time: the expiry on the server slides
// with each request, and the cookie must slide with it or the browser logs the
// user out at seven days whatever they do.
func (s *Sessions) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := s.use(r)
		if !ok {
			deny(w, http.StatusUnauthorized, "log in first")
			return
		}
		if s.cookieIsOld(token) {
			s.setSessionCookie(w, r, token)
		}
		next.ServeHTTP(w, r)
	})
}

// cookieIsOld reports if the browser must get the cookie of this session again,
// and marks the cookie as given. It says true one time in cookieRefresh at
// most, so that a page of many requests does not carry a Set-Cookie on each one.
func (s *Sessions) cookieIsOld(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[token]
	if !ok {
		return false
	}
	now := s.now()
	if now.Sub(sess.issued) < cookieRefresh {
		return false
	}
	sess.issued = now
	s.sessions[token] = sess
	return true
}

// TokenBytes is the length of a session token before it becomes hexadecimal.
const TokenBytes = 32

// randomToken makes a session token. rnd.Hex owns the failure rule for every
// secret of this repository.
func randomToken() string { return rnd.Hex(TokenBytes) }
