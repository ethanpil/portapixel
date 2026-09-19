package db

import (
	"testing"
	"time"
)

// fixture makes a database with two groups, three playlists and one media object.
type fixture struct {
	d                     *DB
	lobby, warehouse      int64
	loop, evening, safety int64
	sha                   string
}

const testSHA = "aaaa111122223333444455556666777788889999aaaabbbbccccddddeeeeffff"

func newFixture(t *testing.T) fixture {
	t.Helper()
	d := open(t)
	f := fixture{d: d, sha: testSHA}

	if err := d.AddMedia(Media{SHA256: f.sha, OrigName: "welcome.jpg", Size: 1234, MIME: "image/jpeg"}); err != nil {
		t.Fatal(err)
	}

	var err error
	if f.lobby, err = d.CreateGroup("Lobby"); err != nil {
		t.Fatal(err)
	}
	if f.warehouse, err = d.CreateGroup("Warehouse"); err != nil {
		t.Fatal(err)
	}
	f.loop = f.playlist(t, "Lobby loop")
	f.evening = f.playlist(t, "Evening slow loop")
	f.safety = f.playlist(t, "Safety loop")
	return f
}

func (f fixture) playlist(t *testing.T, title string) int64 {
	t.Helper()
	id, err := f.d.SavePlaylist(Playlist{
		Title: title,
		Items: []PlaylistItem{{SHA256: f.sha, Name: "welcome.jpg", Duration: 15}},
	})
	if err != nil {
		t.Fatalf("save %s: %v", title, err)
	}
	return id
}

// device pairs a device and puts it in a group.
func (f fixture) device(t *testing.T, id string, group int64) Device {
	t.Helper()
	pair(t, f.d, id)
	if group != 0 {
		if err := f.d.MoveDevice(id, group); err != nil {
			t.Fatal(err)
		}
	}
	dev, err := f.d.Device(id)
	if err != nil {
		t.Fatal(err)
	}
	return dev
}

var opt = ManifestOptions{
	ServerName: "Ridgeline", DefaultPoll: 60,
	MediaBase: "/api/v1/media/", ReleaseBase: "/api/v1/releases/",
}

