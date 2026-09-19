package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/playlist"
)

// fixture makes a media root, a state directory and a Library.
type fixture struct {
	media string
	state string
	lib   *Library
	log   *opslog.Log
	// paired is what the Paired function reports. A test sets it.
	paired bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{media: t.TempDir(), state: t.TempDir()}
	f.log = opslog.New(filepath.Join(f.state, "ops.log"))
	f.lib = New(Options{
		MediaRoot: f.media,
		StateDir:  f.state,
		Log:       f.log,
		Paired:    func() bool { return f.paired },
	})
	return f
}

// dir makes a playlist directory with a playlist.toml and the named files.
func (f *fixture) dir(t *testing.T, name, toml string, files ...string) {
	t.Helper()
	dir := filepath.Join(f.media, filepath.FromSlash(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if toml != "" {
		if err := os.WriteFile(filepath.Join(dir, playlist.FileName), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("content of "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fixture) events(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, e := range f.log.Tail(100) {
		b.WriteString(e.Event + " " + e.Details + "\n")
	}
	return b.String()
}

const goodTOML = `
[playlist]
name = "Lobby loop"
[[item]]
file = "a.jpg"
duration = 15
[[item]]
file = "b.mp4"
mute = true
`

func TestScanGoodPlaylist(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "default", goodTOML, "a.jpg", "b.mp4")

	snap := f.lib.Rescan()
	if len(snap.Playlists) != 1 {
		t.Fatalf("playlists = %d, want 1: %+v", len(snap.Playlists), snap)
	}
	p := snap.Playlists[0]
	if p.Name != "default" || p.Title != "Lobby loop" || p.Fleet || p.Kiosk {
		t.Fatalf("playlist = %+v", p)
	}
	if len(p.Items) != 2 {
		t.Fatalf("items = %d", len(p.Items))
	}
	if p.Items[0].Kind != playlist.KindImage || p.Items[0].Src != "/media/default/a.jpg" {
		t.Errorf("item 0 = %+v", p.Items[0])
	}
	if p.Items[1].Kind != playlist.KindVideo || !p.Items[1].Mute {
		t.Errorf("item 1 = %+v", p.Items[1])
	}
	if p.Items[0].Size == 0 || p.Items[0].Missing {
		t.Errorf("item 0 has no size or is missing: %+v", p.Items[0])
	}
	if len(snap.Problems) != 0 {
		t.Errorf("problems = %+v", snap.Problems)
	}
}

// A bad playlist file must never crash the scan. Each case gives one problem and
// one ops log line, and the good playlist beside it still plays.
func TestScanMalformedPlaylists(t *testing.T) {
	tests := []struct {
		name string
		toml string
		want string // text that the problem message must hold
	}{
		{"broken toml", "[playlist\nname = ", "skipped"},
		{"item with a file and a url", "[[item]]\nfile = \"a.jpg\"\nurl = \"http://x/\"\n", "use one of them"},
		{"item with nothing", "[[item]]\nduration = 5\n", "needs a file or a url"},
		{"path step", "[[item]]\nfile = \"../secret.jpg\"\n", "outside the playlist directory"},
		{"absolute path", "[[item]]\nfile = \"/etc/shadow\"\n", "relative path"},
		{"bad transition", "[playlist]\ntransition = \"spin\"\n[[item]]\nfile = \"a.jpg\"\n", "transition"},
		{"url without a scheme", "[[item]]\nurl = \"dash.example.com\"\nduration = 5\n[[item]]\nfile = \"a.jpg\"\n", "http://"},
		{"negative duration", "[[item]]\nfile = \"a.jpg\"\nduration = -3\n", "less than zero"},
		{"wrong type", "[[item]]\nfile = 7\n", "skipped"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.dir(t, "bad", tt.toml, "a.jpg")
			f.dir(t, "good", goodTOML, "a.jpg", "b.mp4")

			snap := f.lib.Rescan()
			if len(snap.Playlists) != 1 || snap.Playlists[0].Name != "good" {
				t.Fatalf("the good playlist did not survive: %+v", snap.Playlists)
			}
			if len(snap.Problems) != 1 {
				t.Fatalf("problems = %+v", snap.Problems)
			}
			if snap.Problems[0].Playlist != "bad" {
				t.Errorf("problem names %q", snap.Problems[0].Playlist)
			}
			if !strings.Contains(snap.Problems[0].Message, tt.want) {
				t.Errorf("message %q does not hold %q", snap.Problems[0].Message, tt.want)
			}
			if !strings.Contains(f.events(t), "library.playlist.skipped") {
				t.Errorf("no ops log line:\n%s", f.events(t))
			}
		})
	}
}

func TestScanMissingFileIsFlaggedNotFatal(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "default", goodTOML, "a.jpg") // b.mp4 is not there

	snap := f.lib.Rescan()
	p := snap.Playlists[0]
	if p.Items[1].Missing != true || p.Items[1].Warning == "" {
		t.Fatalf("item 1 = %+v", p.Items[1])
	}
	if len(snap.Problems) != 0 {
		t.Errorf("a missing file is an item warning, not a playlist problem: %+v", snap.Problems)
	}
}

func TestScanUnknownKind(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "default", "[[item]]\nfile = \"notes.txt\"\n", "notes.txt")
	snap := f.lib.Rescan()
	it := snap.Playlists[0].Items[0]
	if it.Kind != playlist.KindUnknown || it.Warning == "" {
		t.Fatalf("item = %+v", it)
	}
}

func TestScanSkipsUnderscoreAndScratchDirectories(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "_update", goodTOML, "a.jpg", "b.mp4")
	f.dir(t, "_private", goodTOML, "a.jpg", "b.mp4")
	f.dir(t, "scratch", "", "loose.jpg") // no playlist.toml
	f.dir(t, "default", goodTOML, "a.jpg", "b.mp4")

	snap := f.lib.Rescan()
	if len(snap.Playlists) != 1 || snap.Playlists[0].Name != "default" {
		t.Fatalf("playlists = %+v", snap.Playlists)
	}
	if len(snap.Problems) != 0 {
		t.Errorf("a directory with no playlist.toml is not a fault: %+v", snap.Problems)
	}
}

