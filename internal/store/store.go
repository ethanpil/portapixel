package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// maxNameLen is the maximum length of a safe name. exFAT permits much longer
// names, but a short name keeps the paths readable in the admin UI and in logs.
const maxNameLen = 64

// ObjectName gives the file name of one object in the store:
// <first 8 hex of the sha>-<safe name>. The device keeps fleet objects at
// _fleet/media/<this name>.
//
// The sha must be 64 lower case hex characters. It comes from the fleet
// manifest, which is not trusted input: a value such as "../../.." would put
// the object outside the store. The caller skips an item that gives an error
// here and writes an ops log line.
func ObjectName(sha, origName string) (string, error) {
	if !IsSHA256(sha) {
		return "", fmt.Errorf("%q is not a SHA-256 value of 64 lower case hex characters", sha)
	}
	return sha[:8] + "-" + SafeName(origName), nil
}

// IsSHA256 reports if sha is 64 lower case hex characters. HashFile gives that
// form, so the two ends of a download compare the same thing.
//
// It is exported because the fleet server needs the same rule: a hash from a URL
// path becomes a file name in the media store, and a value such as "../../etc"
// must stop before anything opens a file.
func IsSHA256(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	for i := 0; i < len(sha); i++ {
		c := sha[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// SafeName makes a file name that every filesystem accepts. It keeps letters,
// digits, the full stop, the hyphen and the underscore. It changes each other
// character to a hyphen.
//
// The name keeps its extension, because the extension is what tells the player
// if an object is an image or a video.
func SafeName(origName string) string {
	base := origName
	// Take the last element of a path in either direction. A name that comes
	// from the fleet server can hold a path separator of either kind.
	base = base[strings.LastIndexAny(base, `/\`)+1:]

	ext := path.Ext(base)
	if len(ext) > 10 { // not an extension, only a full stop in the name
		ext = ""
	}
	stem := base[:len(base)-len(ext)]

	stem = keepSafe(stem)
	if ext != "" {
		// Keep the full stop of the extension. keepSafe removes it.
		if safe := keepSafe(ext[1:]); safe != "" {
			ext = "." + safe
		} else {
			ext = ""
		}
	}
	if stem == "" {
		stem = "object"
	}
	if max := maxNameLen - len(ext); len(stem) > max {
		stem = strings.Trim(stem[:max], "-.")
		if stem == "" {
			stem = "object"
		}
	}
	return stem + ext
}

// keepSafe changes every character that is not safe to a hyphen, joins runs of
// hyphens, and removes hyphens and full stops from the two ends.
func keepSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-.")
}

// HashFile gives the SHA-256 of a file as lower case hex.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