func TestManifestOfAGroup(t *testing.T) {
	f := newFixture(t)
	dev := f.device(t, "px-group001", f.lobby)

	if err := f.d.UpdateGroup(Group{ID: f.lobby, Name: "Lobby", DefaultPlaylistID: f.loop,
		ScreenOn: "07:00", ScreenOff: "22:00", ScreenDays: "mon,tue"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.SaveAssignment(Assignment{GroupID: f.lobby, PlaylistID: f.evening,
		Days: []string{"mon", "fri"}, Start: "18:00", End: "22:00", Priority: 1}); err != nil {
		t.Fatal(err)
	}

	m, err := f.d.Manifest(dev, opt)
	if err != nil {
		t.Fatal(err)
	}
	if m.ServerName != "Ridgeline" || m.PollSeconds != 60 {
		t.Fatalf("the header is %+v", m)
	}
	if m.DefaultPlaylist != "lobby-loop" {
		t.Fatalf("the default playlist is %q", m.DefaultPlaylist)
	}
	if len(m.Schedule) != 1 || m.Schedule[0].Playlist != "evening-slow-loop" {
		t.Fatalf("the schedule is %+v", m.Schedule)
	}
	if len(m.Playlists) != 2 {
		t.Fatalf("the manifest holds %d playlists, want 2", len(m.Playlists))
	}
	// The media list holds one entry for the object, whatever number of playlists
	// name it.
	if len(m.Media) != 1 || m.Media[0].SHA256 != f.sha ||
		m.Media[0].URL != "/api/v1/media/"+f.sha || m.Media[0].Name != "welcome.jpg" {
		t.Fatalf("the media list is %+v", m.Media)
	}
	if m.Screen == nil || m.Screen.OnTime != "07:00" || len(m.Screen.Days) != 2 {
		t.Fatalf("the screen rule is %+v", m.Screen)
	}
}

func TestManifestDeviceRuleReplacesTheGroupRules(t *testing.T) {
	f := newFixture(t)
	dev := f.device(t, "px-over0001", f.warehouse)

	if err := f.d.UpdateGroup(Group{ID: f.warehouse, Name: "Warehouse", DefaultPlaylistID: f.loop}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.SaveAssignment(Assignment{GroupID: f.warehouse, PlaylistID: f.loop,
		Start: "08:00", End: "18:00"}); err != nil {
		t.Fatal(err)
	}

	// Without a device rule the group rule applies.
	m, err := f.d.Manifest(dev, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Schedule) != 1 || m.Schedule[0].Playlist != "lobby-loop" {
		t.Fatalf("the group rule did not apply: %+v", m.Schedule)
	}

	// One device rule replaces every group rule.
	if _, err := f.d.SaveAssignment(Assignment{DeviceID: dev.ID, PlaylistID: f.safety,
		Start: "00:00", End: "23:59"}); err != nil {
		t.Fatal(err)
	}
	m, err = f.d.Manifest(dev, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Schedule) != 1 || m.Schedule[0].Playlist != "safety-loop" {
		t.Fatalf("the device rule did not replace the group rules: %+v", m.Schedule)
	}
	// The group default still applies, because the device has none of its own.
	if m.DefaultPlaylist != "lobby-loop" {
		t.Fatalf("the default playlist is %q", m.DefaultPlaylist)
	}

	// A device default wins over the group default.
	if err := f.d.SetDeviceOverrides(dev.ID, f.evening, "", "", ""); err != nil {
		t.Fatal(err)
	}
	dev, _ = f.d.Device(dev.ID)
	m, err = f.d.Manifest(dev, opt)
	if err != nil {
		t.Fatal(err)
	}
	if m.DefaultPlaylist != "evening-slow-loop" {
		t.Fatalf("the device default did not win: %q", m.DefaultPlaylist)
	}
}

func TestManifestRulesGoOutInPriorityOrder(t *testing.T) {
	f := newFixture(t)
	dev := f.device(t, "px-prio0001", f.lobby)

	// Insert them in the wrong order on purpose.
	for _, c := range []struct {
		playlist int64
		priority int
	}{
		{f.safety, 30},
		{f.loop, 10},
		{f.evening, 20},
	} {
		if _, err := f.d.SaveAssignment(Assignment{GroupID: f.lobby, PlaylistID: c.playlist,
			Start: "08:00", End: "18:00", Priority: c.priority}); err != nil {
			t.Fatal(err)
		}
	}

	m, err := f.d.Manifest(dev, opt)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lobby-loop", "evening-slow-loop", "safety-loop"}
	if len(m.Schedule) != len(want) {
		t.Fatalf("the schedule holds %d rules, want %d", len(m.Schedule), len(want))
	}
	for i, name := range want {
		if m.Schedule[i].Playlist != name {
			t.Fatalf("rule %d is %q, want %q (the whole order is %+v)", i, m.Schedule[i].Playlist, name, m.Schedule)
		}
	}
}

func TestManifestScreenRuleOfTheDeviceWins(t *testing.T) {
	f := newFixture(t)
	dev := f.device(t, "px-screen01", f.lobby)

	if err := f.d.UpdateGroup(Group{ID: f.lobby, Name: "Lobby",
		ScreenOn: "07:00", ScreenOff: "22:00", ScreenDays: "mon"}); err != nil {
		t.Fatal(err)
	}
	if err := f.d.SetDeviceOverrides(dev.ID, 0, "06:00", "23:00", "sat,sun"); err != nil {
		t.Fatal(err)
	}
	dev, _ = f.d.Device(dev.ID)

	m, err := f.d.Manifest(dev, opt)
	if err != nil {
		t.Fatal(err)
	}
	if m.Screen == nil || m.Screen.OnTime != "06:00" || m.Screen.OffTime != "23:00" {
		t.Fatalf("the screen rule is %+v", m.Screen)
	}
}

func TestManifestReleaseOnlyWhenApprovedAndMirrored(t *testing.T) {
	f := newFixture(t)
	dev := f.device(t, "px-rel00001", 0)

	if err := f.d.NoteRelease("1.5.0", "notes", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Approved but not mirrored: no release goes out. A half-mirrored release
	// must never reach a card.
	if err := f.d.ApproveRelease("1.5.0"); err != nil {
		t.Fatal(err)
	}
	m, err := f.d.Manifest(dev, opt)
	if err != nil {
		t.Fatal(err)
	}
	if m.Release != nil {
		t.Fatalf("an unmirrored release went out: %+v", m.Release)
	}

	if err := f.d.SetMirrorState("1.5.0", MirrorDone, ""); err != nil {
		t.Fatal(err)
	}
	if m, err = f.d.Manifest(dev, opt); err != nil {
		t.Fatal(err)
	}
	if m.Release == nil || m.Release.Version != "1.5.0" ||
		m.Release.BaseURL != "/api/v1/releases/1.5.0" {
		t.Fatalf("the release is %+v", m.Release)
	}

	// A device that already runs the version gets no release.
	dev.Version = "1.5.0"
	if m, err = f.d.Manifest(dev, opt); err != nil {
		t.Fatal(err)
	}
	if m.Release != nil {
		t.Fatalf("a device that runs the version got a release: %+v", m.Release)
	}
}

func TestManifestCommandsAreDeliveredOnce(t *testing.T) {
	f := newFixture(t)
	dev := f.device(t, "px-cmd00001", 0)

	if _, err := f.d.QueueCommand(dev.ID, "restart-browser", nil); err != nil {
		t.Fatal(err)
	}
	m, err := f.d.Manifest(dev, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Commands) != 1 || m.Commands[0].Type != "restart-browser" {
		t.Fatalf("the commands are %+v", m.Commands)
	}
	if m, err = f.d.Manifest(dev, opt); err != nil {
		t.Fatal(err)
	}
	if len(m.Commands) != 0 {
		t.Fatalf("the command went out twice: %+v", m.Commands)
	}
}

func TestManifestUsesTheDevicePollInterval(t *testing.T) {
	f := newFixture(t)
	dev := f.device(t, "px-poll0001", 0)
	dev.PollSeconds = 300

	m, err := f.d.Manifest(dev, opt)
	if err != nil {
		t.Fatal(err)
	}
	if m.PollSeconds != 300 {
		t.Fatalf("the poll interval is %d, want 300", m.PollSeconds)
	}
}

func TestPlaylistValidation(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name  string
		in    Playlist
		field string
	}{
		{
			name:  "no items",
			in:    Playlist{Title: "empty"},
			field: "items",
		},
		{
			name:  "a file and a url",
			in:    Playlist{Title: "both", Items: []PlaylistItem{{SHA256: testSHA, URL: "https://a.example", Name: "a.jpg"}}},
			field: "items[0]",
		},
		{
			name:  "a url with no scheme",
			in:    Playlist{Title: "scheme", Items: []PlaylistItem{{URL: "a.example", Duration: 10}}},
			field: "items[0].url",
		},
		{
			name:  "mute on a url item",
			in:    Playlist{Title: "mute", Items: []PlaylistItem{{URL: "https://a.example", Duration: 10, Mute: true}}},
			field: "items[0].mute",
		},
		{
			name:  "max duration on a url item",
			in:    Playlist{Title: "max", Items: []PlaylistItem{{URL: "https://a.example", Duration: 10, MaxDuration: 5}}},
			field: "items[0].max_duration",
		},
		{
			name:  "refresh on a file item",
			in:    Playlist{Title: "refresh", Items: []PlaylistItem{{SHA256: testSHA, Name: "a.jpg", RefreshSeconds: 60}}},
			field: "items[0].refresh_seconds",
		},
		{
			name:  "a hash that is not a hash",
			in:    Playlist{Title: "hash", Items: []PlaylistItem{{SHA256: "../../etc/passwd", Name: "a.jpg"}}},
			field: "items[0].sha256",
		},
		{
			name:  "a hash that the library does not hold",
			in:    Playlist{Title: "missing", Items: []PlaylistItem{{SHA256: "b" + testSHA[1:], Name: "a.jpg"}}},
			field: "items[0].sha256",
		},
		{
			name:  "a transition that we do not know",
			in:    Playlist{Title: "trans", Transition: "explode", Items: []PlaylistItem{{SHA256: testSHA, Name: "a.jpg"}}},
			field: "transition",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.d.SavePlaylist(c.in)
			errs, isErrs := err.(Errors)
			if !isErrs {
				t.Fatalf("the save gave %v, want a field error list", err)
			}
			for _, fe := range errs {
				if fe.Field == c.field {
					return
				}
			}
			t.Fatalf("no error names the field %q; the errors are %v", c.field, errs)
		})
	}
}

func TestPlaylistNamesAreUniqueSlugs(t *testing.T) {
	f := newFixture(t)
	// "Lobby loop" is already there and its name is "lobby-loop".
	_, err := f.d.SavePlaylist(Playlist{
		Title: "Lobby  Loop!", Items: []PlaylistItem{{SHA256: f.sha, Name: "welcome.jpg"}},
	})
	errs, isErrs := err.(Errors)
	if !isErrs || errs[0].Field != "name" {
		t.Fatalf("the save gave %v, want a name clash", err)
	}
}

func TestPlaylistDeviceCount(t *testing.T) {
	f := newFixture(t)
	f.device(t, "px-count001", f.lobby)
	f.device(t, "px-count002", f.lobby)
	f.device(t, "px-count003", f.warehouse)

	if err := f.d.UpdateGroup(Group{ID: f.lobby, Name: "Lobby", DefaultPlaylistID: f.loop}); err != nil {
		t.Fatal(err)
	}
	n, err := f.d.PlaylistDeviceCount(f.loop)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("the count is %d, want 2", n)
	}

	// A rule of the warehouse group adds its screens.
	if _, err := f.d.SaveAssignment(Assignment{GroupID: f.warehouse, PlaylistID: f.loop,
		Start: "08:00", End: "18:00"}); err != nil {
		t.Fatal(err)
	}
	if n, err = f.d.PlaylistDeviceCount(f.loop); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("the count is %d, want 3", n)
	}
}