func TestFleetPlaylistsNeedPairing(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "_fleet/media", "", "aabbccdd-clip.mp4")
	f.dir(t, "_fleet/lobby", "[playlist]\nname = \"Fleet lobby\"\n[[item]]\nfile = \"../media/aabbccdd-clip.mp4\"\n")

	if snap := f.lib.Rescan(); len(snap.Playlists) != 0 {
		t.Fatalf("an unpaired device showed a fleet playlist: %+v", snap.Playlists)
	}

	f.paired = true
	snap := f.lib.Rescan()
	if len(snap.Playlists) != 1 {
		t.Fatalf("playlists = %+v", snap.Playlists)
	}
	p := snap.Playlists[0]
	if !p.Fleet || p.Name != "lobby" {
		t.Fatalf("playlist = %+v", p)
	}
	if p.Items[0].Missing {
		t.Fatalf("the fleet object was not found: %+v", p.Items[0])
	}
	if p.Items[0].Src != "/media/_fleet/media/aabbccdd-clip.mp4" {
		t.Fatalf("src = %q", p.Items[0].Src)
	}
	if len(snap.Problems) != 0 {
		t.Errorf("problems = %+v", snap.Problems)
	}
}

func TestFleetRefIsRefusedInALocalPlaylist(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "local", "[[item]]\nfile = \"../media/aabbccdd-clip.mp4\"\n")
	snap := f.lib.Rescan()
	if len(snap.Playlists) != 0 || len(snap.Problems) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestKioskPlaylist(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "board", "[[item]]\nurl = \"https://dash.example.com/board\"\n")
	snap := f.lib.Rescan()
	if !snap.Playlists[0].Kiosk {
		t.Fatalf("a playlist of one URL item is not kiosk: %+v", snap.Playlists[0])
	}
}

func TestEmptyPlaylistIsNotAFault(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "new", "[playlist]\nname = \"New\"\n")
	snap := f.lib.Rescan()
	if len(snap.Playlists) != 1 || len(snap.Playlists[0].Items) != 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap.Problems) != 0 {
		t.Errorf("problems = %+v", snap.Problems)
	}
}

func TestSrcURLEscapesTheName(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "default", "[[item]]\nfile = \"a file &.jpg\"\n", "a file &.jpg")
	snap := f.lib.Rescan()
	if got := snap.Playlists[0].Items[0].Src; got != "/media/default/a%20file%20%26.jpg" {
		t.Fatalf("src = %q", got)
	}
}

func TestHashCache(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "default", goodTOML, "a.jpg", "b.mp4")
	f.lib.Rescan()

	if snap := f.lib.Snapshot(); !snap.Hashing {
		t.Fatalf("the snapshot says that there is nothing to hash")
	}

	// Run the background work to the end.
	done := make(chan struct{})
	close(done)
	f.lib.hashPending(done) // the closed channel stops it after the first pause
	f.lib.hashPending(nil)  // nil never fires, so this pass hashes everything

	snap := f.lib.Snapshot()
	for _, it := range snap.Playlists[0].Items {
		if it.SHA256 == "" || len(it.SHA256) != 64 {
			t.Fatalf("item %q has no hash: %+v", it.Name, it)
		}
	}
	if snap.Hashing {
		t.Errorf("the snapshot still reports work")
	}
	if _, err := os.Stat(filepath.Join(f.state, CacheName)); err != nil {
		t.Fatalf("the cache file is missing: %v", err)
	}

	// A second Library must read the cache and need no work.
	again := New(Options{MediaRoot: f.media, StateDir: f.state, Log: f.log})
	if snap := again.Rescan(); snap.Hashing {
		t.Errorf("the new Library did not use the cache")
	}

	// A changed file must lose its cached hash.
	target := filepath.Join(f.media, "default", "a.jpg")
	if err := os.WriteFile(target, []byte("something else entirely"), 0o644); err != nil {
		t.Fatal(err)
	}
	if snap := again.Rescan(); !snap.Hashing {
		t.Errorf("the cache kept the hash of a changed file")
	}
}

