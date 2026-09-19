package syncer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/manifest"
)

// pairedDev gives a device that is paired with the server.
func pairedDev(t *testing.T, f *fakeServer) *dev {
	t.Helper()
	d := newDev(t, f)
	if _, err := d.s.Pair(context.Background(), f.srv.URL, f.enrollToken, true); err != nil {
		t.Fatalf("pair: %v", err)
	}
	return d
}

// lobbyManifest is one playlist with one media item and one URL item.
func lobbyManifest(ref manifest.MediaRef) manifest.Manifest {
	return manifest.Manifest{
		DefaultPlaylist: "lobby",
		Schedule:        []manifest.Rule{{Playlist: "lobby", Days: []string{"mon"}, Start: "08:00", End: "18:00"}},
		Screen:          &manifest.ScreenRule{OnTime: "07:30", OffTime: "22:00"},
		Media:           []manifest.MediaRef{ref},
		Playlists: []manifest.Playlist{{
			Name: "lobby", Title: "Lobby loop", Transition: "cut",
			Items: []manifest.Item{
				{SHA256: ref.SHA256, Duration: 15},
				{URL: "https://dash.example.com/board", Duration: 60, RefreshSeconds: 300},
			},
		}},
	}
}

func TestApplyWritesTheFleetPlaylistAndTheObject(t *testing.T) {
	f := newFakeServer(t)
	ref := f.addObject("Welcome Sign.jpg", "the picture of the lobby")
	f.setManifest(lobbyManifest(ref))

	d := pairedDev(t, f)
	if next := d.s.Once(context.Background()); next <= 0 {
		t.Fatalf("the wait after a good poll is %v", next)
	}

	// The object is in the store under <sha8>-<safe name> (ARCHITECTURE 5).
	object := d.objectPath(ref)
	if filepath.Base(object) != ref.SHA256[:8]+"-Welcome-Sign.jpg" {
		t.Errorf("the object name is %q", filepath.Base(object))
	}
	data, err := os.ReadFile(object)
	if err != nil || string(data) != "the picture of the lobby" {
		t.Fatalf("the object holds %q (%v)", data, err)
	}

	// The playlist names the object by the one permitted fleet path.
	text := d.readFleetPlaylist("lobby")
	if !strings.Contains(text, `file = "../media/`+ref.SHA256[:8]+`-Welcome-Sign.jpg"`) {
		t.Errorf("the fleet playlist is:\n%s", text)
	}
	if !strings.Contains(text, `url = "https://dash.example.com/board"`) {
		t.Errorf("the URL item is missing:\n%s", text)
	}

	// The library serves it, because the device is paired.
	p, ok := d.lib.Snapshot().Find("lobby")
	if !ok {
		t.Fatal("the library does not serve the fleet playlist")
	}
	if len(p.Items) != 2 || p.Items[0].Missing {
		t.Errorf("the library read %+v", p.Items)
	}
	if !p.Fleet {
		t.Error("the library does not mark the playlist as managed")
	}

	// The scheduler got the rules and the screen times of the server.
	if d.fleetDefault != "lobby" || len(d.fleetRules) != 1 || d.fleetScreen == nil {
		t.Errorf("the scheduler got %q, %d rules and screen %+v", d.fleetDefault, len(d.fleetRules), d.fleetScreen)
	}
	if d.rescans != 1 {
		t.Errorf("the device rescanned %d times, want 1", d.rescans)
	}

	// The heartbeat followed the poll and carries the full hardware ID.
	if len(f.beats) != 1 {
		t.Fatalf("the server got %d heartbeats", len(f.beats))
	}
	if f.beats[0].HardwareID != strings.Repeat("ab", 32) {
		t.Errorf("the heartbeat carries the hardware ID %q", f.beats[0].HardwareID)
	}
	if f.beats[0].Status.PairingCode != "" {
		t.Error("the heartbeat carries the pairing code, which is a loopback secret")
	}
	if f.beats[0].SyncError != "" {
		t.Errorf("the heartbeat reports the fault %q", f.beats[0].SyncError)
	}
}