func TestDeletePlaylistThatIsInUse(t *testing.T) {
	f := newFixture(t)
	if _, err := f.d.SaveAssignment(Assignment{GroupID: f.lobby, PlaylistID: f.loop,
		Start: "08:00", End: "18:00"}); err != nil {
		t.Fatal(err)
	}
	if err := f.d.DeletePlaylist(f.loop); err != ErrInUse {
		t.Fatalf("the delete gave %v, want ErrInUse", err)
	}
	if err := f.d.DeletePlaylist(f.safety); err != nil {
		t.Fatalf("a playlist that nothing uses did not go away: %v", err)
	}
}

func TestDeleteMediaThatIsInUse(t *testing.T) {
	f := newFixture(t)
	users, err := f.d.DeleteMedia(f.sha)
	if err != ErrInUse {
		t.Fatalf("the delete gave %v, want ErrInUse", err)
	}
	if len(users) != 3 {
		t.Fatalf("the answer names %v, want the three playlists", users)
	}
}

func TestAssignmentValidation(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name  string
		in    Assignment
		field string
	}{
		{"no owner", Assignment{PlaylistID: f.loop}, "group_id"},
		{"two owners", Assignment{GroupID: f.lobby, DeviceID: "px-a", PlaylistID: f.loop}, "group_id"},
		{"no playlist", Assignment{GroupID: f.lobby}, "playlist_id"},
		{"a day that is not a day", Assignment{GroupID: f.lobby, PlaylistID: f.loop, Days: []string{"funday"}}, "days"},
		{"a time of the wrong shape", Assignment{GroupID: f.lobby, PlaylistID: f.loop, Start: "8am"}, "start"},
		{"an hour that is not an hour", Assignment{GroupID: f.lobby, PlaylistID: f.loop, End: "25:00"}, "end"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := f.d.SaveAssignment(c.in)
			errs, isErrs := err.(Errors)
			if !isErrs {
				t.Fatalf("the save gave %v, want a field error list", err)
			}
			for _, fe := range errs {
				if fe.Field == c.field {
					return
				}
			}
			t.Fatalf("no error names the field %q; the errors are %v", c.field, errs)
		})
	}
}
