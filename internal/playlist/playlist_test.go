package playlist

import (
	"reflect"
	"strings"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

func TestParse(t *testing.T) {
	const good = `
# a comment from the user
[playlist]
name = "Lobby loop"
shuffle = true
transition = "cut"

[[item]]
file = "welcome.jpg"
duration = 15

[[item]]
file = "promo.mp4"
mute = true
max_duration = 60

[[item]]
url = "https://dash.example.com/board"
duration = 60
refresh_seconds = 300
`
	want := Playlist{
		Meta: Meta{Name: "Lobby loop", Shuffle: boolPtr(true), Transition: "cut"},
		Items: []Item{
			{File: "welcome.jpg", Duration: 15},
			{File: "promo.mp4", Mute: true, MaxDuration: 60},
			{URL: "https://dash.example.com/board", Duration: 60, RefreshSeconds: 300},
		},
	}

	got, err := Parse([]byte(good), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseSmallFiles(t *testing.T) {
	tests := []struct {
		name string
		toml string
		want Playlist
	}{
		{
			name: "no name and one item",
			toml: "[[item]]\nfile = \"a.jpg\"\n",
			want: Playlist{Items: []Item{{File: "a.jpg"}}},
		},
		{
			name: "one url item is kiosk mode",
			toml: "[[item]]\nurl = \"https://example.com/\"\n",
			want: Playlist{Items: []Item{{URL: "https://example.com/"}}},
		},
		{
			name: "shuffle false is not the same as no shuffle",
			toml: "[playlist]\nshuffle = false\n[[item]]\nfile = \"a.jpg\"\n",
			want: Playlist{Meta: Meta{Shuffle: boolPtr(false)}, Items: []Item{{File: "a.jpg"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.toml), Options{})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestParseGarbage gives the parser text that a person or a broken program could
// write. The rule is that Parse gives an error and never panics.
func TestParseGarbage(t *testing.T) {
	inputs := []string{
		"",
		" ",
		"\n\n\n",
		"not toml at all",
		"[[item]",
		"[[item]]\nfile = \n",
		"[[item]]\nfile = \"a.jpg",
		"[[item]]\nfile = 3\n",
		"[[item]]\nduration = \"long\"\n",
		"[playlist]\nname = [1, 2]\n",
		"[playlist]\n[playlist]\n",
		"[[item]]\nfile = \"a.jpg\"\nfile = \"b.jpg\"\n",
		"\x00\x01\x02\xff",
		"[[item]]\nfile = \"\x00.jpg\"\n",
		strings.Repeat("[[item]]\n", 1000),
		strings.Repeat("[", 500),
		"[playlist]\nname = \"" + strings.Repeat("a", 100000) + "\"\n",
		"[[item]]\nfile = \"" + strings.Repeat("../", 200) + "a.jpg\"\n",
		"[[item]]\nurl = \"javascript:alert(1)\"\n",
		"[[item]]\nfile = \"a.jpg\"\nurl = \"https://a\"\n",
		"[[item]]\nduration = -5\nfile = \"a.jpg\"\n",
		"[[item]]\nfile = \"C:\\\\media\\\\a.jpg\"\n",
		"[[item]]\nfile = \"/etc/passwd\"\n",
		"[[item]]\nmute = \"yes\"\nfile = \"a.jpg\"\n",
		"[playlist]\nshuffle = \"maybe\"\n[[item]]\nfile = \"a.jpg\"\n",
		"[playlist]\ntransition = \"wipe\"\n[[item]]\nfile = \"a.jpg\"\n",
		"[[items]]\nfile = \"a.jpg\"\n", // the wrong table name, so no items
	}
	for _, in := range inputs {
		name := in
		if len(name) > 30 {
			name = name[:30] + "..."
		}
		t.Run(name, func(t *testing.T) {
			// Parse must not panic. An error is the correct answer for each of
			// these inputs.
			p, err := Parse([]byte(in), Options{})
			if err == nil {
				t.Fatalf("want an error, got %+v", p)
			}
			// Validate on the value that came back must also not panic.
			p.Validate(Options{})
			Render(p)
			for _, it := range p.Items {
				Kind(it)
			}
			p.IsKiosk()
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name      string
		playlist  Playlist
		opt       Options
		wantField string // "" means that the playlist is good
	}{
		{
			name:     "a good playlist",
			playlist: Playlist{Items: []Item{{File: "a.jpg", Duration: 10}}},
		},
		{
			name:     "a file in a subdirectory",
			playlist: Playlist{Items: []Item{{File: "images/a.jpg"}}},
		},
		{
			name:      "no items",
			playlist:  Playlist{},
			wantField: "item",
		},
		{
			name:      "an item with nothing in it",
			playlist:  Playlist{Items: []Item{{}}},
			wantField: "item[0]",
		},
		{
			name:      "an item with a file and a url",
			playlist:  Playlist{Items: []Item{{File: "a.jpg", URL: "https://a"}}},
			wantField: "item[0]",
		},
		{
			name:      "an absolute path",
			playlist:  Playlist{Items: []Item{{File: "/etc/passwd"}}},
			wantField: "item[0].file",
		},
		{
			name:      "a windows path",
			playlist:  Playlist{Items: []Item{{File: `sub\a.jpg`}}},
			wantField: "item[0].file",
		},
		{
			name:      "a drive letter",
			playlist:  Playlist{Items: []Item{{File: "C:/media/a.jpg"}}},
			wantField: "item[0].file",
		},
		{
			name:      "a parent step",
			playlist:  Playlist{Items: []Item{{File: "../secret.jpg"}}},
			wantField: "item[0].file",
		},
		{
			name:      "a parent step in the middle",
			playlist:  Playlist{Items: []Item{{File: "images/../../secret.jpg"}}},
			wantField: "item[0].file",
		},
		{
			name:      "a path that is not clean",
			playlist:  Playlist{Items: []Item{{File: "./a.jpg"}}},
			wantField: "item[0].file",
		},
		{
			name:      "a double separator",
			playlist:  Playlist{Items: []Item{{File: "images//a.jpg"}}},
			wantField: "item[0].file",
		},
		{
			name:      "a fleet reference without the option",
			playlist:  Playlist{Items: []Item{{File: "../media/abababab-a.jpg"}}},
			wantField: "item[0].file",
		},
		{
			name:     "a fleet reference with the option",
			playlist: Playlist{Items: []Item{{File: "../media/abababab-a.jpg"}}},
			opt:      Options{AllowFleetRefs: true},
		},
		{
			name:      "another parent step with the option",
			playlist:  Playlist{Items: []Item{{File: "../../media/a.jpg"}}},
			opt:       Options{AllowFleetRefs: true},
			wantField: "item[0].file",
		},
		{
			name:      "another directory with the option",
			playlist:  Playlist{Items: []Item{{File: "../secret/a.jpg"}}},
			opt:       Options{AllowFleetRefs: true},
			wantField: "item[0].file",
		},
		{
			name:      "a fleet reference that goes deeper",
			playlist:  Playlist{Items: []Item{{File: "../media/sub/a.jpg"}}},
			opt:       Options{AllowFleetRefs: true},
			wantField: "item[0].file",
		},
		{
			name:      "a url that is not http",
			playlist:  Playlist{Items: []Item{{URL: "file:///etc/passwd"}}},
			wantField: "item[0].url",
		},
		{
			name:      "refresh_seconds on a file item",
			playlist:  Playlist{Items: []Item{{File: "a.mp4", RefreshSeconds: 60}}},
			wantField: "item[0].refresh_seconds",
		},
		{
			name:      "max_duration on a url item",
			playlist:  Playlist{Items: []Item{{URL: "https://a", Duration: 10, MaxDuration: 60}}},
			wantField: "item[0].max_duration",
		},
		{
			name:     "a url item alone needs no duration",
			playlist: Playlist{Items: []Item{{URL: "https://a"}}},
		},
		{
			name: "a url item with other items needs a duration",
			playlist: Playlist{Items: []Item{
				{File: "a.jpg", Duration: 10},
				{URL: "https://a"},
			}},
			wantField: "item[1].duration",
		},
		{
			name:      "a negative duration",
			playlist:  Playlist{Items: []Item{{File: "a.jpg", Duration: -1}}},
			wantField: "item[0].duration",
		},
		{
			name:      "a negative max duration",
			playlist:  Playlist{Items: []Item{{File: "a.mp4", MaxDuration: -1}}},
			wantField: "item[0].max_duration",
		},
		{
			name:      "a bad transition",
			playlist:  Playlist{Meta: Meta{Transition: "wipe"}, Items: []Item{{File: "a.jpg"}}},
			wantField: "playlist.transition",
		},
		{
			name:     "a good transition",
			playlist: Playlist{Meta: Meta{Transition: "push-up"}, Items: []Item{{File: "a.jpg"}}},
		},
		{
			name: "the second item is bad",
			playlist: Playlist{Items: []Item{
				{File: "a.jpg", Duration: 10},
				{File: "../b.jpg"},
			}},
			wantField: "item[1].file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.playlist.Validate(tt.opt)
			if tt.wantField == "" {
				if len(errs) > 0 {
					t.Fatalf("want no error, got %v", errs)
				}
				return
			}
			found := false
			for _, e := range errs {
				if e.Field == tt.wantField {
					found = true
				}
			}
			if !found {
				t.Fatalf("want an error for %q, got %v", tt.wantField, errs)
			}
		})
	}
}

func TestKind(t *testing.T) {
	tests := []struct {
		item Item
		want string
	}{
		{item: Item{File: "a.jpg"}, want: KindImage},
		{item: Item{File: "a.jpeg"}, want: KindImage},
		{item: Item{File: "a.PNG"}, want: KindImage},
		{item: Item{File: "a.gif"}, want: KindImage},
		{item: Item{File: "a.webp"}, want: KindImage},
		{item: Item{File: "a.svg"}, want: KindImage},
		{item: Item{File: "a.avif"}, want: KindImage},
		{item: Item{File: "a.bmp"}, want: KindImage},
		{item: Item{File: "sub/b.Jpg"}, want: KindImage},
		{item: Item{File: "a.mp4"}, want: KindVideo},
		{item: Item{File: "a.m4v"}, want: KindVideo},
		{item: Item{File: "a.mov"}, want: KindVideo},
		{item: Item{File: "a.webm"}, want: KindVideo},
		{item: Item{File: "a.mkv"}, want: KindVideo},
		{item: Item{File: "a.ogv"}, want: KindVideo},
		{item: Item{File: "a.MP4"}, want: KindVideo},
		{item: Item{URL: "https://example.com/"}, want: KindURL},
		{item: Item{File: "a.pdf"}, want: KindUnknown},
		{item: Item{File: "a.txt"}, want: KindUnknown},
		{item: Item{File: "noextension"}, want: KindUnknown},
		{item: Item{File: "a.mp4.txt"}, want: KindUnknown},
		{item: Item{}, want: KindUnknown},
	}
	for _, tt := range tests {
		name := tt.item.File
		if name == "" {
			name = tt.item.URL
		}
		t.Run(name, func(t *testing.T) {
			if got := Kind(tt.item); got != tt.want {
				t.Fatalf("Kind(%+v) = %q, want %q", tt.item, got, tt.want)
			}
		})
	}
}

func TestIsKiosk(t *testing.T) {
	tests := []struct {
		name string
		p    Playlist
		want bool
	}{
		{
			name: "one url item",
			p:    Playlist{Items: []Item{{URL: "https://a"}}},
			want: true,
		},
		{
			name: "one url item with a refresh",
			p:    Playlist{Items: []Item{{URL: "https://a", RefreshSeconds: 300}}},
			want: true,
		},
		{name: "one file item", p: Playlist{Items: []Item{{File: "a.jpg"}}}},
		{name: "no items", p: Playlist{}},
		{
			name: "two url items",
			p:    Playlist{Items: []Item{{URL: "https://a", Duration: 10}, {URL: "https://b", Duration: 10}}},
		},
		{
			name: "a url item and a file item",
			p:    Playlist{Items: []Item{{URL: "https://a", Duration: 10}, {File: "a.jpg"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.IsKiosk(); got != tt.want {
				t.Fatalf("IsKiosk = %v, want %v", got, tt.want)
			}
		})
	}
}