// TestSecondApplyOfTheSameManifestWritesNothing is the steady-state rule: no file
// changes, no rescan and no player event while the server says the same thing (D2).
func TestSecondApplyOfTheSameManifestWritesNothing(t *testing.T) {
	f := newFakeServer(t)
	ref := f.addObject("welcome.jpg", "the picture of the lobby")
	f.setManifest(lobbyManifest(ref))

	d := pairedDev(t, f)
	d.s.Once(context.Background())

	before := treeTimes(t, d.media)
	rescans := d.rescans

	// A file system stores a modification time with a resolution that can be a
	// second, so the second pass happens after a pause that a change would show.
	time.Sleep(20 * time.Millisecond)
	d.s.Once(context.Background())

	after := treeTimes(t, d.media)
	for name, when := range after {
		was, had := before[name]
		switch {
		case !had:
			t.Errorf("the second pass made %s", name)
		case !when.Equal(was):
			t.Errorf("the second pass wrote %s again", name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			t.Errorf("the second pass removed %s", name)
		}
	}
	if d.rescans != rescans {
		t.Errorf("the second pass rescanned %d times, want 0", d.rescans-rescans)
	}
	if f.objectHits[ref.SHA256] != 1 {
		t.Errorf("the device asked for the object %d times", f.objectHits[ref.SHA256])
	}
	if len(f.beats) != 2 {
		t.Errorf("the device sent %d heartbeats; one follows every poll", len(f.beats))
	}
}

// TestObjectComesFromALocalPlaylistBySHA is the smart cache of plan section 12: a
// file that is on the card under any name with a matching hash is never fetched.
func TestObjectComesFromALocalPlaylistBySHA(t *testing.T) {
	f := newFakeServer(t)
	const content = "the same bytes on both ends"
	ref := f.addObject("from-the-server.jpg", content)
	f.setManifest(manifest.Manifest{
		DefaultPlaylist: "lobby",
		Media:           []manifest.MediaRef{ref},
		Playlists: []manifest.Playlist{{
			Name: "lobby", Items: []manifest.Item{{SHA256: ref.SHA256, Duration: 10}},
		}},
	})

	d := newDev(t, f)
	// A local playlist that a person sideloaded holds the same picture under
	// another name.
	local := d.write("sideload/holiday.jpg", content)
	d.write("sideload/playlist.toml", "[[item]]\nfile = \"holiday.jpg\"\n")
	d.lib.Rescan()
	hashEverything(t, d)

	if _, err := d.s.Pair(context.Background(), f.srv.URL, f.enrollToken, true); err != nil {
		t.Fatalf("pair: %v", err)
	}
	d.s.Once(context.Background())

	if hits := f.objectHits[ref.SHA256]; hits != 0 {
		t.Errorf("the device downloaded the object %d times", hits)
	}
	data, err := os.ReadFile(d.objectPath(ref))
	if err != nil || string(data) != content {
		t.Fatalf("the object of the store holds %q (%v)", data, err)
	}
	// The copy leaves the file of the person where it is.
	if _, err := os.Stat(local); err != nil {
		t.Errorf("the local file went away: %v", err)
	}
}

// TestDownloadResumesAfterADroppedConnection is D24: the part file survives and the
// retry sends a Range header.
func TestDownloadResumesAfterADroppedConnection(t *testing.T) {
	f := newFakeServer(t)
	content := strings.Repeat("0123456789", 20) // 200 bytes
	ref := f.addObject("clip.mp4", content)
	f.setManifest(manifest.Manifest{
		DefaultPlaylist: "lobby",
		Media:           []manifest.MediaRef{ref},
		Playlists: []manifest.Playlist{{
			Name: "lobby", Items: []manifest.Item{{SHA256: ref.SHA256, Duration: 10}},
		}},
	})
	f.mu.Lock()
	f.cut = 64 // the connection drops after 64 bytes
	f.mu.Unlock()

	d := pairedDev(t, f)
	d.s.Once(context.Background())

	// The round failed, the part file holds what arrived, and no playlist is there.
	part := d.objectPath(ref) + partSuffix
	info, err := os.Stat(part)
	if err != nil {
		t.Fatalf("the part file is not there: %v", err)
	}
	if info.Size() != 64 {
		t.Errorf("the part file holds %d bytes, want 64", info.Size())
	}
	if _, err := os.Stat(d.fleetPath("lobby")); err == nil {
		t.Error("a failed round wrote a playlist")
	}
	if got := d.s.Report().SyncError; got == "" {
		t.Error("the report says nothing about the failed object")
	}

	// The next poll continues from byte 64.
	d.s.Once(context.Background())
	if _, err := os.Stat(part); err == nil {
		t.Error("the part file stayed after the download finished")
	}
	data, err := os.ReadFile(d.objectPath(ref))
	if err != nil || string(data) != content {
		t.Fatalf("the object holds %d bytes (%v)", len(data), err)
	}
	if len(f.ranges) < 2 || f.ranges[1] != "bytes=64-" {
		t.Errorf("the ranges of the two requests are %q", f.ranges)
	}
	if d.rescans != 1 {
		t.Errorf("the device rescanned %d times, want 1", d.rescans)
	}
}

// TestBytesThatDoNotMatchTheHashKeepTheOldPlaylists proves that a bad object never
// reaches the screen and never takes the playlists that work.
func TestBytesThatDoNotMatchTheHashKeepTheOldPlaylists(t *testing.T) {
	f := newFakeServer(t)
	good := f.addObject("welcome.jpg", "the picture of the lobby")
	f.setManifest(lobbyManifest(good))

	d := pairedDev(t, f)
	d.s.Once(context.Background())
	first := d.readFleetPlaylist("lobby")

	// The server offers a second playlist whose object answers with other bytes of
	// the same length.
	bad := f.addObject("news.jpg", "the picture of the news")
	f.mu.Lock()
	f.served[bad.SHA256] = []byte("XXX picture XX the XXXX")
	f.mu.Unlock()
	next := lobbyManifest(good)
	next.Media = append(next.Media, bad)
	next.Playlists = append(next.Playlists, manifest.Playlist{
		Name: "news", Items: []manifest.Item{{SHA256: bad.SHA256, Duration: 10}},
	})
	f.setManifest(next)

	d.s.Once(context.Background())

	if got := d.readFleetPlaylist("lobby"); got != first {
		t.Errorf("the old playlist changed:\n%s", got)
	}
	if _, err := os.Stat(d.fleetPath("news")); err == nil {
		t.Error("the failed round wrote the new playlist")
	}
	// The bad bytes are not on the card, not even as a part file: a hash that does
	// not match means that a resume would keep the wrong bytes.
	if _, err := os.Stat(d.objectPath(bad)); err == nil {
		t.Error("the object with the wrong bytes stayed")
	}
	if _, err := os.Stat(d.objectPath(bad) + partSuffix); err == nil {
		t.Error("the part file of the wrong bytes stayed")
	}
	if !strings.Contains(d.s.Report().SyncError, "SHA-256") {
		t.Errorf("the report says %q", d.s.Report().SyncError)
	}
}

// TestAManifestThatDoesNotFitFailsBeforeAnyDownload is D41.
func TestAManifestThatDoesNotFitFailsBeforeAnyDownload(t *testing.T) {
	f := newFakeServer(t)
	big := f.addObject("feature.mp4", strings.Repeat("x", 4096))
	f.setManifest(manifest.Manifest{
		DefaultPlaylist: "lobby",
		Media:           []manifest.MediaRef{big},
		Playlists: []manifest.Playlist{{
			Name: "lobby", Items: []manifest.Item{{SHA256: big.SHA256, Duration: 10}},
		}},
	})

	d := pairedDev(t, f)
	// The partition has the reserve and 100 bytes, and the object needs 4096.
	d.free = SpaceReserve + 100
	d.s.Once(context.Background())

	if hits := f.objectHits[big.SHA256]; hits != 0 {
		t.Errorf("the device downloaded %d times before it gave up", hits)
	}
	if _, err := os.Stat(d.fleetPath("lobby")); err == nil {
		t.Error("the refused round wrote a playlist")
	}
	message := d.s.Report().SyncError
	if !strings.Contains(message, "needs ") || !strings.Contains(message, "has ") {
		t.Errorf("the sentence is %q, and D41 wants needs and has", message)
	}
	// The next heartbeat carries the sentence, so the fleet dashboard shows it too.
	if len(f.beats) != 1 || !strings.Contains(f.beats[0].SyncError, "needs ") {
		t.Errorf("the heartbeat carries %q", f.beats[0].SyncError)
	}
}

// TestEvictionTakesTheOldestUnreferencedObjectsOnly is the other half of D41: the
// device frees what it needs and no more, and it never touches an object that the
// manifest names.
func TestEvictionTakesTheOldestUnreferencedObjectsOnly(t *testing.T) {
	f := newFakeServer(t)
	old := f.addObject("old.jpg", strings.Repeat("o", 100))
	middle := f.addObject("middle.jpg", strings.Repeat("m", 100))
	fresh := f.addObject("fresh.jpg", strings.Repeat("f", 100))

	twoObjects := manifest.Manifest{
		DefaultPlaylist: "lobby",
		Media:           []manifest.MediaRef{old, middle},
		Playlists: []manifest.Playlist{{Name: "lobby", Items: []manifest.Item{
			{SHA256: old.SHA256, Duration: 10}, {SHA256: middle.SHA256, Duration: 10},
		}}},
	}
	f.setManifest(twoObjects)

	d := pairedDev(t, f)
	d.s.Once(context.Background())
	for _, ref := range []manifest.MediaRef{old, middle} {
		if _, err := os.Stat(d.objectPath(ref)); err != nil {
			t.Fatalf("the first round did not store %s: %v", ref.Name, err)
		}
	}
	// The oldest object is older than the other one.
	when := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(d.objectPath(old), when, when); err != nil {
		t.Fatal(err)
	}

	// A new manifest names one object that the device does not have, and the
	// partition has room for 50 of the 100 bytes that it needs.
	f.setManifest(manifest.Manifest{
		DefaultPlaylist: "lobby",
		Media:           []manifest.MediaRef{fresh},
		Playlists: []manifest.Playlist{{Name: "lobby", Items: []manifest.Item{
			{SHA256: fresh.SHA256, Duration: 10},
		}}},
	})
	d.free = SpaceReserve + 50
	d.s.Once(context.Background())

	if err := d.s.Report().SyncError; err != "" {
		t.Fatalf("the round failed: %s", err)
	}
	if _, err := os.Stat(d.objectPath(old)); err == nil {
		t.Error("the oldest object stayed")
	}
	if _, err := os.Stat(d.objectPath(middle)); err != nil {
		t.Error("the eviction took more objects than the round needed")
	}
	if _, err := os.Stat(d.objectPath(fresh)); err != nil {
		t.Errorf("the new object is not in the store: %v", err)
	}
}

// TestAManifestWithBadReferencesIsSafe proves that a manifest is treated as data.
// A hash that is a path step, or an address on another host, is left out and
// nothing is written outside the object store.
func TestAManifestWithBadReferencesIsSafe(t *testing.T) {
	f := newFakeServer(t)
	good := f.addObject("welcome.jpg", "the picture of the lobby")
	// A name that tries to leave the object store. The store makes it safe.
	traversal := f.addObject("../../../etc/passwd", "some bytes")
	f.setManifest(manifest.Manifest{
		DefaultPlaylist: "lobby",
		Media: []manifest.MediaRef{
			good,
			traversal,
			// A hash that is a path step. store.ObjectName refuses it.
			{SHA256: "../../x", Size: 3, Name: "x.jpg", URL: "/api/v1/media/x"},
			// An address on another host. The device token must never go there.
			{SHA256: strings.Repeat("ef", 32), Size: 3, Name: "evil.jpg",
				URL: "http://evil.example.com/x"},
		},
		Playlists: []manifest.Playlist{{
			Name: "lobby",
			Items: []manifest.Item{
				{SHA256: "../../x", Duration: 5},
				{SHA256: strings.Repeat("ef", 32), Duration: 5},
				{SHA256: good.SHA256, Duration: 15},
				{SHA256: traversal.SHA256, Duration: 5},
			},
		}, {
			Name:  "../escape",
			Items: []manifest.Item{{SHA256: good.SHA256, Duration: 5}},
		}},
	})

	d := pairedDev(t, f)
	d.s.Once(context.Background())

	if err := d.s.Report().SyncError; err != "" {
		t.Fatalf("the round failed: %s", err)
	}
	// The two good items are the items that are left, and the bad playlist name is
	// gone.
	text := d.readFleetPlaylist("lobby")
	if strings.Count(text, "[[item]]") != 2 {
		t.Errorf("the playlist is:\n%s", text)
	}
	if got := filepath.Base(d.objectPath(traversal)); got != traversal.SHA256[:8]+"-passwd" {
		t.Errorf("the object of the path name is called %q", got)
	}
	if _, err := os.Stat(filepath.Join(d.media, "escape")); err == nil {
		t.Error("a playlist name of the server made a directory outside _fleet")
	}
	// Every file that the manifest made is under _fleet, and no path step of a name
	// made a directory of its own.
	for name := range treeTimes(t, d.media) {
		if !strings.HasPrefix(name, library.FleetDir+"/") {
			t.Errorf("the manifest wrote %s, which is outside %s", name, library.FleetDir)
		}
		if strings.Contains(name, "etc/") {
			t.Errorf("a name of the manifest made the path %s", name)
		}
	}
	// The object of the other host was never fetched.
	if hits := f.objectHits[strings.Repeat("ef", 32)]; hits != 0 {
		t.Errorf("the device asked for the object of the other host %d times", hits)
	}
}

// TestAFailedRenameLeavesTheOldSetWhole is the swap rule: a reader sees the old set
// or the new set and never a mixture. The test fails the rename that puts a staged
// directory in place, which is the step at which a process could die.
func TestAFailedRenameLeavesTheOldSetWhole(t *testing.T) {
	f := newFakeServer(t)
	ref := f.addObject("welcome.jpg", "the picture of the lobby")
	f.setManifest(lobbyManifest(ref))

	d := pairedDev(t, f)
	d.s.Once(context.Background())
	first := d.readFleetPlaylist("lobby")
	firstState := d.st.Fleet

	// A second playlist, and a rename that fails when a staged directory goes into
	// place.
	next := lobbyManifest(ref)
	next.Playlists = append(next.Playlists, manifest.Playlist{
		Name: "news", Items: []manifest.Item{{URL: "https://news.example.com", Duration: 30}},
	})
	f.setManifest(next)
	d.renameErr = func(oldPath, _ string) error {
		if strings.Contains(oldPath, stagingPrefix) {
			return os.ErrPermission
		}
		return nil
	}

	d.s.Once(context.Background())

	if got := d.readFleetPlaylist("lobby"); got != first {
		t.Errorf("the old playlist did not come back:\n%s", got)
	}
	if _, err := os.Stat(d.fleetPath("news")); err == nil {
		t.Error("the new playlist is there although the swap failed")
	}
	if d.st.Fleet != firstState {
		t.Error("the device recorded a manifest that it did not apply")
	}
	// No staging and no trash directory is left behind.
	entries, err := os.ReadDir(d.fleetPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagingPrefix) || strings.HasPrefix(e.Name(), trashPrefix) {
			t.Errorf("the failed swap left %s", e.Name())
		}
	}

	// The next poll with a working rename finishes the change.
	d.renameErr = nil
	d.s.Once(context.Background())
	if _, err := os.Stat(d.fleetPath("news", "playlist.toml")); err != nil {
		t.Errorf("the retry did not write the new playlist: %v", err)
	}
}

