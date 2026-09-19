package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/store"
)

// CacheName is the name of the hash cache in the state directory. The cache is
// on ext4, not on the media partition: it is our bookkeeping and it must not
// appear on a card that a person reads on a laptop.
const CacheName = "hashcache.json"

// hashPause is the rest between two files. Hashing is background work: it must
// never take the processor away from the browser. 4 files a second is fast
// enough to hash a full card in a few minutes and slow enough to be invisible.
const hashPause = 250 * time.Millisecond

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
	dirty   bool
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

// put adds a hash.
func (c *hashCache) put(rel string, size, modNS int64, sha string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[rel] = cacheEntry{Size: size, ModNS: modNS, SHA256: sha}
	c.dirty = true
}

// keep removes every entry that is not in the live set. Without it the cache
// grows for the life of the device, because a deleted file leaves its entry.
func (c *hashCache) keep(live map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for k := range c.entries {
		if !live[k] {
			delete(c.entries, k)
			c.dirty = true
		}
	}
}

// save writes the cache when it changed. A failure is not reported: the cache is
// an optimisation, and a device that cannot write it still plays.
func (c *hashCache) save() {
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return
	}
	data, err := json.Marshal(c.entries)
	c.dirty = false
	c.mu.Unlock()

	if err == nil {
		fsutil.WriteFileAtomic(c.path, data, 0o644)
	}
}

// pendingFile is one file that needs a hash.
type pendingFile struct {
	rel   string
	abs   string
	size  int64
	modNS int64
}

// hashPending hashes every file that the cache does not know. It stops when done
// is closed, so a shutdown does not wait for a 1 GB video.
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
	l.cache.keep(live)

	for _, f := range pending {
		select {
		case <-done:
			l.cache.save()
			return
		case <-time.After(hashPause):
		}
		sha, err := store.HashFile(f.abs)
		if err != nil {
			l.log("library.hash.fail", f.rel+": "+err.Error())
			continue
		}
		l.cache.put(f.rel, f.size, f.modNS, sha)
	}
	l.cache.save()
}
