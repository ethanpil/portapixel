package library

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// CacheName is the name of the hash cache in the state directory. The cache is
// on ext4, not on the media partition: it is our bookkeeping and it must not
// appear on a card that a person reads on a laptop.
const CacheName = "hashcache.json"

// hashPause is the rest between two files. The device computes the hash in the
// background: it must never take the processor away from the browser. 4 files a
// second is fast enough to hash a full card in a few minutes and slow enough to be
// invisible.
const hashPause = 250 * time.Millisecond

// errStopping says that the daemon is shutting down in the middle of a hash.
var errStopping = errors.New("the daemon is stopping")

// cacheEntry is one file in the cache. The key of the map is the path under the
// media root. Size and modification time together say if the file is still the
// file that we hashed: a new file with the same name has a new time.
type cacheEntry struct {
	Size   int64  `json:"size"`
	ModNS  int64  `json:"mtime_ns"`
	SHA256 string `json:"sha256"`
}

// hashCache holds the SHA-256 of each file. It is safe for use by more than one
// goroutine.
type hashCache struct {
	mu      sync.Mutex
	path    string
	entries map[string]cacheEntry
	// version counts the changes. save compares it with the value that it copied
	// before the write: a put that landed while the file was written must not look
	// like a cache that is already on the disk.
	version int
	saved   int
	// failed is true after a write that did not work. The failure goes in the ops
	// log once and not at every pass.
	failed bool
}

// loadCache reads the cache. A missing or damaged file gives an empty cache: the
// only cost is that the device hashes the files again.
func loadCache(stateDir string) *hashCache {
	c := &hashCache{
		path:    filepath.Join(stateDir, CacheName),
		entries: make(map[string]cacheEntry),
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return c
	}
	var entries map[string]cacheEntry
	if err := json.Unmarshal(data, &entries); err == nil && entries != nil {
		c.entries = entries
	}
	return c
}

// get gives the hash of a file, if the cache holds a hash for this exact file.
func (c *hashCache) get(rel string, size, modNS int64) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[rel]
	if !ok || e.Size != size || e.ModNS != modNS || e.SHA256 == "" {
		return "", false
	}
	return e.SHA256, true
}

// candidate is one entry of the cache that holds a wanted hash.
type candidate struct {
	rel   string
	size  int64
	modNS int64
}

// find gives every file that the cache says holds this hash. The caller checks
// which of them is still the file that we hashed.
//
// Every candidate and not the first one. A map has no order. With one live file and
// one stale entry of the same content, the first answer was a coin flip. Half of the
// polls then downloaded a video of 1 GB that was already on the card.
func (c *hashCache) find(sha string) []candidate {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []candidate
	for k, e := range c.entries {
		if strings.EqualFold(e.SHA256, sha) {
			out = append(out, candidate{rel: k, size: e.Size, modNS: e.ModNS})
		}
	}
	// One order, so two calls give one answer.
	sort.Slice(out, func(a, b int) bool { return out[a].rel < out[b].rel })
	return out
}

// put adds a hash.
func (c *hashCache) put(rel string, size, modNS int64, sha string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[rel] = cacheEntry{Size: size, ModNS: modNS, SHA256: sha}
	c.version++
}

// keep removes every entry that is not in the live set. Without it the cache
// grows for the life of the device, because a deleted file leaves its entry.
//
// skip names the path prefixes that this pass did not look at. An unpaired device
// does not read the _fleet tree. A playlist that failed to read named no file at
// all. The hashes under those prefixes must stay.
func (c *hashCache) keep(live map[string]bool, skip []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for k := range c.entries {
		if live[k] || hasAnyPrefix(k, skip) {
			continue
		}
		delete(c.entries, k)
		c.version++
	}
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(value, p) {
			return true
		}
	}
	return false
}

// save writes the cache when it changed. It gives the fault of the write, so the
// caller can say it once. The cache is an optimisation: a device that cannot write
// it still plays.
//
// The version counter and not a flag. The lock is free while the file is written, so
// a put can land in the middle. A flag that went to false afterwards would say that
// the change is on the disk, and it is not. A write that failed also keeps the
// counter, so the next pass tries again: one failed write once made a cache that
// nobody could save again, and the device then hashed the whole card at every boot.
func (c *hashCache) save() error {
	c.mu.Lock()
	if c.version == c.saved {
		c.mu.Unlock()
		return nil
	}
	version := c.version
	data, err := json.Marshal(c.entries)
	c.mu.Unlock()
	if err != nil {
		return err
	}

	if err := fsutil.WriteFileAtomic(c.path, data, 0o644); err != nil {
		return err
	}
	c.mu.Lock()
	c.saved = version
	c.mu.Unlock()
	return nil
}

// CachedSHA gives the hash of a file, if the cache holds a hash for this exact
// file. abs is an absolute path, and a path that is not under the media root gives
// no answer.
//
// It takes an absolute path for the same reason that NoteSHA does: the key of the
// cache is the path under the media root, and every caller that made that key
// itself made it without the guard against a path outside the root.
//
// The fleet client asks before it reads a video of 1 GB again. An object of the
// fleet store counts as present only when its hash matches, and the cache already
// holds that answer for every object that a fleet playlist names (D24).
func (l *Library) CachedSHA(abs string, size, modNS int64) (string, bool) {
	rel := l.rel(abs)
	if rel == "" {
		return "", false
	}
	return l.cache.get(rel, size, modNS)
}

