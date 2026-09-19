package config

import (
	"os"
	"path/filepath"
	"strings"
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
		// wantRepaired holds the fields that Load must report as repaired.
		wantRepaired []string
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
		{
			// The file parses, so only Validate finds the fault. Load keeps the
			// name that the person wrote and repairs the one bad field (D38).
			name:         "a media file with a bad value is repaired, not dropped",
			media:        ptr(strings.Replace(goodFile, "rotation = 0", "rotation = 45", 1)),
			shadow:       &otherFile,
			wantName:     "Lobby",
			wantWarning:  true,
			wantRepaired: []string{"display.rotation"},
		},
		{
			name:         "an empty password takes the default password",
			media:        ptr(strings.Replace(goodFile, `password = "portapixel"`, `password = ""`, 1)),
			wantName:     "Lobby",
			wantWarning:  true,
			wantRepaired: []string{"web.password"},
		},
		{
			name:         "a repaired shadow copy still beats the defaults",
			media:        ptr("this is not toml ["),
			shadow:       ptr(strings.Replace(otherFile, "volume = 100", "volume = 900", 1)),
			wantName:     "Shadow copy",
			wantShadow:   true,
			wantWarning:  true,
			wantRepaired: []string{"audio.volume"},
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
			if len(got.Repaired) != len(tt.wantRepaired) {
				t.Fatalf("Repaired = %v, want %v", got.Repaired, tt.wantRepaired)
			}
			for _, field := range tt.wantRepaired {
				if !hasField(got.Repaired, field) {
					t.Errorf("Repaired = %v, want a fault for %q", got.Repaired, field)
				}
				if !strings.Contains(got.Warning, field) {
					t.Errorf("the warning does not name %q: %s", field, got.Warning)
				}
			}
			// A repaired configuration must always be one that the daemon can run.
			if errs := got.Config.Validate(); len(errs) > 0 {
				t.Errorf("Load gave a configuration that breaks a rule: %v", errs)
			}
		})
	}
}

// TestLoadRepairsInsteadOfDropping covers the first boot of a stick that a
// person wrote by hand and that has one typed character wrong. There is no
// shadow copy yet, so a file that Load threw away would give the device the
// published default password and lose the WiFi key.
func TestLoadRepairsInsteadOfDropping(t *testing.T) {
	mediaRoot, stateDir := dirs(t)

	cfg := Default()
	cfg.Device.Name = "Lobby"
	cfg.Network.WifiSSID = "Guest"
	cfg.Network.WifiPSK = "a wifi secret"
	cfg.Web.Password = "letmein"
	cfg.Schedule = []Rule{{Playlist: "day", Start: "08:00", End: "18:00"}}
	file := strings.Replace(string(Render(cfg)), "rotation = 0", "rotation = 45", 1)
	write(t, MediaPath(mediaRoot), file)

	got := Load(mediaRoot, stateDir)

	if got.FromDefault || got.FromShadow {
		t.Fatalf("Load threw the file away: %+v", got)
	}
	if len(got.Repaired) != 1 || got.Repaired[0].Field != "display.rotation" {
		t.Fatalf("Repaired = %v, want display.rotation", got.Repaired)
	}
	if got.Config.Display.Rotation != 0 {
		t.Errorf("rotation = %d, want the default", got.Config.Display.Rotation)
	}
	if got.Config.Web.Password != "letmein" {
		t.Errorf("password = %q: the device must not fall back to the published default", got.Config.Web.Password)
	}
	if got.Config.Network.WifiPSK != "a wifi secret" || got.Config.Network.WifiSSID != "Guest" {
		t.Errorf("Load lost the wifi settings: %+v", got.Config.Network)
	}
	if got.Config.Device.Name != "Lobby" || len(got.Config.Schedule) != 1 {
		t.Errorf("Load lost a value that the person wrote: %+v", got.Config)
	}
}

// TestLoadNeverWritesToTheMediaRoot holds the rule that the media partition is
// the property of the person. Load reads it and writes nothing there, whatever
// it finds.
func TestLoadNeverWritesToTheMediaRoot(t *testing.T) {
	files := map[string]string{
		"a good file":    string(Render(Default())),
		"a bad value":    strings.Replace(string(Render(Default())), "rotation = 0", "rotation = 45", 1),
		"a file of junk": "this is not toml [",
		"an empty file":  "",
	}
	for name, content := range files {
		t.Run(name, func(t *testing.T) {
			mediaRoot, stateDir := dirs(t)
			write(t, MediaPath(mediaRoot), content)

			Load(mediaRoot, stateDir)

			after, err := os.ReadFile(MediaPath(mediaRoot))
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != content {
				t.Fatalf("Load changed the file on the media root:\n%s", after)
			}
			names, err := filepath.Glob(filepath.Join(mediaRoot, "*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(names) != 1 {
				t.Fatalf("Load left %v in the media root, want only the configuration file", names)
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
	good := string(Render(Default()))
	tests := []struct {
		name  string
		media string
		// wantShadow is true when Load must fall back to the shadow copy. A file
		// that parses is repaired instead, and the repaired values are the ones
		// that run.
		wantShadow bool
	}{
		{name: "the file is not toml", media: "bad [", wantShadow: true},
		{
			// The last-known-good copy must hold a file that is good in both
			// ways: it parses and every value in it is permitted. A repaired file
			// is not that file.
			name:  "the file holds a bad value",
			media: strings.Replace(good, "rotation = 0", "rotation = 45", 1),
		},
		{
			name:  "the file holds a network value that would inject a directive",
			media: strings.Replace(good, `mode = "dhcp"`, "mode = \"static\"\naddress = \"192.168.1.50/24\\n\\tup /bin/sh -c id\"", 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mediaRoot, stateDir := dirs(t)
			write(t, ShadowPath(stateDir), good)
			write(t, MediaPath(mediaRoot), tt.media)

			got := Load(mediaRoot, stateDir)
			if got.FromShadow != tt.wantShadow {
				t.Fatalf("FromShadow = %v, want %v: %+v", got.FromShadow, tt.wantShadow, got)
			}
			if !tt.wantShadow && len(got.Repaired) == 0 {
				t.Fatal("Load must report the fields that it repaired")
			}

			shadow, err := os.ReadFile(ShadowPath(stateDir))
			if err != nil {
				t.Fatal(err)
			}
			if string(shadow) != good {
				t.Fatal("a bad media file must never replace the shadow copy")
			}
		})
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
