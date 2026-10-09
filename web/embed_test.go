package web

import (
	"bytes"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ethanpil/portapixel/internal/config"
)

// The licence list is in two places: the root of the repository, which is what D33
// asks for, and this directory, which is what the binary embeds. A list that drifts
// is worse than no list, so the two must be the same bytes.
func TestLicensesMatchTheRepositoryCopy(t *testing.T) {
	embedded, err := Licenses()
	if err != nil {
		t.Fatalf("the binary embeds no %s: %v", LicensesName, err)
	}
	root, err := os.ReadFile("../" + LicensesName)
	if err != nil {
		t.Fatalf("read the copy at the root of the repository: %v", err)
	}
	if !bytes.Equal(embedded, root) {
		t.Errorf("web/%s and %s at the root of the repository are different. Copy one over the other.",
			LicensesName, LicensesName)
	}
}

// The playlist editor and the device settings page offer the transitions from
// the list in shared/playlist-editor.js. It must hold the words of
// config.Transitions, no more and no fewer.
func TestTheEditorOffersEveryTransition(t *testing.T) {
	data, err := os.ReadFile("shared/playlist-editor.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "export const TRANSITIONS = [")
	if start < 0 {
		t.Fatal("shared/playlist-editor.js has no TRANSITIONS list")
	}
	block := text[start : start+strings.Index(text[start:], "];")]
	var words []string
	for _, m := range regexp.MustCompile(`\['([a-z-]+)',`).FindAllStringSubmatch(block, -1) {
		words = append(words, m[1])
	}
	want := slices.Clone(config.Transitions)
	slices.Sort(words)
	slices.Sort(want)
	if !slices.Equal(words, want) {
		t.Errorf("the editor offers %v, config.Transitions is %v", words, want)
	}
}
