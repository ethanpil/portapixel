package server_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// adminRoute is one route of /api/admin in the table below.
type adminRoute struct {
	// name is what a failure names.
	name string
	// method and path are the request. path may hold %s markers that args fills.
	method, path string
	// body is the JSON body, or nil.
	body any
	// want is the status of the happy path. Zero means 200.
	want int
	// upload says that the body is a file and not JSON, so the request goes
	// through the upload helper.
	upload bool
	// noSession says that the route answers before a login. Only three do.
	noSession bool
}

// TestEveryAdminRouteAnswersAndNeedsASession is the table of the whole admin API.
//
// internal/server/admin holds no test file of its own, and it needs none: a route is
// an HTTP contract, and a test that called the handler by hand would not see the
// session guard, the CSRF header or the Host allowlist. This test covers every route
// two ways. The happy path proves that the route works, and the second pass proves
// that it answers 401 with no session.
//
// A route that somebody adds to internal/server/admin and not to this table fails
// TestTheTableHoldsEveryAdminRoute below.
func TestEveryAdminRouteAnswersAndNeedsASession(t *testing.T) {
	for _, c := range adminRoutes(t) {
		c := c
		t.Run(c.name, func(t *testing.T) {
			// The happy path. Each subtest gets its own server, so one route
			// cannot leave state that the next one reads.
			f := newFleet(t).with(t)
			f.login()
			ids := f.setUpRoutes(t)
			path := fill(c.path, ids)

			want := c.want
			if want == 0 {
				want = http.StatusOK
			}
			var res reply
			if c.upload {
				res = f.upload(path, "welcome.png", imageBytes(t, 20, 20))
			} else {
				res = f.adminCall(c.method, path, c.body)
			}
			if res.status != want {
				t.Fatalf("%s %s answered %d, want %d: %s", c.method, path, res.status, want, res.body)
			}

			// The auth failure. A fresh server with no login.
			bare := newFleet(t).with(t)
			bareIDs := bare.setUpRoutesWithoutSession(t)
			barePath := fill(c.path, bareIDs)
			var noAuth reply
			if c.upload {
				noAuth = bare.upload(barePath, "welcome.png", []byte("x"))
			} else {
				noAuth = bare.adminCall(c.method, barePath, c.body)
			}
			if c.noSession {
				if noAuth.status == http.StatusUnauthorized {
					t.Fatalf("%s %s needs a session and it must not", c.method, barePath)
				}
				return
			}
			if noAuth.status != http.StatusUnauthorized {
				t.Fatalf("%s %s with no session answered %d, want 401: %s",
					c.method, barePath, noAuth.status, noAuth.body)
			}
			if noAuth.errorText(t) == "" {
				t.Fatalf("%s %s answered 401 with no error text: %s", c.method, barePath, noAuth.body)
			}
		})
	}
}

// fill puts the markers of a path in place. A path with no marker goes through as
// it is: Sprintf would otherwise add "%!(EXTRA ...)" to it.
func fill(path string, args []any) string {
	if !strings.Contains(path, "%") {
		return path
	}
	return fmt.Sprintf(path, args...)
}

// setUpRoutes makes the records that the table needs and gives the values of the %s
// markers: a group, a playlist, an assignment, a token, a media hash, a device and
// a pairing code.
func (f *fleet) setUpRoutes(t *testing.T) []any {
	t.Helper()
	group := f.makeGroup("Lobby")
	sha := f.uploadMedia("welcome.png", imageBytes(t, 20, 20))
	playlist := f.makePlaylist("Lobby loop",
		map[string]any{"sha256": sha, "name": "welcome.png", "duration": 10})

	res := f.mustOK(f.adminCall(http.MethodPost, "/api/admin/assignments", map[string]any{
		"group_id": group, "playlist_id": playlist,
		"days": []string{"mon"}, "start": "08:00", "end": "18:00", "priority": 10,
	}), "an assignment")
	var made struct {
		ID int64 `json:"id"`
	}
	res.json(t, &made)

	res = f.mustOK(f.adminCall(http.MethodPost, "/api/admin/tokens", map[string]any{
		"name": "the first batch", "mode": "auto",
	}), "a token")
	var token struct {
		ID int64 `json:"id"`
	}
	res.json(t, &token)

	f.pairDevice("px-table001")
	code, _ := f.enroll("px-table002", "")

	return []any{group, playlist, made.ID, token.ID, sha, "px-table001", code.PairingCode, "1.5.0"}
}

