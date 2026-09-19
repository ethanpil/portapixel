package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// state is what fill writes down, so swap and poll can speak for the same
// screens later.
type state struct {
	Base    string            `json:"base"`
	Tokens  map[string]string `json:"tokens"`  // device ID -> device token
	Names   map[string]string `json:"names"`   // device ID -> screen name
	Secrets map[string]string `json:"secrets"` // device ID -> claim secret of a pending screen
}

func loadState(path string) (state, error) {
	var s state
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}

func (s state) save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// The screens of the test fleet. Each one shows the UI a different state.
type screen struct {
	id    string
	name  string
	group string
	// hardware is the first hardware ID that the screen reports.
	hardware string
	version  string
}

var screens = []screen{
	{id: "px-4a91c7e2", name: "Lobby North", group: "Lobby", hardware: "a1b2c3d4e5f60718", version: "1.5.0"},
	{id: "px-7c33b190", name: "Cafeteria West", group: "Cafeteria", hardware: "b2c3d4e5f6071829", version: "1.5.0"},
	{id: "px-5e10d8b7", name: "Warehouse A", group: "Warehouse", hardware: "c3d4e5f60718293a", version: "1.4.2"},
	{id: "px-9a02f451", name: "Meeting room 3", group: "Meeting rooms", hardware: "d4e5f60718293a4b", version: "1.5.0"},
	{id: "px-2f81c604", name: "Front window", group: "Retail", hardware: "e5f60718293a4b5c", version: "1.5.0"},
}

// pendingScreen asks to pair by code, so it waits in the list with a code.
var pendingScreen = screen{id: "px-b71e33d0", name: "Stock room", hardware: "f60718293a4b5c6d", version: "1.5.0"}

func fill(base, password, statePath string) error {
	if password == "" {
		return fmt.Errorf("fill needs -password")
	}
	c, err := newClient(base)
	if err != nil {
		return err
	}
	if err := c.call("POST", "/api/admin/login", map[string]string{"password": password}, nil); err != nil {
		return err
	}
	fmt.Println("signed in")

	groups, err := makeGroups(c)
	if err != nil {
		return err
	}
	media, err := upload(c)
	if err != nil {
		return err
	}
	playlists, err := makePlaylists(c, media)
	if err != nil {
		return err
	}
	if err := setGroups(c, groups, playlists); err != nil {
		return err
	}
	if err := makeRules(c, groups, playlists); err != nil {
		return err
	}
	token, err := makeTokens(c, groups)
	if err != nil {
		return err
	}
	st, err := enroll(c, base, token, groups)
	if err != nil {
		return err
	}
	if err := firstHeartbeats(c, st); err != nil {
		return err
	}
	if err := queueWork(c); err != nil {
		return err
	}
	if err := st.save(statePath); err != nil {
		return err
	}
	fmt.Printf("the state file is %s\n", statePath)
	return nil
}

/* ------------------------------------------------------------------- groups */

var groupNames = []string{"Lobby", "Cafeteria", "Warehouse", "Retail", "Meeting rooms"}

