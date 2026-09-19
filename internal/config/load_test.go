package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// dirs makes a media root and a state directory for one test.
func dirs(t *testing.T) (mediaRoot, stateDir string) {
	t.Helper()
	root := t.TempDir()
	mediaRoot = filepath.Join(root, "media")
	stateDir = filepath.Join(root, "state")
	for _, d := range []string{mediaRoot, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return mediaRoot, stateDir
}

func TestLoad(t *testing.T) {
	goodFile := string(Render(func() Config {
		c := Default()
		c.Device.Name = "Lobby"
		return c
	}()))
	otherFile := string(Render(func() Config {
		c := Default()
		c.Device.Name = "Shadow copy"
		return c
	}()))

	tests := []struct {
		name string
		// media and shadow hold the content of each file. A nil value means
		// that the file is not there.
		media  *string
		shadow *string

		wantName    string
		wantShadow  bool
		wantDefault bool
		wantWarning bool
	}{
		{
			name:     "a good media file",
			media:    &goodFile,
			wantName: "Lobby",
		},
		{
			name:        "a bad media file falls back to the shadow copy",
			media:       ptr("this is not toml ["),
			shadow:      &otherFile,
			wantName:    "Shadow copy",
			wantShadow:  true,
			wantWarning: true,
		},
		{
			name:        "a missing media file falls back to the shadow copy",
			shadow:      &otherFile,
			wantName:    "Shadow copy",
			wantShadow:  true,
			wantWarning: true,
		},
		{
			name:        "no file at all gives the defaults",
			wantName:    "PortaPixel",
			wantDefault: true,
			wantWarning: true,
		},
		{
			name:        "a bad media file and a bad shadow copy give the defaults",
			media:       ptr("bad ["),
			shadow:      ptr("also bad ["),
			wantName:    "PortaPixel",
			wantDefault: true,
			wantWarning: true,
		},
		{
			name:     "a good media file wins over the shadow copy",
			media:    &goodFile,
			shadow:   &otherFile,
			wantName: "Lobby",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mediaRoot, stateDir := dirs(t)
			if tt.media != nil {
				write(t, MediaPath(mediaRoot), *tt.media)
			}
			if tt.shadow != nil {
				write(t, ShadowPath(stateDir), *tt.shadow)
			}

			got := Load(mediaRoot, stateDir)
			if got.Config.Device.Name != tt.wantName {
				t.Errorf("name = %q, want %q", got.Config.Device.Name, tt.wantName)
			}
			if got.FromShadow != tt.wantShadow {
				t.Errorf("FromShadow = %v, want %v", got.FromShadow, tt.wantShadow)
			}
			if got.FromDefault != tt.wantDefault {
				t.Errorf("FromDefault = %v, want %v", got.FromDefault, tt.wantDefault)
			}
			if (got.Warning != "") != tt.wantWarning {
				t.Errorf("Warning = %q, want a warning: %v", got.Warning, tt.wantWarning)
			}
		})
	}
}

func TestLoadMirrorsTheGoodFile(t *testing.T) {
	mediaRoot, stateDir := dirs(t)
	data := string(Render(Default()))
	write(t, MediaPath(mediaRoot), data)

	if _, err := os.Stat(ShadowPath(stateDir)); err == nil {
		t.Fatal("the shadow copy must not exist yet")
	}
	Load(mediaRoot, stateDir)

	got, err := os.ReadFile(ShadowPath(stateDir))
	if err != nil {
		t.Fatalf("Load must write the shadow copy: %v", err)
	}
	if string(got) != data {
		t.Fatal("the shadow copy must hold the same bytes as the media file")
	}
}

func TestLoadWritesTheMirrorOnlyWhenItChanges(t *testing.T) {
	mediaRoot, stateDir := dirs(t)
	write(t, MediaPath(mediaRoot), string(Render(Default())))

	Load(mediaRoot, stateDir)
	first, err := os.Stat(ShadowPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}

	// Make the modification time old, so that a second write shows.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(ShadowPath(stateDir), old, old); err != nil {
		t.Fatal(err)
	}

	Load(mediaRoot, stateDir)
	second, err := os.Stat(ShadowPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if !second.ModTime().Equal(old) {
		t.Fatal("Load wrote the shadow copy again although the content is the same")
	}

	// A different file must reach the shadow copy.
	cfg := Default()
	cfg.Device.Name = "Lobby"
	write(t, MediaPath(mediaRoot), string(Render(cfg)))
	Load(mediaRoot, stateDir)
	got, err := os.ReadFile(ShadowPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == string(Render(Default())) {
		t.Fatal("the shadow copy must hold the new content")
	}
	_ = first
}

func TestLoadDoesNotMirrorABadFile(t *testing.T) {
	mediaRoot, stateDir := dirs(t)
	good := string(Render(Default()))
	write(t, ShadowPath(stateDir), good)
	write(t, MediaPath(mediaRoot), "bad [")

	Load(mediaRoot, stateDir)

	got, err := os.ReadFile(ShadowPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != good {
		t.Fatal("a bad media file must never replace the shadow copy")
	}
}

func TestSave(t *testing.T) {
	mediaRoot, stateDir := dirs(t)
	cfg := Default()
	cfg.Device.Name = "Lobby"
	cfg.Display.Rotation = 90

	if err := Save(mediaRoot, stateDir, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	back := Load(mediaRoot, stateDir)
	if back.Config.Device.Name != "Lobby" || back.Config.Display.Rotation != 90 {
		t.Fatalf("Load after Save gave %+v", back.Config)
	}
	if back.FromShadow || back.FromDefault || back.Warning != "" {
		t.Fatalf("Load after Save must read the media file: %+v", back)
	}

	media, err := os.ReadFile(MediaPath(mediaRoot))
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := os.ReadFile(ShadowPath(stateDir))
	if err != nil {
		t.Fatalf("Save must mirror the file: %v", err)
	}
	if string(media) != string(shadow) {
		t.Fatal("the two copies must hold the same bytes")
	}
}

func TestSaveRefusesABadConfiguration(t *testing.T) {
	mediaRoot, stateDir := dirs(t)
	good := string(Render(Default()))
	write(t, MediaPath(mediaRoot), good)

	cfg := Default()
	cfg.Display.Rotation = 45
	err := Save(mediaRoot, stateDir, cfg)
	if err == nil {
		t.Fatal("Save must refuse a bad configuration")
	}
	var errs Errors
	if !asErrors(err, &errs) || !hasField(errs, "display.rotation") {
		t.Fatalf("Save must give the field errors, got %v", err)
	}

	// The old file must still be there.
	got, err := os.ReadFile(MediaPath(mediaRoot))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != good {
		t.Fatal("a refused save must not change the file")
	}
}

func ptr(s string) *string { return &s }

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// asErrors reports if err is a list of field errors.
func asErrors(err error, out *Errors) bool {
	errs, ok := err.(Errors)
	if ok {
		*out = errs
	}
	return ok
}