// TestAPlaylistThatTheManifestDroppedGoesAway keeps the card from growing a
// directory for every playlist that a screen ever had.
func TestAPlaylistThatTheManifestDroppedGoesAway(t *testing.T) {
	f := newFakeServer(t)
	ref := f.addObject("welcome.jpg", "the picture of the lobby")
	with := lobbyManifest(ref)
	with.Playlists = append(with.Playlists, manifest.Playlist{
		Name: "news", Items: []manifest.Item{{URL: "https://news.example.com", Duration: 30}},
	})
	f.setManifest(with)

	d := pairedDev(t, f)
	d.s.Once(context.Background())
	if _, err := os.Stat(d.fleetPath("news")); err != nil {
		t.Fatalf("the first round did not write the second playlist: %v", err)
	}

	f.setManifest(lobbyManifest(ref))
	d.s.Once(context.Background())
	if _, err := os.Stat(d.fleetPath("news")); err == nil {
		t.Error("the playlist that the manifest dropped stayed")
	}
	if _, err := os.Stat(d.fleetPath("lobby", "playlist.toml")); err != nil {
		t.Errorf("the playlist that stays went away: %v", err)
	}
}

// TestPollIntervalFollowsTheServerAndTheLimits covers the clamp of the poll
// interval.
func TestPollIntervalFollowsTheServerAndTheLimits(t *testing.T) {
	tests := []struct {
		name         string
		fromServer   int
		fromTOML     int
		wantInterval time.Duration
	}{
		{name: "the server value wins", fromServer: 120, fromTOML: 60, wantInterval: 120 * time.Second},
		{name: "the TOML value when the server sends none", fromTOML: 45, wantInterval: 45 * time.Second},
		{name: "under the floor", fromServer: 1, fromTOML: 60, wantInterval: MinPoll},
		{name: "over the ceiling", fromServer: 99999, fromTOML: 60, wantInterval: MaxPoll},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.setManifest(manifest.Manifest{PollSeconds: tt.fromServer})
			d := pairedDev(t, f)
			d.cfg.Server.PollSeconds = tt.fromTOML

			// Jitter is fixed at 0.5 in the fixture, which is no move at all.
			if got := d.s.Once(context.Background()); got != tt.wantInterval {
				t.Errorf("the interval is %v, want %v", got, tt.wantInterval)
			}
		})
	}
}