func TestHashCacheDropsDeadEntries(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "default", goodTOML, "a.jpg", "b.mp4")
	f.lib.Rescan()
	f.lib.hashPending(nil)

	if err := os.RemoveAll(filepath.Join(f.media, "default")); err != nil {
		t.Fatal(err)
	}
	f.lib.Rescan()
	f.lib.hashPending(nil)
	if n := len(f.lib.cache.entries); n != 0 {
		t.Fatalf("the cache kept %d dead entries", n)
	}
}

func TestCRUD(t *testing.T) {
	f := newFixture(t)

	name, err := f.lib.CreatePlaylist("Lobby Loop")
	if err != nil {
		t.Fatal(err)
	}
	if name != "lobby-loop" {
		t.Fatalf("name = %q", name)
	}
	if _, err := f.lib.CreatePlaylist("Lobby Loop"); !errors.Is(err, ErrExists) {
		t.Fatalf("a second create gave %v", err)
	}
	if _, err := f.lib.CreatePlaylist("***"); !errors.Is(err, ErrBadName) {
		t.Fatalf("a bad name gave %v", err)
	}

	// Add a file and then save the playlist that names it.
	file, size, err := f.lib.AddMedia(name, "My Photo!.JPG", strings.NewReader("jpeg data"))
	if err != nil {
		t.Fatal(err)
	}
	if file != "My-Photo.JPG" || size != 9 {
		t.Fatalf("file = %q size = %d", file, size)
	}
	// The same name again must not replace the file.
	again, _, err := f.lib.AddMedia(name, "My Photo!.JPG", strings.NewReader("other data"))
	if err != nil {
		t.Fatal(err)
	}
	if again == file {
		t.Fatalf("the second upload replaced %q", file)
	}

	p := playlist.Playlist{
		Meta:  playlist.Meta{Name: "Lobby Loop"},
		Items: []playlist.Item{{File: file, Duration: 12}},
	}
	if err := f.lib.SavePlaylist(name, p); err != nil {
		t.Fatal(err)
	}
	snap := f.lib.Snapshot()
	got, ok := snap.Find(name)
	if !ok || len(got.Items) != 1 || got.Items[0].Duration != 12 || got.Items[0].Missing {
		t.Fatalf("playlist = %+v", got)
	}

	// A playlist that breaks a rule gives field errors, not a write.
	bad := playlist.Playlist{Items: []playlist.Item{{File: "../x.jpg"}}}
	var errs playlist.Errors
	if err := f.lib.SavePlaylist(name, bad); !errors.As(err, &errs) {
		t.Fatalf("a bad playlist gave %v", err)
	}

	// Rename.
	next, err := f.lib.RenamePlaylist(name, "Front Desk")
	if err != nil {
		t.Fatal(err)
	}
	if next != "front-desk" {
		t.Fatalf("new name = %q", next)
	}
	if _, ok := f.lib.Snapshot().Find(next); !ok {
		t.Fatalf("the renamed playlist is not in the snapshot")
	}

	// Delete the media file, then the playlist.
	if err := f.lib.DeleteMedia(next, file); err != nil {
		t.Fatal(err)
	}
	if err := f.lib.DeleteMedia(next, file); !errors.Is(err, ErrNoFile) {
		t.Fatalf("a second delete gave %v", err)
	}
	if err := f.lib.DeletePlaylist(next); err != nil {
		t.Fatal(err)
	}
	if len(f.lib.Snapshot().Playlists) != 0 {
		t.Fatalf("the playlist is still there")
	}
}

