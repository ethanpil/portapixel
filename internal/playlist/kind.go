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
	KindURL     = "url"
	KindUnknown = "unknown"
)

// imageExt and videoExt hold the extensions that the browser can show. WPE
// WebKit gives the images; GStreamer gives the video.
var (
	imageExt = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
		".webp": true, ".svg": true, ".avif": true, ".bmp": true,
	}
	videoExt = map[string]bool{
		".mp4": true, ".m4v": true, ".mov": true,
		".webm": true, ".mkv": true, ".ogv": true,
	}
)

// Kind says what an item is. A file with an extension that we do not know is
// "unknown": the player skips it and the admin UI marks it.
func Kind(it Item) string {
	if it.URL != "" && it.File == "" {
		return KindURL
	}
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
