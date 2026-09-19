package playlist

import "mime"

// mediaTypes gives the media type of each file extension that Kind knows.
//
// Alpine and a distroless container have no /etc/mime.types, so the mime package
// knows almost no extension there. Go then reads the first bytes of a file, and
// it cannot identify SVG, AVIF or Matroska. The browser gets text/plain and does
// not show the item. Windows reads the registry and Ubuntu has mailcap, so a
// development machine and the CI runner hide this fault.
//
// The device and the server import this package, so one table serves the two.
var mediaTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".avif": "image/avif",
	".bmp":  "image/bmp",
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".mov":  "video/quicktime",
	".webm": "video/webm",
	".mkv":  "video/x-matroska",
	".ogv":  "video/ogg",
}

// MediaType gives the media type of a lower case extension with its dot, or ""
// when Kind does not know the extension. A caller that must not depend on the
// init below calls this function.
func MediaType(ext string) string {
	return mediaTypes[ext]
}

// init also puts the table into the mime package, because http.ServeContent asks
// that package and takes no table from the caller.
func init() {
	for ext, kind := range mediaTypes {
		// The error is for an extension with no leading dot. Each key has one.
		_ = mime.AddExtensionType(ext, kind)
	}
}
