package httpguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// login makes a session and gives the cookie that it set.
func login(t *testing.T, s *Sessions) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	s.Login(w)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Login set %d cookies, want 1", len(cookies))
	}
	return cookies[0]
}

func TestLoginCookie(t *testing.T) {
	s := NewSessions()
	c := login(t, s)

	if c.Name != CookieName {
		t.Errorf("cookie name = %q, want %q", c.Name, CookieName)
	}
	if len(c.Value) != 64 {
		t.Errorf("token is %d characters long, want 64 hex characters", len(c.Value))
	}
	if !c.HttpOnly {
		t.Error("the cookie must be HttpOnly")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Error("the cookie must be SameSite=Strict")
	}
	if c.Path != "/" {
		t.Errorf("cookie path = %q, want /", c.Path)
	}
	if c.MaxAge != 7*24*60*60 {
		t.Errorf("cookie MaxAge = %d, want seven days", c.MaxAge)
	}

	// Two logins must not give the same token.
	if other := login(t, s); other.Value == c.Value {
		t.Error("two sessions got the same token")
	}
}

func TestRequire(t *testing.T) {
	s := NewSessions()
	good := login(t, s)

	tests := []struct {
		name   string
		cookie *http.Cookie
		want   int
	}{
		{name: "a live session passes", cookie: good, want: 200},
		{name: "no cookie fails", want: 401},
		{name: "an empty token fails", cookie: &http.Cookie{Name: CookieName, Value: ""}, want: 401},
		{name: "an unknown token fails", cookie: &http.Cookie{Name: CookieName, Value: "abc"}, want: 401},
		{name: "another cookie fails", cookie: &http.Cookie{Name: "other", Value: good.Value}, want: 401},
	}

	h := s.Require(okHandler)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			if tt.cookie != nil {
				r.AddCookie(tt.cookie)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("got %d, want %d", w.Code, tt.want)
			}
		})
	}
}

func TestSessionExpiry(t *testing.T) {
	s := NewSessions()
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at }

	c := login(t, s)
	r := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	r.AddCookie(c)

	// Six days later the session still works and the expiry slides forward.
	at = at.Add(6 * 24 * time.Hour)
	if !s.Valid(r) {
		t.Fatal("the session must live for seven days")
	}

	// Six more days: the slide keeps the session alive.
	at = at.Add(6 * 24 * time.Hour)
	if !s.Valid(r) {
		t.Fatal("use of the session must move the expiry forward")
	}

	// Eight days with no use: the session is gone.
	at = at.Add(8 * 24 * time.Hour)
	if s.Valid(r) {
		t.Fatal("a session that nobody used for eight days must be gone")
	}
	if len(s.expires) != 0 {
		t.Fatalf("the store holds %d dead sessions", len(s.expires))
	}
}

func TestLogout(t *testing.T) {
	s := NewSessions()
	c := login(t, s)

	r := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	s.Logout(w, r)

	if s.Valid(r) {
		t.Fatal("the session must be gone after Logout")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatalf("Logout must clear the cookie, got %+v", cookies)
	}
}
