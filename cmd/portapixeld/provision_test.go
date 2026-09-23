package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/playlist"
)

// The default content is videos (D37). provision must make a playlist item for
// each video, in name order, with no duration and with mute false, so each video
// plays for its full length. It must not copy the README.md that is beside them.
func TestInstallDefaultMediaVideos(t *testing.T) {
	root := t.TempDir()
	p := &paths{
		media:    filepath.Join(root, "media"),
		state:    filepath.Join(root, "state"),
		releases: filepath.Join(root, "releases"),
	}
	source := filepath.Join(p.releases, DefaultMediaDir)
	for _, dir := range []string{p.media, p.state, source} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The names are not in name order here, so the sort is tested too.
	names := []string{"05-mountain-road.mp4", "01-meadow.mp4", "03-forest.mp4", "README.md"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(source, n), []byte("data of "+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	log := opslog.New(filepath.Join(p.state, opsLogName))
	installed, err := installDefaultMedia(p, identity.Identity{}, log)
	if err != nil || !installed {
		t.Fatalf("installDefaultMedia = %v, %v; want true, nil", installed, err)
	}

	target := filepath.Join(p.media, DefaultPlaylistName)
	data, err := os.ReadFile(filepath.Join(target, playlist.FileName))
	if err != nil {
		t.Fatal(err)
	}
	pl, err := playlist.Parse(data, playlist.Options{})
	if err != nil {
		t.Fatalf("the playlist does not parse: %v\n%s", err, data)
	}
	want := []string{"01-meadow.mp4", "03-forest.mp4", "05-mountain-road.mp4"}
	if len(pl.Items) != len(want) {
		t.Fatalf("got %d items, want %d\n%s", len(pl.Items), len(want), data)
	}
	for i, it := range pl.Items {
		if it.File != want[i] {
			t.Errorf("item %d is %q, want %q", i, it.File, want[i])
		}
		if k := playlist.Kind(it); k != playlist.KindVideo {
			t.Errorf("item %d is %q, want %q", i, k, playlist.KindVideo)
		}
		if it.Duration != 0 || it.MaxDuration != 0 || it.Mute {
			t.Errorf("item %d = %+v, want no duration, no max_duration and mute false", i, it)
		}
		got, err := os.ReadFile(filepath.Join(target, it.File))
		if err != nil || string(got) != "data of "+it.File {
			t.Errorf("the copy of %s is wrong: %q, %v", it.File, got, err)
		}
	}
	if !strings.Contains(string(data), "mute = false") {
		t.Errorf("the playlist does not say mute = false:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(target, "README.md")); err == nil {
		t.Error("provision copied README.md into the playlist")
	}

	// A second run changes nothing: the media partition holds a playlist now.
	again, err := installDefaultMedia(p, identity.Identity{}, log)
	if err != nil || again {
		t.Fatalf("second run = %v, %v; want false, nil", again, err)
	}
}

// Each file in os/default-media, other than README.md, must be a file that the
// player can show. Else provision skips it with no word, and the image carries
// dead weight.
func TestRepoDefaultMediaIsPlayable(t *testing.T) {
	dir := filepath.Join("..", "..", "os", "default-media")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	taken, err := mediaFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(taken) == 0 {
		t.Fatal("os/default-media holds no media file")
	}
	ok := map[string]bool{}
	for _, n := range taken {
		ok[n] = true
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "README.md" {
			continue
		}
		if !ok[e.Name()] {
			t.Errorf("os/default-media/%s is not an image or a video", e.Name())
		}
	}
}
