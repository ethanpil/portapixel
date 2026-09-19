package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ethanpil/portapixel/internal/server/db"
)

// NewTokenView is the answer of POST /api/admin/tokens. It is the one time that
// the server sends the token itself, so it also gives the TOML block that goes
// into portapixel.toml on a card.
type NewTokenView struct {
	ID    int64  `json:"id"`
	Token string `json:"token"`
	// TOML is ready to paste into portapixel.toml on any number of cards (D25).
	TOML string `json:"toml"`
}

// getTokens lists the enrollment tokens. The token values are not in the list:
// the table holds only their hashes.
func (d Deps) getTokens(w http.ResponseWriter, r *http.Request) {
	list, err := d.DB.EnrollTokens()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": list})
}

// createToken makes an enrollment token and shows it one time.
func (d Deps) createToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Mode string `json:"mode"`
		// GroupID puts every device of this token in one group.
		GroupID int64 `json:"group_id"`
		// ExpiresAt is an RFC 3339 time, or an empty string for no expiry.
		ExpiresAt string `json:"expires_at"`
		MaxUses   int    `json:"max_uses"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	var expires time.Time
	if body.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, body.ExpiresAt)
		if err != nil {
			writeFields(w, "the request has a field that this server cannot use",
				db.Errors{{Field: "expires_at", Message: "must be a time such as 2026-12-31T23:59:59Z"}})
			return
		}
		expires = t
	}
	if body.GroupID != 0 {
		if _, err := d.DB.Group(body.GroupID); err != nil {
			fail(w, err)
			return
		}
	}

	id, token, err := d.DB.CreateEnrollToken(body.Name, body.Mode, body.GroupID, expires, body.MaxUses)
	if err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("token-create", "an enrollment token in "+body.Mode+" mode")
	writeJSON(w, http.StatusOK, NewTokenView{
		ID:    id,
		Token: token,
		TOML:  tomlBlock(d.Settings().PublicURL, token, d.defaultPoll()),
	})
}

// tomlBlock makes the [server] block of portapixel.toml. The admin pastes it on
// a card before the first boot, and the device pairs itself (D25).
func tomlBlock(publicURL, token string, poll int) string {
	if publicURL == "" {
		publicURL = "https://signage.example.com"
	}
	return "[server]\n" +
		"url = " + quote(publicURL) + "\n" +
		"token = " + quote(token) + "\n" +
		"poll_seconds = " + strconv.Itoa(poll) + "\n"
}

// quote puts a string in the TOML basic form. A URL and a hex token hold no
// character that needs an escape, but the value goes into a file that a parser
// reads, so the quotation is not left to chance.
func quote(s string) string {
	out := make([]rune, 0, len(s)+2)
	out = append(out, '"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			out = append(out, '\\', r)
		case '\n', '\r', '\t':
			out = append(out, ' ')
		default:
			out = append(out, r)
		}
	}
	return string(append(out, '"'))
}

// revokeToken stops a token. A card that already paired keeps working.
func (d Deps) revokeToken(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r, "id")
	if !okID {
		return
	}
	if err := d.DB.RevokeEnrollToken(id); err != nil {
		fail(w, err)
		return
	}
	d.Log.Log("token-revoke", strconv.FormatInt(id, 10))
	writeJSON(w, http.StatusOK, ok)
}

// deleteToken removes the row of a token.
func (d Deps) deleteToken(w http.ResponseWriter, r *http.Request) {
	id, okID := pathID(w, r, "id")
	if !okID {
		return
	}
	if err := d.DB.DeleteEnrollToken(id); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ok)
}