// FindSHA gives the absolute path of a file under the media root that holds this
// hash, among the files that the hash cache knows.
//
// A file that is already on the card under any name with a matching SHA-256 is
// never fetched again (plan section 12). The file can be in a local playlist, and
// then the fleet client copies it in place of a download.
func (l *Library) FindSHA(sha string) (string, bool) {
	for _, c := range l.cache.find(sha) {
		abs := filepath.Join(l.opt.MediaRoot, filepath.FromSlash(c.rel))
		// The cache can name a file that changed or went away since the last pass. A
		// copy of the wrong bytes would give an object that fails its hash check.
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() || info.Size() != c.size || info.ModTime().UnixNano() != c.modNS {
			continue
		}
		return abs, true
	}
	return "", false
}

// NoteSHA records the hash of a file that another package wrote under the media
// root. The fleet client knows the hash of every object that it puts in the store,
// so the background pass must not read the file one more time.
func (l *Library) NoteSHA(abs, sha string) {
	rel := l.rel(abs)
	if rel == "" || sha == "" {
		return
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return
	}
	l.cache.put(rel, info.Size(), info.ModTime().UnixNano(), sha)
}

// pendingFile is one file that needs a hash.
type pendingFile struct {
	rel   string
	abs   string
	size  int64
	modNS int64
}

// hashPending computes the hash of every file that the cache does not know. It
// stops when done is closed, inside a file and between two files. A 1 GB video on
// a Pi takes about a minute. A shutdown that waits for it is a shutdown that the
// system kills before the cache is written.
func (l *Library) hashPending(done <-chan struct{}) {
	l.mu.RLock()
	snap := l.snap
	l.mu.RUnlock()

	var pending []pendingFile
	live := make(map[string]bool)
	for _, p := range snap.Playlists {
		for _, it := range p.Items {
			if it.path == "" || it.Missing {
				continue
			}
			rel := l.rel(it.path)
			if rel == "" {
				continue
			}
			live[rel] = true
			if _, ok := l.cache.get(rel, it.Size, it.modNS); !ok {
				pending = append(pending, pendingFile{rel: rel, abs: it.path, size: it.Size, modNS: it.modNS})
			}
		}
	}
	l.prune(snap, live)

	for _, f := range pending {
		select {
		case <-done:
			l.saveCache()
			return
		case <-time.After(hashPause):
		}
		// The size and the time come from after the read, not from the scan. A
		// file that was replaced while we read it would otherwise get the hash of
		// its new content under the size and the time of the old content.
		sha, size, modNS, err := hashFile(f.abs, done)
		if err != nil {
			if errors.Is(err, errStopping) {
				l.saveCache()
				return
			}
			l.log("library.hash.fail", f.rel+": "+err.Error())
			continue
		}
		l.cache.put(f.rel, size, modNS, sha)
	}
	l.saveCache()
}

// prune drops the cache entries of files that are gone.
//
// A file that this pass did not look at is not a file that is gone. Pruning
// against an incomplete set would throw the cache away, and the device would then
// hash the whole card again at 4 files a second.
//
// Three cases are incomplete. A media root that we cannot read gives a fault of
// the root itself: the card may not be mounted yet. A playlist that we cannot read
// names no file. An unpaired device does not read the _fleet tree at all, and the
// objects there must keep their hashes for the next pairing (D24).
func (l *Library) prune(snap Snapshot, live map[string]bool) {
	var skip []string
	for _, p := range snap.Problems {
		if p.Playlist == "" {
			l.log("library.cache.keep", "the media root has a fault, so the hash cache is not trimmed")
			return
		}
		skip = append(skip, p.Playlist+"/", FleetDir+"/"+p.Playlist+"/")
	}
	if !l.opt.Paired() {
		skip = append(skip, FleetDir+"/")
	}
	l.cache.keep(live, skip)
}

// saveCache writes the cache and says once when it cannot.
func (l *Library) saveCache() {
	err := l.cache.save()

	l.cache.mu.Lock()
	failed := l.cache.failed
	l.cache.failed = err != nil
	l.cache.mu.Unlock()

	if err != nil && !failed {
		l.log("library.cache.save.fail", err.Error()+"; the device will hash these files again after a restart")
	}
}

// hashFile computes the SHA-256 of a file and gives the size and the modification
// time that the file had after the read.
//
// It ends with errStopping when done is closed. store.HashFile has no way to stop,
// and its signature is used by the fleet client, so the reader that can stop lives
// here.
func hashFile(path string, done <-chan struct{}) (sha string, size, modNS int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, &stopReader{r: f, done: done}); err != nil {
		return "", 0, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		return "", 0, 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), info.Size(), info.ModTime().UnixNano(), nil
}

// stopReader ends a read when done is closed. io.Copy reads in blocks of 32 kB,
// so the test happens often enough to stop inside a large file.
type stopReader struct {
	r    io.Reader
	done <-chan struct{}
}

func (s *stopReader) Read(p []byte) (int, error) {
	select {
	case <-s.done:
		return 0, errStopping
	default:
	}
	return s.r.Read(p)
}
