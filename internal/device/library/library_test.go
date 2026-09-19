package library

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

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
	file, size, err := f.lib.AddMedia(name, "My Photo!.JPG", strings.NewReader("jpeg data"), -1)
	if err != nil {
		t.Fatal(err)
	}
	if file != "My-Photo.JPG" || size != 9 {
		t.Fatalf("file = %q size = %d", file, size)
	}
	// The same name again must not replace the file.
	again, _, err := f.lib.AddMedia(name, "My Photo!.JPG", strings.NewReader("other data"), -1)
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
		if _, _, err := f.lib.AddMedia(name, "x.jpg", strings.NewReader("x"), -1); err == nil {
			t.Errorf("AddMedia accepted the playlist name %q", name)
		}
	}
	for _, file := range []string{"../../etc/shadow", "sub/x.jpg", `..\x.jpg`, ""} {
		if err := f.lib.DeleteMedia("ok", file); !errors.Is(err, ErrBadName) && !errors.Is(err, ErrNoFile) {
			t.Errorf("DeleteMedia(%q) gave %v", file, err)
		}
	}
	// A file that the upload wrote must stay inside the playlist directory.
	name, _, err := f.lib.AddMedia("ok", "../../evil.jpg", strings.NewReader("x"), -1)
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

// Two uploads of one file name arrive together when a person clicks twice or a
// request is retried. They shared one staging file before, so the two streams
// went into one file and both calls reported success on one file of mixed bytes.
func TestAddMediaTwoUploadsOfOneName(t *testing.T) {
	f := newFixture(t)
	if _, err := f.lib.CreatePlaylist("lobby"); err != nil {
		t.Fatal(err)
	}

	const size = 64 << 10
	var wg sync.WaitGroup
	names := make([]string, 2)
	errs := make([]error, 2)
	for i, text := range []string{strings.Repeat("a", size), strings.Repeat("b", size)} {
		wg.Add(1)
		go func(i int, text string) {
			defer wg.Done()
			names[i], _, errs[i] = f.lib.AddMedia("lobby", "clip.mp4", slowReader(text), int64(len(text)))
		}(i, text)
	}
	wg.Wait()

	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("upload %d: %v", i, errs[i])
		}
	}
	if names[0] == names[1] {
		t.Fatalf("both uploads took the name %q", names[0])
	}
	// Each file must hold one stream and not a mix of the two. Which upload took
	// which name is a race, and either answer is right.
	for _, name := range names {
		if name != "clip.mp4" && name != "clip-2.mp4" {
			t.Fatalf("the upload took the name %q", name)
		}
		got, err := os.ReadFile(filepath.Join(f.media, "lobby", name))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != size {
			t.Fatalf("%s holds %d bytes, want %d", name, len(got), size)
		}
		want := strings.Repeat(string(got[0]), size)
		if string(got) != want {
			t.Fatalf("%s holds bytes of both uploads", name)
		}
	}
	// No staging file is left behind.
	entries, err := os.ReadDir(filepath.Join(f.media, "lobby"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".upload-") {
			t.Errorf("a staging file is left: %s", e.Name())
		}
	}
}

// slowReader hands out small blocks, so that two uploads that run together really
// do run together.
func slowReader(text string) io.Reader {
	return iotest.OneByteReader(strings.NewReader(text))
}

// An upload that is larger than the free space must be refused. One upload that
// fills PPMEDIA is a device that cannot save its configuration, its playlists or
// its ops log.
func TestAddMediaRefusesAFileThatDoesNotFit(t *testing.T) {
	f := newFixture(t)
	if _, err := f.lib.CreatePlaylist("lobby"); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.lib.AddMedia("lobby", "huge.mp4", strings.NewReader("x"), 1<<60)
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("a file of 1 EB gave %v, want ErrNoSpace", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.media, "lobby")); len(entries) != 1 {
		t.Fatalf("the refused upload left files: %v", entries)
	}
}

// freeName gave the last name it tried when every name was taken, and that name
// belonged to a file that exists: the upload wrote over a file that a playlist
// item names, and the API answered a third name.
func TestFreeNameRefusesWhenEveryNameIsTaken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	for n := 2; n < maxNameTries; n++ {
		name := fmt.Sprintf("clip-%d.mp4", n)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target, name, err := freeName(dir, "clip.mp4", nil)
	if err == nil {
		t.Fatalf("freeName gave %q/%q and no error", target, name)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "clip.mp4")); err != nil || string(got) != "first" {
		t.Errorf("clip.mp4 = %q, %v", got, err)
	}
}

