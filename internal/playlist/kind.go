package playlist

import (
	"path"
	"strings"
)

// The kinds of item. The player, the admin UI and the fleet dashboard use these
// words.
const (
	KindImage   = "image"
	KindVideo   = "video"
	KindUnknown = "unknown"
)

// imageExt and videoExt hold the extensions that the player can show. The
// player is mpv, which decodes the images and the video with FFmpeg. SVG is not
// on the list.
var (
	imageExt = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
		".webp": true, ".avif": true, ".bmp": true,
	}
	videoExt = map[string]bool{
		".mp4": true, ".m4v": true, ".mov": true,
		".webm": true, ".mkv": true, ".ogv": true,
	}
)

// Kind says what an item is. A file with an extension that we do not know is
// "unknown": the player skips it and the admin UI marks it.
func Kind(it Item) string {
	ext := strings.ToLower(path.Ext(it.File))
	switch {
	case imageExt[ext]:
		return KindImage
	case videoExt[ext]:
		return KindVideo
	default:
		return KindUnknown
	}
}
