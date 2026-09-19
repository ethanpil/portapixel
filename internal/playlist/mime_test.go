package playlist

import (
	"mime"
	"strings"
	"testing"
)

// TestEachMediaExtensionHasAType holds the type table and the kind tables together.
// An extension in one and not in the other gives a file that plays on a
// development machine and not on the device.
func TestEachMediaExtensionHasAType(t *testing.T) {
	for _, group := range []struct {
		name   string
		exts   map[string]bool
		prefix string
	}{
		{"image", imageExt, "image/"},
		{"video", videoExt, "video/"},
	} {
		for ext := range group.exts {
			kind, ok := mediaTypes[ext]
			if !ok {
				t.Errorf("%s extension %s has no media type", group.name, ext)
				continue
			}
			if !strings.HasPrefix(kind, group.prefix) {
				t.Errorf("%s has the type %s, which is not %s*", ext, kind, group.prefix)
			}
			if got := mime.TypeByExtension(ext); !strings.HasPrefix(got, group.prefix) {
				t.Errorf("the mime package gives %q for %s", got, ext)
			}
		}
	}
	for ext := range mediaTypes {
		if !imageExt[ext] && !videoExt[ext] {
			t.Errorf("%s has a media type but Kind does not know it", ext)
		}
	}
}