// A playlist.toml that does not parse is the file that the user repairs. A rename
// moved the directory and then wrote an empty placeholder over it, so every item
// and every byte that the user could have repaired was gone.
func TestRenameKeepsABrokenPlaylistFile(t *testing.T) {
	f := newFixture(t)
	const broken = "[[item]\nfile = \"a.jpg\"\nthis line is not TOML\n"
	f.dir(t, "lobby", broken, "a.jpg")
	f.lib.Rescan()

	next, err := f.lib.RenamePlaylist("lobby", "Front Desk")
	if err != nil {
		t.Fatal(err)
	}
	if next != "front-desk" {
		t.Fatalf("new name = %q", next)
	}
	got, err := os.ReadFile(filepath.Join(f.media, next, playlist.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != broken {
		t.Fatalf("the rename rewrote a playlist.toml that it could not read:\n%s", got)
	}
}

// The daemon must correct the schedule rules after a rename, so the library tells
// it which names changed.
func TestRenameTellsTheDaemon(t *testing.T) {
	f := newFixture(t)
	var seen [][2]string
	f.lib = New(Options{
		MediaRoot: f.media,
		StateDir:  f.state,
		Log:       f.log,
		OnRename:  func(old, next string) { seen = append(seen, [2]string{old, next}) },
	})
	f.dir(t, "lobby", "[playlist]\nname = \"Lobby\"\n[[item]]\nfile = \"a.jpg\"\n", "a.jpg")
	f.lib.Rescan()

	if _, err := f.lib.RenamePlaylist("lobby", "Front Desk"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != [2]string{"lobby", "front-desk"} {
		t.Fatalf("OnRename saw %v", seen)
	}
	// A rename to the same name changes no reference.
	if _, err := f.lib.RenamePlaylist("front-desk", "front-desk"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("a rename to the same name told the daemon: %v", seen)
	}
}

// A burst of uploads must cost one message to the player. Each message takes the
// player back to item 0, so twenty uploads showed the first item twenty times.
func TestChangesAreCoalescedIntoOneEvent(t *testing.T) {
	f := newFixture(t)
	var events atomic.Int64
	f.lib = New(Options{
		MediaRoot: f.media,
		StateDir:  f.state,
		Log:       f.log,
		// Long enough that a busy machine cannot put a gap in the burst.
		ChangeDelay: time.Second,
		OnChange:    func() { events.Add(1) },
	})
	if _, err := f.lib.CreatePlaylist("lobby"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("clip-%d.mp4", i)
		if _, _, err := f.lib.AddMedia("lobby", name, strings.NewReader("x"), 1); err != nil {
			t.Fatal(err)
		}
		// Every upload must be in the snapshot at once: the API answer must be
		// the truth, whatever the message to the player does.
		if _, ok := f.lib.Snapshot().Find("lobby"); !ok {
			t.Fatalf("the playlist is not in the snapshot after upload %d", i)
		}
	}
	time.Sleep(1600 * time.Millisecond)
	if got := events.Load(); got != 1 {
		t.Fatalf("21 changes made %d player events, want one", got)
	}
}

// A scan that could not read the media root says nothing about which files are
// gone. Pruning against it threw the whole cache away, and the device then hashed
// the full card again at 4 files a second.
func TestHashCacheKeepsEntriesAfterABadScan(t *testing.T) {
	f := newFixture(t)
	f.dir(t, "default", goodTOML, "a.jpg", "b.mp4")
	f.lib.Rescan()
	f.lib.hashPending(nil)
	if n := len(f.lib.cache.entries); n != 2 {
		t.Fatalf("the cache holds %d entries after the first pass", n)
	}

	// The card is not mounted any more.
	f.lib.snap = Snapshot{Problems: []Problem{{Message: "Cannot read the media directory: no such file"}}}
	f.lib.hashPending(nil)
	if n := len(f.lib.cache.entries); n != 2 {
		t.Fatalf("a scan that could not read the card dropped the cache to %d entries", n)
	}
	if !strings.Contains(f.events(t), "library.cache.keep") {
		t.Errorf("no library.cache.keep line:\n%s", f.events(t))
	}
}

// An unpaired device does not read the _fleet tree at all. Its objects are not in
// the live set, and they must keep their hashes for the next pairing (D24).
func TestHashCacheKeepsFleetEntriesWhileUnpaired(t *testing.T) {
	f := newFixture(t)
	f.paired = true
	f.dir(t, "_fleet/media", "", "aabbccdd-clip.mp4")
	f.dir(t, "_fleet/lobby", "[[item]]\nfile = \"../media/aabbccdd-clip.mp4\"\n")
	f.dir(t, "default", goodTOML, "a.jpg", "b.mp4")
	f.lib.Rescan()
	f.lib.hashPending(nil)
	if n := len(f.lib.cache.entries); n != 3 {
		t.Fatalf("the cache holds %d entries, want 3", n)
	}

	f.paired = false
	f.lib.Rescan()
	f.lib.hashPending(nil)
	if _, ok := f.lib.cache.entries["_fleet/media/aabbccdd-clip.mp4"]; !ok {
		t.Fatalf("the unpaired scan dropped the fleet object: %v", f.lib.cache.entries)
	}
}

// A write that fails must not clear the dirty flag. One failed write made the
// cache unsaveable for the life of the daemon, and the device hashed the whole
// card at every boot with nothing to show for it.
func TestHashCacheKeepsDirtyAfterAFailedWrite(t *testing.T) {
	f := newFixture(t)
	c := f.lib.cache
	// A path inside a file is a path that no write can use.
	blocker := filepath.Join(f.state, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.path = filepath.Join(blocker, CacheName)

	c.put("default/a.jpg", 10, 20, strings.Repeat("a", 64))
	if err := c.save(); err == nil {
		t.Fatal("save gave no error for a path that cannot be written")
	}
	if c.version == c.saved {
		t.Fatal("a failed write counted as saved; the cache can never be saved again")
	}

	// The path works again, so the entry is saved.
	c.path = filepath.Join(f.state, CacheName)
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	if c.version != c.saved {
		t.Fatal("a write that worked did not count as saved")
	}
	again := loadCache(f.state)
	if _, ok := again.entries["default/a.jpg"]; !ok {
		t.Fatalf("the entry was not saved: %v", again.entries)
	}
}

// A hash must stop when the daemon stops. A 1 GB video on a Pi takes about a
// minute, and a shutdown that waits for it is a shutdown that the system kills
// before the cache is written.
func TestHashFileStopsWhenTheDaemonStops(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.mp4")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	if _, _, _, err := hashFile(path, done); !errors.Is(err, errStopping) {
		t.Fatalf("hashFile on a closed done gave %v, want errStopping", err)
	}
	// An open done hashes the whole file.
	sha, size, _, err := hashFile(path, make(chan struct{}))
	if err != nil || len(sha) != 64 || size != 1<<20 {
		t.Fatalf("hashFile = %q, %d, %v", sha, size, err)
	}
}