func makeGroups(c *client) (map[string]int64, error) {
	out := map[string]int64{}
	var list struct {
		Groups []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"groups"`
	}
	if err := c.call("GET", "/api/admin/groups", nil, &list); err != nil {
		return nil, err
	}
	for _, g := range list.Groups {
		out[g.Name] = g.ID
	}
	for _, name := range groupNames {
		if out[name] != 0 {
			continue
		}
		var made struct {
			ID int64 `json:"id"`
		}
		if err := c.call("POST", "/api/admin/groups", map[string]string{"name": name}, &made); err != nil {
			return nil, err
		}
		out[name] = made.ID
	}
	fmt.Printf("%d groups\n", len(out))
	return out, nil
}

/* -------------------------------------------------------------------- media */

func upload(c *client) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range library() {
		var res struct {
			Media struct {
				SHA256 string `json:"sha256"`
			} `json:"media"`
			Duplicate bool `json:"duplicate"`
		}
		if err := c.put("/api/admin/media", f.name, f.bytes, &res); err != nil {
			return nil, err
		}
		out[f.name] = res.Media.SHA256
	}
	fmt.Printf("%d files in the library\n", len(out))
	return out, nil
}

/* ---------------------------------------------------------------- playlists */

type item map[string]any

func makePlaylists(c *client, media map[string]string) (map[string]int64, error) {
	wanted := []struct {
		title string
		items []item
	}{
		{"Lobby loop", []item{
			{"sha256": media["welcome-autumn.jpg"], "name": "welcome-autumn.jpg", "duration": 15},
			{"sha256": media["lobby-hours.jpg"], "name": "lobby-hours.jpg", "duration": 12},
			{"sha256": media["promo-fall.mp4"], "name": "promo-fall.mp4", "mute": false},
			{"url": "https://dashboards.example.com/lobby", "duration": 40, "refresh_seconds": 300},
		}},
		{"Safety loop", []item{
			{"sha256": media["safety-notice-1.png"], "name": "safety-notice-1.png", "duration": 20},
			{"sha256": media["safety-notice-2.png"], "name": "safety-notice-2.png", "duration": 20},
			{"sha256": media["safety-brief-4k-hevc.mp4"], "name": "safety-brief-4k-hevc.mp4", "mute": true},
		}},
		{"Menu boards", []item{
			{"sha256": media["menu-board.png"], "name": "menu-board.png", "duration": 30},
		}},
		{"Retail promo", []item{
			{"sha256": media["retail-promo.jpg"], "name": "retail-promo.jpg", "duration": 10},
			{"sha256": media["promo-fall.mp4"], "name": "promo-fall.mp4", "mute": false, "max_duration": 24},
		}},
		{"Room signs", []item{
			{"sha256": media["room-signs.jpg"], "name": "room-signs.jpg", "duration": 20},
		}},
	}

	out := map[string]int64{}
	var list struct {
		Playlists []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"playlists"`
	}
	if err := c.call("GET", "/api/admin/playlists", nil, &list); err != nil {
		return nil, err
	}
	for _, p := range list.Playlists {
		out[p.Title] = p.ID
	}
	for _, w := range wanted {
		if out[w.title] != 0 {
			continue
		}
		var saved struct {
			ID int64 `json:"id"`
		}
		body := map[string]any{"title": w.title, "transition": "crossfade", "items": w.items}
		if err := c.call("POST", "/api/admin/playlists", body, &saved); err != nil {
			return nil, fmt.Errorf("playlist %s: %w", w.title, err)
		}
		out[w.title] = saved.ID
	}
	fmt.Printf("%d playlists\n", len(out))
	return out, nil
}

func setGroups(c *client, groups, playlists map[string]int64) error {
	defaults := map[string]string{
		"Lobby":         "Lobby loop",
		"Cafeteria":     "Menu boards",
		"Warehouse":     "Safety loop",
		"Retail":        "Retail promo",
		"Meeting rooms": "Room signs",
	}
	for name, id := range groups {
		body := map[string]any{
			"name":                name,
			"default_playlist_id": playlists[defaults[name]],
			"screen_on":           "07:00",
			"screen_off":          "22:00",
			"screen_days":         []string{"mon", "tue", "wed", "thu", "fri"},
		}
		if name == "Warehouse" {
			// A warehouse runs all hours, so it keeps no screen rule.
			body["screen_on"], body["screen_off"], body["screen_days"] = "", "", []string{}
		}
		if err := c.call("PUT", fmt.Sprintf("/api/admin/groups/%d", id), body, nil); err != nil {
			return err
		}
	}
	return nil
}

func makeRules(c *client, groups, playlists map[string]int64) error {
	var have struct {
		Assignments []struct {
			ID int64 `json:"id"`
		} `json:"assignments"`
	}
	if err := c.call("GET", "/api/admin/assignments", nil, &have); err != nil {
		return err
	}
	if len(have.Assignments) > 0 {
		return nil
	}
	rules := []map[string]any{
		{"group_id": groups["Lobby"], "playlist_id": playlists["Lobby loop"],
			"days": []string{"mon", "tue", "wed", "thu", "fri"}, "start": "08:00", "end": "18:00", "priority": 10},
		{"group_id": groups["Lobby"], "playlist_id": playlists["Retail promo"],
			"days": []string{"mon", "tue", "wed", "thu", "fri"}, "start": "18:00", "end": "22:00", "priority": 20},
		{"group_id": groups["Lobby"], "playlist_id": playlists["Menu boards"],
			"days": []string{"sat", "sun"}, "start": "10:00", "end": "17:00", "priority": 30},
		{"group_id": groups["Retail"], "playlist_id": playlists["Retail promo"],
			"days": []string{}, "start": "09:00", "end": "21:00", "priority": 10},
	}
	for _, r := range rules {
		if err := c.call("POST", "/api/admin/assignments", r, nil); err != nil {
			return err
		}
	}
	fmt.Printf("%d time rules\n", len(rules))
	return nil
}

/* ------------------------------------------------------------------- tokens */

