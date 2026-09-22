package media

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethanpil/portapixel/internal/store"
)

// orphanGrace is how old a blob with no row must be before the sweep removes it.
//
// An upload writes the blob first and the row after it, so a blob with no row can
// be an upload that is still in flight. An hour is far longer than any upload, and
// a blob that is younger than that is never touched.
//
// The grace period is what makes the known set safe although it is read before the
// walk. Every path that gives a blob a row touches the file first: a new object
// arrives with a new modification time, and an upload of bytes that the store
// already holds gives the old file a new one (Put). So a blob that gains a row
// during a sweep is always younger than the cutoff of that sweep.
const orphanGrace = time.Hour

// SweepResult says what one sweep removed.
type SweepResult struct {
	// Blobs and Thumbs count the files that had no row.
	Blobs  int
	Thumbs int
	// Temps counts the part files of uploads that a crash left behind.
	Temps int
	Bytes int64
}

// Sweep removes the files of the store that no row can reach.
//
// Why the store needs this: an upload commits the blob to its final name before
// the row goes in, and a delete removes the row before the file. Either failure
// leaves a file that no route can reach and that no page counts. On Windows a
// Range download that is in flight makes the delete of a file fail, so the delete
// path leaves an orphan on a normal day and not only after a crash.
//
// known holds every hash that the media table has. The caller reads it from the
// database, because the store must not import the database package.
func (s *Store) Sweep(known map[string]bool, now time.Time) (SweepResult, error) {
	var out SweepResult
	cutoff := now.Add(-orphanGrace)

	err := filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A directory that went away under the walk is not a fault of ours.
			return nil
		}
		if entry.IsDir() {
			if path != s.root && filepath.Base(path) == tmpDirName {
				out.Temps += s.sweepTemps(path, cutoff)
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !store.IsSHA256(name) || known[name] {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil || info.ModTime().After(cutoff) {
			return nil
		}
		size := info.Size()
		if rmErr := os.Remove(path); rmErr == nil {
			out.Blobs++
			out.Bytes += size
		}
		return nil
	})
	if err != nil {
		return out, err
	}

	// A thumbnail of an object that went away is an orphan of the same kind.
	entries, err := os.ReadDir(s.thumbs)
	if err != nil {
		return out, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "thumb") {
			// A part file of the thumbnail step.
			if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
				if os.Remove(filepath.Join(s.thumbs, name)) == nil {
					out.Temps++
				}
			}
			continue
		}
		sha := strings.TrimSuffix(name, ".jpg")
		if sha == name || !store.IsSHA256(sha) || known[sha] {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(s.thumbs, name)) == nil {
			out.Thumbs++
		}
	}
	return out, nil
}

// sweepTemps removes the part files of uploads that never finished.
func (s *Store) sweepTemps(dir string, cutoff time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(dir, entry.Name())) == nil {
			n++
		}
	}
	return n
}

// SweepEvery runs the sweep now and then once in each interval, until stop is
// closed. The server starts it: the first pass cleans what the last run left, and
// the later passes clean what a failed delete leaves.
func (s *Store) SweepEvery(interval time.Duration, known func() (map[string]bool, error), stop <-chan struct{}) {
	run := func() {
		rows, err := known()
		if err != nil {
			slog.Warn("the media sweep could not read the media table", "error", err)
			return
		}
		res, err := s.Sweep(rows, time.Now())
		if err != nil {
			slog.Warn("the media sweep failed", "error", err)
			return
		}
		if res.Blobs+res.Thumbs+res.Temps > 0 {
			slog.Info("the media sweep removed files that no row can reach",
				"objects", res.Blobs, "thumbnails", res.Thumbs, "part_files", res.Temps, "bytes", res.Bytes)
		}
	}
	run()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			run()
		}
	}
}
