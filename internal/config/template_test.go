package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A device with the template on PPMEDIA must start as a device with no file:
// every value is a default, and Load finds nothing to repair.
func TestTemplateLoadsAsDefault(t *testing.T) {
	got, err := Parse(Template())
	if err != nil {
		t.Fatalf("the template does not parse: %v\n%s", err, Template())
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Fatalf("the template parses to another value:\n got %+v\nwant %+v", got, Default())
	}

	media, state := t.TempDir(), t.TempDir()
	if err := os.WriteFile(MediaPath(media), Template(), 0o644); err != nil {
		t.Fatal(err)
	}
	res := Load(media, state)
	if res.FromDefault || res.FromShadow || len(res.Repaired) > 0 || res.Warning != "" {
		t.Fatalf("Load found a fault in the template: %+v", res)
	}
	if !reflect.DeepEqual(res.Config, Default()) {
		t.Fatalf("Load gave another value than the defaults: %+v", res.Config)
	}
}

// The person removes the comment mark from a table line and from the key lines
// under it. The values come out, and nothing else changes.
func TestTemplateTurnsOnWhatThePersonUncomments(t *testing.T) {
	tests := []struct {
		name  string
		edits [][2]string // old text, new text
		want  func(*Config)
	}{
		{
			name: "server url and token",
			edits: [][2]string{
				{"# [server]\n", "[server]\n"},
				{`# url = ""`, `url = "https://fleet.example.com"`},
				{`# token = ""`, `token = "enroll-token"`},
			},
			want: func(c *Config) {
				c.Server.URL = "https://fleet.example.com"
				c.Server.Token = "enroll-token"
			},
		},
		{
			name: "WiFi",
			edits: [][2]string{
				{"# [network]\n", "[network]\n"},
				{`# wifi_ssid = ""`, `wifi_ssid = "Office"`},
				{`# wifi_psk = ""`, `wifi_psk = "a secret"`},
				{`# wifi_country = "US"`, `wifi_country = "DE"`},
			},
			want: func(c *Config) {
				c.Network.WifiSSID = "Office"
				c.Network.WifiPSK = "a secret"
				c.Network.WifiCountry = "DE"
			},
		},
		{
			name: "a schedule rule from the example",
			edits: [][2]string{
				{"# [[schedule]]\n# playlist = \"weekday-loop\"\n# days = [\"mon\", \"tue\", \"wed\", \"thu\", \"fri\"]\n# start = \"08:00\"\n# end = \"18:00\"\n",
					"[[schedule]]\nplaylist = \"weekday-loop\"\ndays = [\"mon\", \"tue\", \"wed\", \"thu\", \"fri\"]\nstart = \"08:00\"\nend = \"18:00\"\n"},
			},
			want: func(c *Config) {
				c.Schedule = []Rule{{Playlist: "weekday-loop", Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "08:00", End: "18:00"}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := string(Template())
			for _, e := range tt.edits {
				if n := strings.Count(text, e[0]); n != 1 {
					t.Fatalf("the template holds %d copies of %q, and the test needs one", n, e[0])
				}
				text = strings.Replace(text, e[0], e[1], 1)
			}
			got, err := Parse([]byte(text))
			if err != nil {
				t.Fatalf("the edited template does not parse: %v\n%s", err, text)
			}
			want := Default()
			tt.want(&want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("the edited template parses to another value:\n got %+v\nwant %+v", got, want)
			}
			if errs := got.Validate(); len(errs) > 0 {
				t.Fatalf("the edited template is not valid: %v", errs)
			}
		})
	}
}

// Each key that Render writes is in the template as a comment, and no line of
// the template is live.
func TestTemplateHoldsEveryKeyAsAComment(t *testing.T) {
	text := string(Template())
	keys := 0
	for _, l := range strings.Split(string(Render(Default())), "\n") {
		if s := strings.TrimLeft(l, " "); s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		keys++
		if !strings.Contains(text, "\n# "+l+"\n") {
			t.Errorf("the template has no comment for the line %q", l)
		}
	}
	if keys == 0 {
		t.Fatal("Render(Default()) has no live line, so the test checked nothing")
	}
	for _, l := range strings.Split(text, "\n") {
		if s := strings.TrimLeft(l, " "); s != "" && !strings.HasPrefix(s, "#") {
			t.Errorf("the template has a live line: %q", l)
		}
	}
}

// A person opens the file in Notepad on Windows. It must read as plain ASCII
// with Unix line ends, and it must hold nothing secret that Default does not.
func TestTemplateIsPlainText(t *testing.T) {
	data := Template()
	for i, c := range data {
		if c >= 0x80 || c == '\r' {
			t.Fatalf("byte %d is %#x: the template must be ASCII with Unix line ends", i, c)
		}
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Error("the template must end with a line end")
	}
	if got := strings.Count(string(data), "password = "); got != 1 || !strings.Contains(string(data), `# password = "portapixel"`) {
		t.Errorf("the template must hold one password, the documented default")
	}
}

// The image ships os/portapixel.toml. It is a generated file, and this test is
// the only thing that keeps it in step with Render and Default.
func TestShippedTemplateIsCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "os", FileName)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read the template that the image ships: %v", err)
	}
	if !bytes.Equal(got, Template()) {
		t.Fatalf("os/%s is not the output of config.Template().\n"+
			"Run this command in the repository root and commit the result:\n\n"+
			"\tgo run ./internal/config/cmd/gentemplate\n", FileName)
	}
}