// makeTokens makes the enrollment tokens and gives the value of the one that the
// fake screens use.
func makeTokens(c *client, groups map[string]int64) (string, error) {
	var first struct {
		Token string `json:"token"`
	}
	body := map[string]any{"name": "the first batch", "mode": "auto", "group_id": 0, "max_uses": 50}
	if err := c.call("POST", "/api/admin/tokens", body, &first); err != nil {
		return "", err
	}

	// A second token in pending mode, and a third one that is revoked, so the
	// enrollment page has a list with more than one state in it.
	var second struct {
		ID int64 `json:"id"`
	}
	waiting := map[string]any{
		"name": "cafeteria cards", "mode": "pending", "group_id": groups["Cafeteria"],
		"expires_at": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339), "max_uses": 10,
	}
	if err := c.call("POST", "/api/admin/tokens", waiting, &second); err != nil {
		return "", err
	}
	var third struct {
		ID int64 `json:"id"`
	}
	if err := c.call("POST", "/api/admin/tokens", map[string]any{"name": "old batch", "mode": "auto"}, &third); err != nil {
		return "", err
	}
	if err := c.call("POST", fmt.Sprintf("/api/admin/tokens/%d/revoke", third.ID), map[string]any{}, nil); err != nil {
		return "", err
	}
	fmt.Println("3 enrollment tokens")
	return first.Token, nil
}

/* ------------------------------------------------------------------ screens */

func enroll(c *client, base, token string, groups map[string]int64) (state, error) {
	st := state{Base: base, Tokens: map[string]string{}, Names: map[string]string{}, Secrets: map[string]string{}}

	for _, s := range screens {
		var res manifest.EnrollResponse
		req := manifest.EnrollRequest{
			DeviceID: s.id, HardwareID: s.hardware, Name: s.name, Token: token, Version: s.version,
		}
		if err := c.call("POST", "/api/v1/enroll", req, &res); err != nil {
			return st, err
		}
		if res.DeviceToken == "" {
			return st, fmt.Errorf("%s got no device token", s.id)
		}
		st.Tokens[s.id] = res.DeviceToken
		st.Names[s.id] = s.name
		if id := groups[s.group]; id != 0 {
			if err := c.call("POST", "/api/admin/devices/"+s.id+"/group", map[string]any{"group_id": id}, nil); err != nil {
				return st, err
			}
		}
	}

	// The screen that pairs by code. It sends no token, so it waits in the list.
	var res manifest.EnrollResponse
	req := manifest.EnrollRequest{
		DeviceID: pendingScreen.id, HardwareID: pendingScreen.hardware,
		Name: pendingScreen.name, Version: pendingScreen.version,
	}
	if err := c.call("POST", "/api/v1/enroll", req, &res); err != nil {
		return st, err
	}
	st.Names[pendingScreen.id] = pendingScreen.name
	st.Secrets[pendingScreen.id] = res.ClaimSecret
	fmt.Printf("%d screens paired, one waits with the code %s\n", len(screens), res.PairingCode)
	return st, nil
}

// firstHeartbeats gives each screen a health report. The conflict case needs two
// reports with two hardware IDs inside the clone window.
func firstHeartbeats(c *client, st state) error {
	for _, s := range screens {
		hb := heartbeatFor(s)
		if err := c.bearer("POST", "/api/v1/heartbeat", st.Tokens[s.id], hb, nil); err != nil {
			return err
		}
	}

	// Two hardware IDs on one token, a moment apart: a clone (D21).
	front := screens[4]
	hb := heartbeatFor(front)
	hb.HardwareID = "999888777666555a"
	if err := c.bearer("POST", "/api/v1/heartbeat", st.Tokens[front.id], hb, nil); err != nil {
		return err
	}
	fmt.Println("heartbeats sent; one screen reports a clone conflict")
	return nil
}

// queueWork puts one command in a queue, so the screen detail page has a history
// to draw before anything is pressed.
func queueWork(c *client) error {
	return c.call("POST", "/api/admin/devices/px-5e10d8b7/commands",
		map[string]any{"type": "rescan"}, nil)
}

/* --------------------------------------------------------------------- swap */

// swap reports a new hardware ID on one screen. The last contact of that screen
// must be older than the clone window, which backdate makes true, so the server
// reads it as a repair and asks for one click (D21).
func swap(base, statePath string) error {
	st, err := loadState(statePath)
	if err != nil {
		return err
	}
	c, err := newClient(base)
	if err != nil {
		return err
	}
	target := screens[3] // Meeting room 3
	hb := heartbeatFor(target)
	hb.HardwareID = "111222333444555b"
	if err := c.bearer("POST", "/api/v1/heartbeat", st.Tokens[target.id], hb, nil); err != nil {
		return err
	}
	fmt.Printf("%s now reports new hardware\n", target.name)
	return nil
}
