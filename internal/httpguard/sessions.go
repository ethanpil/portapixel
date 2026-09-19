package httpguard

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// CookieName is the name of the session cookie.
const CookieName = "pp_session"

// sessionLife is how long a session lives after its last use. The expiry
// slides: each request that uses the session moves it forward.
const sessionLife = 7 * 24 * time.Hour

// Sessions holds the logged-in sessions in memory. A restart of the daemon logs
// everybody out, which is correct for an appliance: there is one admin user and
// no state worth keeping.
type Sessions struct {
	mu      sync.Mutex
	expires map[string]time.Time
	now     func() time.Time
}

// NewSessions makes an empty session store.
func NewSessions() *Sessions {
	return &Sessions{expires: make(map[string]time.Time), now: time.Now}
}

// Login makes a session and sets the cookie. The caller checks the password
// first.
func (s *Sessions) Login(w http.ResponseWriter) {
	token := randomToken()

	s.mu.Lock()
	s.expires[token] = s.now().Add(sessionLife)
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionLife / time.Second),
	})
}

// Logout removes the session of this request and clears the cookie.
func (s *Sessions) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		s.mu.Lock()
		delete(s.expires, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// Valid reports if the request carries a live session. It also moves the expiry
// forward, so that a user who works every day never has to log in again.
func (s *Sessions) Valid(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	expiry, ok := s.expires[c.Value]
	if !ok {
		return false
	}
	now := s.now()
	if now.After(expiry) {
		delete(s.expires, c.Value)
		return false
	}
	s.expires[c.Value] = now.Add(sessionLife)
	return true
}

// Require rejects a request that carries no live session.
func (s *Sessions) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Valid(r) {
			deny(w, http.StatusUnauthorized, "log in first")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// randomToken makes a 32-byte token as hex. rand.Read cannot fail.
func randomToken() string {
	var b [32]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