// TestJitterMovesThePollInterval proves that a fleet does not poll in lockstep.
func TestJitterMovesThePollInterval(t *testing.T) {
	f := newFakeServer(t)
	d := pairedDev(t, f)
	d.cfg.Server.PollSeconds = 100

	for _, tt := range []struct {
		jitter float64
		want   time.Duration
	}{
		{0, 90 * time.Second},
		{0.5, 100 * time.Second},
		{1, 110 * time.Second},
	} {
		d.s.opt.Jitter = func() float64 { return tt.jitter }
		if got := d.s.interval(); got != tt.want {
			t.Errorf("a jitter of %v gives %v, want %v", tt.jitter, got, tt.want)
		}
	}
}

// ------------------------------------------------------------------ helpers

// treeTimes gives the modification time of every file under a root, by its path
// relative to the root. A test that says "nothing was written" compares two of
// these maps.
func treeTimes(t *testing.T, root string) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = info.ModTime()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// hashEverything runs the background hash pass of the library until it has nothing
// left. The fleet client asks that cache before it downloads an object.
func hashEverything(t *testing.T, d *dev) {
	t.Helper()
	done := make(chan struct{})
	go d.lib.HashInBackground(done)
	defer close(done)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !d.lib.Snapshot().Hashing {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the library did not finish hashing the files of the test")
}
