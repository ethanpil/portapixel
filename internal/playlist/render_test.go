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
				},
			},
		},
		{
			name: "no name and no options",
			p:    Playlist{Items: []Item{{File: "a.jpg"}}},
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
			name: "ken burns",
			p: Playlist{
				Meta:  Meta{Name: "Photos", KenBurns: true},
				Items: []Item{{File: "a.jpg", Duration: 20}, {File: "b.mp4"}},
			},
		},
		{
			name: "items with their own transitions",
			p: Playlist{
				Meta: Meta{Transition: "fade"},
				Items: []Item{
					{File: "a.jpg", Transition: "slide-in-left", TransitionMS: 700},
					{File: "b.mp4", Mute: true, Transition: "cut"},
					{File: "c.jpg", TransitionMS: 250},
					{File: "d.jpg"},
				},
			},
		},
		{
			name: "an image with a mute flag",
			p:    Playlist{Items: []Item{{File: "a.jpg", Mute: true}}},
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
		"# ken_burns = true",
		"[[item]]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output must hold %q:\n%s", want, out)
		}
	}
}

func TestRenderCommentsGoAwayWhenAKeyIsSet(t *testing.T) {
	p := Playlist{
		Meta:  Meta{Name: "a", Shuffle: boolPtr(true), Transition: "cut", KenBurns: true},
		Items: []Item{{File: "a.mp4", MaxDuration: 30}},
	}
	out := string(Render(p))
	for _, bad := range []string{"# shuffle", "# transition", "# max_duration", "# ken_burns"} {
		if strings.Contains(out, bad) {
			t.Errorf("the output must not hold %q when the key has a value:\n%s", bad, out)
		}
	}
	for _, want := range []string{"shuffle = true", `transition = "cut"`, "max_duration = 30", "ken_burns = true"} {
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

// The two keys of an item come back only when they have a value, and they
// stay with their item.
func TestRenderWritesTheItemTransitionOnlyWhenSet(t *testing.T) {
	out := string(Render(Playlist{Items: []Item{
		{File: "a.jpg"},
		{File: "b.jpg", Transition: "zoom-out", TransitionMS: 900},
	}}))
	if n := strings.Count(out, "transition = \"zoom-out\""); n != 1 {
		t.Errorf("the item transition is written %d times, want 1:\n%s", n, out)
	}
	first := out[:strings.Index(out, "b.jpg")]
	if strings.Contains(first[strings.Index(first, "[[item]]"):], "transition") {
		t.Errorf("the first item has no transition, and the file gives it one:\n%s", out)
	}
	if !strings.Contains(out, "transition_ms = 900") {
		t.Errorf("the length of the item transition is not in the file:\n%s", out)
	}
}
