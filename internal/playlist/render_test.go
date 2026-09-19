package playlist

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestRenderRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		p    Playlist
		opt  Options
	}{
		{
			name: "a mixed playlist",
			p: Playlist{
				Meta: Meta{Name: "Lobby loop", Shuffle: boolPtr(true), Transition: "cut"},
				Items: []Item{
					{File: "welcome.jpg", Duration: 15},
					{File: "promo.mp4", Mute: true, MaxDuration: 60},
					{URL: "https://dash.example.com/board", Duration: 60, RefreshSeconds: 300},
				},
			},
		},
		{
			name: "no name and no options",
			p:    Playlist{Items: []Item{{File: "a.jpg"}}},
		},
		{
			name: "kiosk mode",
			p:    Playlist{Items: []Item{{URL: "https://example.com/board"}}},
		},
		{
			name: "shuffle off",
			p:    Playlist{Meta: Meta{Shuffle: boolPtr(false)}, Items: []Item{{File: "a.jpg"}}},
		},
		{
			name: "a video with sound",
			p:    Playlist{Items: []Item{{File: "a.mp4", Mute: false}}},
		},
		{
			name: "a fleet playlist",
			p: Playlist{
				Meta:  Meta{Name: "Fleet loop"},
				Items: []Item{{File: "../media/abababab-a.jpg", Duration: 10}},
			},
			opt: Options{AllowFleetRefs: true},
		},
		{
			name: "names with quotation marks and backslashes",
			p: Playlist{
				Meta:  Meta{Name: `The "big" screen \ lobby`},
				Items: []Item{{File: `a "quoted" name.jpg`, Duration: 5}},
			},
		},
		{
			name: "a name with other alphabets",
			p:    Playlist{Meta: Meta{Name: "東京 café"}, Items: []Item{{File: "a.jpg"}}},
		},
		{
			name: "an image with a mute flag",
			p:    Playlist{Items: []Item{{File: "a.jpg", Mute: true}}},
		},
		{
			name: "a url with a query",
			p:    Playlist{Items: []Item{{URL: "https://a/board?token=x&y=1"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := Render(tt.p)
			got, err := Parse(first, tt.opt)
			if err != nil {
				t.Fatalf("the output does not parse: %v\n%s", err, first)
			}
			if !reflect.DeepEqual(got, tt.p) {
				t.Fatalf("parse gave another value:\n got %+v\nwant %+v\n%s", got, tt.p, first)
			}
			second := Render(got)
			if !bytes.Equal(first, second) {
				t.Fatalf("render is not stable:\nfirst:\n%s\nsecond:\n%s", first, second)
			}
		})
	}
}

func TestRenderHoldsTheStockComments(t *testing.T) {
	out := string(Render(Playlist{Items: []Item{{File: "a.jpg"}}}))
	for _, want := range []string{
		"# PortaPixel playlist.",
		"your comments do not",
		"[playlist]",
		"# shuffle = true",
		`# transition = "cut"`,
		"[[item]]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output must hold %q:\n%s", want, out)
		}
	}
}

func TestRenderCommentsGoAwayWhenAKeyIsSet(t *testing.T) {
	p := Playlist{
		Meta:  Meta{Name: "a", Shuffle: boolPtr(true), Transition: "cut"},
		Items: []Item{{File: "a.mp4", MaxDuration: 30}},
	}
	out := string(Render(p))
	for _, bad := range []string{"# shuffle", "# transition", "# max_duration"} {
		if strings.Contains(out, bad) {
			t.Errorf("the output must not hold %q when the key has a value:\n%s", bad, out)
		}
	}
	for _, want := range []string{"shuffle = true", `transition = "cut"`, "max_duration = 30"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output must hold %q:\n%s", want, out)
		}
	}
}

func TestRenderUsesUnixLineEnds(t *testing.T) {
	out := Render(Playlist{Items: []Item{{File: "a.jpg"}}})
	if bytes.Contains(out, []byte("\r")) {
		t.Error("the file must use Unix line ends")
	}
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Error("the file must end with a line end")
	}
}