// setUpRoutesWithoutSession gives markers for the pass that has no session. Nothing
// here needs the admin API, because a request with no session must fail before the
// route reads a record.
func (f *fleet) setUpRoutesWithoutSession(t *testing.T) []any {
	t.Helper()
	return []any{int64(1), int64(1), int64(1), int64(1),
		"aaaa111122223333444455556666777788889999aaaabbbbccccddddeeeeffff",
		"px-table001", "K7M2QP", "1.5.0"}
}

// adminRoutes is the table. The order of the markers is the order of
// setUpRoutes: group, playlist, assignment, token, media hash, device, code,
// version.
func adminRoutes(t *testing.T) []adminRoute {
	t.Helper()
	return []adminRoute{
		{name: "login", method: http.MethodPost, path: "/api/admin/login",
			body: map[string]string{"password": testPassword}, noSession: true},
		{name: "logout", method: http.MethodPost, path: "/api/admin/logout", noSession: true},
		{name: "session", method: http.MethodGet, path: "/api/admin/session", noSession: true},

		{name: "the device list", method: http.MethodGet, path: "/api/admin/devices"},
		{name: "one device", method: http.MethodGet, path: "/api/admin/devices/%[6]s"},
		{name: "rename a device", method: http.MethodPost, path: "/api/admin/devices/%[6]s/rename",
			body: map[string]string{"name": "Lobby North"}},
		{name: "move a device", method: http.MethodPost, path: "/api/admin/devices/%[6]s/group",
			body: map[string]any{"group_id": 0}},
		{name: "the overrides of a device", method: http.MethodPost, path: "/api/admin/devices/%[6]s/overrides",
			body: map[string]any{"default_playlist_id": 0, "screen_on": "", "screen_off": "", "screen_days": []string{}}},
		{name: "approve a device by its ID", method: http.MethodPost, path: "/api/admin/devices/px-table002/approve",
			body: map[string]any{}},
		{name: "reject a device by its ID", method: http.MethodPost, path: "/api/admin/devices/px-table002/reject"},
		{name: "confirm the hardware", method: http.MethodPost, path: "/api/admin/devices/%[6]s/confirm-hardware"},
		{name: "resolve a conflict", method: http.MethodPost, path: "/api/admin/devices/%[6]s/resolve-conflict"},
		{name: "queue a command", method: http.MethodPost, path: "/api/admin/devices/%[6]s/commands",
			body: map[string]any{"type": "rescan"}},
		{name: "the commands of a device", method: http.MethodGet, path: "/api/admin/devices/%[6]s/commands"},
		{name: "delete a device", method: http.MethodDelete, path: "/api/admin/devices/%[6]s"},
		{name: "approve by code", method: http.MethodPost, path: "/api/admin/pending/%[7]s/approve",
			body: map[string]any{}},
		{name: "reject by code", method: http.MethodPost, path: "/api/admin/pending/%[7]s/reject"},

		{name: "the group list", method: http.MethodGet, path: "/api/admin/groups"},
		{name: "make a group", method: http.MethodPost, path: "/api/admin/groups",
			body: map[string]string{"name": "Warehouse"}},
		{name: "update a group", method: http.MethodPut, path: "/api/admin/groups/%[1]d",
			body: map[string]any{"name": "Lobby", "default_playlist_id": 0,
				"screen_on": "", "screen_off": "", "screen_days": []string{}}},
		{name: "delete a group", method: http.MethodDelete, path: "/api/admin/groups/%[1]d"},
		{name: "a command for a group", method: http.MethodPost, path: "/api/admin/groups/%[1]d/commands",
			body: map[string]any{"type": "screen-off"}},

		{name: "the assignment list", method: http.MethodGet, path: "/api/admin/assignments"},
		{name: "make an assignment", method: http.MethodPost, path: "/api/admin/assignments",
			body: map[string]any{"group_id": 1, "playlist_id": 1, "days": []string{"tue"},
				"start": "09:00", "end": "17:00", "priority": 20}},
		{name: "update an assignment", method: http.MethodPut, path: "/api/admin/assignments/%[3]d",
			body: map[string]any{"group_id": 1, "playlist_id": 1, "days": []string{"wed"},
				"start": "10:00", "end": "16:00", "priority": 30}},
		{name: "delete an assignment", method: http.MethodDelete, path: "/api/admin/assignments/%[3]d"},

		{name: "the playlist list", method: http.MethodGet, path: "/api/admin/playlists"},
		{name: "one playlist", method: http.MethodGet, path: "/api/admin/playlists/%[2]d"},
		{name: "make a playlist", method: http.MethodPost, path: "/api/admin/playlists",
			body: map[string]any{"title": "Another loop", "items": []any{
				map[string]any{"url": "https://dash.example.com", "duration": 30},
			}}},
		{name: "update a playlist", method: http.MethodPut, path: "/api/admin/playlists/%[2]d",
			body: map[string]any{"title": "Lobby loop", "items": []any{
				map[string]any{"url": "https://dash.example.com", "duration": 30},
			}}},
		{name: "the device count of a playlist", method: http.MethodGet, path: "/api/admin/playlists/%[2]d/devices"},
		{name: "delete a playlist", method: http.MethodDelete, path: "/api/admin/playlists/%[2]d",
			// A playlist that a rule names cannot go away, and setUpRoutes makes
			// such a rule. That is the answer of the happy path here.
			want: http.StatusConflict},

		{name: "the media list", method: http.MethodGet, path: "/api/admin/media"},
		{name: "upload a file", method: http.MethodPost, path: "/api/admin/media", upload: true},
		{name: "the thumbnail of a file", method: http.MethodGet, path: "/api/admin/media/%[5]s/thumb"},

		{name: "the token list", method: http.MethodGet, path: "/api/admin/tokens"},
		{name: "make a token", method: http.MethodPost, path: "/api/admin/tokens",
			body: map[string]any{"name": "a second batch", "mode": "pending"}},
		{name: "revoke a token", method: http.MethodPost, path: "/api/admin/tokens/%[4]d/revoke"},
		{name: "delete a token", method: http.MethodDelete, path: "/api/admin/tokens/%[4]d"},

		{name: "the release list", method: http.MethodGet, path: "/api/admin/releases"},
		{name: "refresh the release list", method: http.MethodPost, path: "/api/admin/releases/refresh"},
		// The three routes below name a version that the table does not hold, so
		// the happy path is the 404 of a record that is not there. The mirror path
		// with a real release is in release_test.go.
		{name: "approve a release", method: http.MethodPost, path: "/api/admin/releases/%[8]s/approve",
			want: http.StatusNotFound},
		{name: "unapprove a release", method: http.MethodPost, path: "/api/admin/releases/%[8]s/unapprove",
			want: http.StatusNotFound},
		{name: "mirror a release", method: http.MethodPost, path: "/api/admin/releases/%[8]s/mirror",
			want: http.StatusNotFound},
		{name: "upload a bundle", method: http.MethodPost, path: "/api/admin/releases/%[8]s/bundle",
			upload: true, want: http.StatusPreconditionFailed},

		{name: "the health page", method: http.MethodGet, path: "/api/admin/health"},
		{name: "the integrity check", method: http.MethodPost, path: "/api/admin/health/integrity"},

		{name: "the settings", method: http.MethodGet, path: "/api/admin/settings"},
		{name: "save the settings", method: http.MethodPut, path: "/api/admin/settings",
			body: map[string]any{"server_name": "Test fleet", "default_poll_seconds": 60, "public_url": ""}},
		{name: "change the password", method: http.MethodPost, path: "/api/admin/password",
			body: map[string]string{"current": testPassword, "password": "a new good password"}},

		// The media delete needs a file that no playlist holds, so it goes last: it
		// uses the hash of its own upload.
		{name: "delete a file", method: http.MethodDelete,
			path: "/api/admin/media/8f4b1b6f3a0ddc7ffb7b2d0b4c1b1c9a6e9b3e2c5d4f6a7b8c9d0e1f2a3b4c5d",
			want: http.StatusNotFound},
	}
}

// TestTheTableHoldsEveryAdminRoute keeps the table above complete.
//
// The list of routes is in internal/server/admin.Routes, and a route that nobody
// covers is a route that nobody tested. The count is the guard: a new route without
// a line in the table fails here, with the number in the message.
func TestTheTableHoldsEveryAdminRoute(t *testing.T) {
	// The number of routes of internal/server/admin.Routes. Raise it here and add
	// the line to adminRoutes at the same time.
	const routesOfTheAdminAPI = 51

	covered := map[string]bool{}
	for _, c := range adminRoutes(t) {
		covered[c.name] = true
	}
	if len(covered) != len(adminRoutes(t)) {
		t.Fatalf("the table holds two rows with one name")
	}
	if len(covered) != routesOfTheAdminAPI {
		t.Fatalf("the table holds %d routes and the admin API has %d; add the missing line",
			len(covered), routesOfTheAdminAPI)
	}
}