func TestCRUDRefusesPathTraversal(t *testing.T) {
	f := newFixture(t)
	if _, err := f.lib.CreatePlaylist("ok"); err != nil {
		t.Fatal(err)
	}
	// A name with a path step is not a slug, so it cannot name a playlist.
	for _, name := range []string{"../etc", "ok/..", ".", "..", "_fleet", "OK"} {
		if _, _, err := f.lib.AddMedia(name, "x.jpg", strings.NewReader("x")); err == nil {
			t.Errorf("AddMedia accepted the playlist name %q", name)
		}
	}
	for _, file := range []string{"../../etc/shadow", "sub/x.jpg", `..\x.jpg`, ""} {
		if err := f.lib.DeleteMedia("ok", file); !errors.Is(err, ErrBadName) && !errors.Is(err, ErrNoFile) {
			t.Errorf("DeleteMedia(%q) gave %v", file, err)
		}
	}
	// A file that the upload wrote must stay inside the playlist directory.
	name, _, err := f.lib.AddMedia("ok", "../../evil.jpg", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(name, `/\`) {
		t.Fatalf("the upload name holds a separator: %q", name)
	}
	if _, err := os.Stat(filepath.Join(f.media, "ok", name)); err != nil {
		t.Fatalf("the file is not in the playlist directory: %v", err)
	}
}

func TestCRUDRefusesFleetPlaylists(t *testing.T) {
	f := newFixture(t)
	f.paired = true
	f.dir(t, "_fleet/media", "", "aabbccdd-clip.mp4")
	f.dir(t, "_fleet/lobby", "[[item]]\nfile = \"../media/aabbccdd-clip.mp4\"\n")
	f.lib.Rescan()

	if err := f.lib.DeletePlaylist("lobby"); !errors.Is(err, ErrManaged) {
		t.Fatalf("DeletePlaylist gave %v, want ErrManaged", err)
	}
	if err := f.lib.SavePlaylist("lobby", playlist.Playlist{}); !errors.Is(err, ErrManaged) {
		t.Fatalf("SavePlaylist gave %v, want ErrManaged", err)
	}
}

func TestCRUDNotFound(t *testing.T) {
	f := newFixture(t)
	if err := f.lib.DeletePlaylist("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestBuildManifest(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "default", `
[playlist]
name = "Lobby loop"
transition = "cut"
[[item]]
file = "a.jpg"
[[item]]
file = "b.mp4"
max_duration = 60
[[item]]
file = "gone.jpg"
[[item]]
file = "notes.txt"
[[item]]
url = "https://dash.example.com/board"
duration = 60
refresh_seconds = 300
`, "a.jpg", "b.mp4", "notes.txt")
	snap := f.lib.Rescan()
	p, _ := snap.Find("default")

	cfg := config.Default()
	m := BuildManifest(&p, cfg, "high", 1)
	if m.Fallback || m.Playlist == nil {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Playlist.Transition != "cut" || m.Playlist.TransitionMS != 500 {
		t.Errorf("transition = %q %d", m.Playlist.Transition, m.Playlist.TransitionMS)
	}
	if len(m.Playlist.Items) != 3 {
		t.Fatalf("items = %+v", m.Playlist.Items)
	}
	if m.Playlist.Items[0].Duration != cfg.Playback.ImageDuration {
		t.Errorf("the image did not get the default duration: %+v", m.Playlist.Items[0])
	}
	if m.Playlist.Items[2].Kind != "url" || m.Playlist.Items[2].RefreshSeconds != 300 {
		t.Errorf("url item = %+v", m.Playlist.Items[2])
	}
	for i, it := range m.Playlist.Items {
		if it.Index != i {
			t.Errorf("item %d has index %d", i, it.Index)
		}
	}
}

func TestBuildManifestFallback(t *testing.T) {
	cfg := config.Default()
	if m := BuildManifest(nil, cfg, "low", 1); !m.Fallback || m.Playlist != nil {
		t.Fatalf("a nil playlist gave %+v", m)
	}
	// A playlist whose files are all missing has nothing to show.
	p := Playlist{Name: "x", Items: []Item{{Kind: "image", Missing: true}}}
	if m := BuildManifest(&p, cfg, "low", 1); !m.Fallback {
		t.Fatalf("a playlist of missing files is not fallback")
	}
}

func TestBuildManifestShuffle(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "default", `
[playlist]
shuffle = true
[[item]]
file = "a.jpg"
[[item]]
file = "b.jpg"
[[item]]
file = "c.jpg"
[[item]]
file = "d.jpg"
[[item]]
file = "e.jpg"
`, "a.jpg", "b.jpg", "c.jpg", "d.jpg", "e.jpg")
	p, _ := f.lib.Rescan().Find("default")
	cfg := config.Default()

	first := names(BuildManifest(&p, cfg, "high", 7))
	if same := names(BuildManifest(&p, cfg, "high", 7)); first != same {
		t.Fatalf("the same seed gave two orders: %q and %q", first, same)
	}
	// Some other seed must give another order. One of ten seeds is enough: five
	// items have 120 orders.
	different := false
	for seed := uint64(1); seed < 10 && !different; seed++ {
		if names(BuildManifest(&p, cfg, "high", seed)) != first {
			different = true
		}
	}
	if !different {
		t.Fatalf("every seed gave the order %q", first)
	}
}

func names(m PlayerManifest) string {
	var b strings.Builder
	for _, it := range m.Playlist.Items {
		b.WriteString(it.Name + " ")
	}
	return b.String()
}
