package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// ParseKeys names the full key of each unknown key, in the order of the file. An
// unknown table gives the keys in it and not its own name.
func TestParseKeysNamesTheUnknownKeys(t *testing.T) {
	tests := []struct {
		name string
		file string
		want []string
	}{
		{"a file with no unknown key", string(Render(Default())), nil},
		{"a key at the top of the file", "url = \"x\"\n", []string{"url"}},
		{"a key that is gone", "[device]\ntier = \"low\"\nname = \"a\"\n", []string{"device.tier"}},
		{"a key under the wrong table", "[watchdog]\nenabled = true\nurl = \"x\"\n", []string{"watchdog.url"}},
		{"a table that is gone", "[bogus]\na = 1\nb = 2\n", []string{"bogus.a", "bogus.b"}},
		{"an empty table that is gone", "[bogus]\n", []string{"bogus"}},
		{"a key in a schedule rule", "[[schedule]]\nplaylist = \"a\"\nfoo = 1\n", []string{"schedule.foo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, err := ParseKeys([]byte(tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("unknown keys = %q, want %q", got, tt.want)
			}
		})
	}
}

// A file that the device writes holds no key that it does not know.
func TestLoadGivesNoWarningForARenderedFile(t *testing.T) {
	mediaRoot, stateDir := dirs(t)
	cfg := Default()
	cfg.Device.Name = "Lobby"
	cfg.Network.WifiSSID = "Guest"
	write(t, MediaPath(mediaRoot), string(Render(cfg)))

	got := Load(mediaRoot, stateDir)

	if got.Warning != "" || len(got.Unknown) != 0 || len(got.Repaired) != 0 {
		t.Fatalf("Load found a fault in a rendered file: %+v", got)
	}
}

// The person removes the "#" from url and keeps it on "# [server]". The key then
// sits at the top of the file and no table owns it. Load warns of it. It does not
// repair, it does not change a value, and the file still loads (and is mirrored).
func TestLoadWarnsOfAKeyWhoseTableLineIsStillCommented(t *testing.T) {
	mediaRoot, stateDir := dirs(t)
	text := string(Template())
	if strings.Count(text, `# url = ""`) != 1 {
		t.Fatal("the template must hold one `# url = \"\"` line for this test")
	}
	text = strings.Replace(text, `# url = ""`, `url = "https://fleet.example.com"`, 1)
	write(t, MediaPath(mediaRoot), text)

	got := Load(mediaRoot, stateDir)

	if got.FromDefault || got.FromShadow || len(got.Repaired) != 0 {
		t.Fatalf("Load threw the file away or repaired it: %+v", got)
	}
	if !reflect.DeepEqual(got.Config, Default()) {
		t.Errorf("Load changed a value:\n got %+v\nwant %+v", got.Config, Default())
	}
	if !slices.Equal(got.Unknown, []string{"url"}) {
		t.Errorf("Unknown = %q, want [url]", got.Unknown)
	}
	const want = "portapixel.toml has a key that this device does not know: url (is its [table] line still commented?)"
	if got.Warning != want {
		t.Errorf("Warning = %q, want %q", got.Warning, want)
	}
	if _, err := os.Stat(ShadowPath(stateDir)); err != nil {
		t.Errorf("Load did not mirror a file that loads: %v", err)
	}
}

// The warning names at most five keys and says how many it left out.
func TestLoadNamesAtMostFiveUnknownKeys(t *testing.T) {
	mediaRoot, stateDir := dirs(t)
	write(t, MediaPath(mediaRoot), "[bogus]\na = 1\nb = 2\nc = 3\nd = 4\ne = 5\nf = 6\ng = 7\n")

	got := Load(mediaRoot, stateDir)

	if len(got.Unknown) != 7 {
		t.Fatalf("Unknown = %q, want 7 keys", got.Unknown)
	}
	for _, k := range []string{"bogus.a", "bogus.e", "and 2 more"} {
		if !strings.Contains(got.Warning, k) {
			t.Errorf("Warning = %q, want it to hold %q", got.Warning, k)
		}
	}
	if strings.Contains(got.Warning, "bogus.f") {
		t.Errorf("Warning = %q, names a sixth key", got.Warning)
	}
	if !strings.Contains(got.Warning, "has keys that") {
		t.Errorf("Warning = %q, want the plural form", got.Warning)
	}
}

// A file with a bad value and an unknown key gets one warning that says both.
func TestLoadWarnsOfBothARepairAndAnUnknownKey(t *testing.T) {
	mediaRoot, stateDir := dirs(t)
	text := strings.Replace(string(Render(Default())), "rotation = 0", "rotation = 45\nurl = \"x\"", 1)
	write(t, MediaPath(mediaRoot), text)

	got := Load(mediaRoot, stateDir)

	if len(got.Repaired) != 1 || !slices.Equal(got.Unknown, []string{"display.url"}) {
		t.Fatalf("Repaired = %v, Unknown = %q; want one repair and display.url", got.Repaired, got.Unknown)
	}
	for _, w := range []string{"display.rotation", "display.url"} {
		if !strings.Contains(got.Warning, w) {
			t.Errorf("Warning = %q, want it to name %s", got.Warning, w)
		}
	}
}

// The image ships os/portapixel.toml. As the person gets it, the file gives the
// defaults and no warning.
func TestShippedTemplateGivesNoWarning(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "os", FileName))
	if err != nil {
		t.Fatalf("cannot read the template that the image ships: %v", err)
	}
	mediaRoot, stateDir := dirs(t)
	write(t, MediaPath(mediaRoot), string(data))

	got := Load(mediaRoot, stateDir)

	if got.Warning != "" || len(got.Unknown) != 0 || len(got.Repaired) != 0 || got.FromDefault || got.FromShadow {
		t.Fatalf("Load found a fault in the shipped template: %+v", got)
	}
	if !reflect.DeepEqual(got.Config, Default()) {
		t.Errorf("Load gave another value than the defaults: %+v", got.Config)
	}
}
