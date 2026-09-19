package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/identity"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/opslog"
	"github.com/ethanpil/portapixel/internal/store"
)

// fakeServer is a fleet server for the tests. It answers the three routes of the
// device API with the real wire types of internal/manifest, so a change of the
// contract breaks these tests and not only the server.
type fakeServer struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// enrollToken pairs a device at once. An empty value means that no token
	// pairs.
	enrollToken string
	deviceToken string
	claimSecret string
	pairingCode string
	// approved lets a request that waits become paired at its next poll.
	approved bool
	// revoked makes every call with the device token answer 401.
	revoked bool
	// objectsFail makes every object request answer 500. A test that must prove a
	// copy from the card sets it: a download would then hide the copy.
	objectsFail bool

	man manifest.Manifest
	// objects holds the bytes of each object, by hash. served replaces the bytes
	// that go out, so a test can send content that does not match its hash.
	objects map[string][]byte
	served  map[string][]byte

	enrolls      []manifest.EnrollRequest
	beats        []manifest.Heartbeat
	manifestHits int
	objectHits   map[string]int
	// contentTypes holds the Content-Type of every request with a body.
	contentTypes []string
	// ranges holds the Range header of every object request.
	ranges []string
	// ignoreRange answers the whole object even when the request asks for a range.
	ignoreRange bool
	// cut is the number of bytes to send before the connection drops. It goes back
	// to zero after one drop, so the next attempt works.
	cut int64
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{
		t:           t,
		enrollToken: "an enrollment token",
		deviceToken: "a device token",
		pairingCode: "K7M2QP",
		objects:     map[string][]byte{},
		served:      map[string][]byte{},
		objectHits:  map[string]int{},
		man:         manifest.Manifest{ServerName: "Test fleet", PollSeconds: 60},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+EnrollPath, f.enroll)
	mux.HandleFunc("GET "+ManifestPath, f.manifest)
	mux.HandleFunc("POST "+HeartbeatPath, f.heartbeat)
	mux.HandleFunc("GET /api/v1/media/{sha}", f.object)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// addObject puts one object in the library of the server and gives its hash.
func (f *fakeServer) addObject(name, content string) manifest.MediaRef {
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])

	f.mu.Lock()
	f.objects[sha] = []byte(content)
	f.mu.Unlock()

	return manifest.MediaRef{
		SHA256: sha,
		Size:   int64(len(content)),
		Name:   name,
		URL:    "/api/v1/media/" + sha,
	}
}

// setManifest replaces the manifest that the next poll gets.
func (f *fakeServer) setManifest(m manifest.Manifest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.ServerName == "" {
		m.ServerName = "Test fleet"
	}
	// PollSeconds stays as the test gave it. A zero value is the server that names
	// no interval, and then the device takes the value of the TOML.
	f.man = m
}

func (f *fakeServer) enroll(w http.ResponseWriter, r *http.Request) {
	var req manifest.EnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.contentTypes = append(f.contentTypes, r.Header.Get("Content-Type"))
	f.enrolls = append(f.enrolls, req)

	switch {
	case req.Token != "" && req.Token == f.enrollToken:
		writeJSON(w, manifest.EnrollResponse{Status: "paired", DeviceToken: f.deviceToken})
	case req.Token != "" && req.Token == f.claimSecret:
		if f.approved {
			f.claimSecret = ""
			writeJSON(w, manifest.EnrollResponse{Status: "paired", DeviceToken: f.deviceToken})
			return
		}
		writeJSON(w, manifest.EnrollResponse{
			Status: "pending", PairingCode: f.pairingCode, ClaimSecret: req.Token,
		})
	case req.Token == "":
		f.claimSecret = "a claim secret"
		writeJSON(w, manifest.EnrollResponse{
			Status: "pending", PairingCode: f.pairingCode, ClaimSecret: f.claimSecret,
		})
	default:
		writeRevoked(w, "this token is not valid")
	}
}

func (f *fakeServer) manifest(w http.ResponseWriter, r *http.Request) {
	if !f.checkToken(w, r) {
		return
	}
	f.mu.Lock()
	f.manifestHits++
	m := f.man
	f.mu.Unlock()
	writeJSON(w, m)
}

func (f *fakeServer) heartbeat(w http.ResponseWriter, r *http.Request) {
	if !f.checkToken(w, r) {
		return
	}
	var hb manifest.Heartbeat
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.contentTypes = append(f.contentTypes, r.Header.Get("Content-Type"))
	f.beats = append(f.beats, hb)
	f.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

// object serves one object with Range support, and with the two faults that the
// tests need: a connection that drops in the middle, and bytes that do not match
// the hash.
func (f *fakeServer) object(w http.ResponseWriter, r *http.Request) {
	if !f.checkToken(w, r) {
		return
	}
	sha := path.Base(r.URL.Path)

	f.mu.Lock()
	if f.objectsFail {
		f.mu.Unlock()
		http.Error(w, "no object here", http.StatusInternalServerError)
		return
	}
	data, ok := f.objects[sha]
	if replacement, swapped := f.served[sha]; swapped {
		data = replacement
	}
	f.objectHits[sha]++
	f.ranges = append(f.ranges, r.Header.Get("Range"))
	ignoreRange, cut := f.ignoreRange, f.cut
	if cut > 0 {
		f.cut = 0
	}
	f.mu.Unlock()

	if !ok {
		http.Error(w, `{"error":"no such object"}`, http.StatusNotFound)
		return
	}

	var start int64
	if value := r.Header.Get("Range"); value != "" && !ignoreRange {
		if _, err := fmt.Sscanf(value, "bytes=%d-", &start); err != nil || start < 0 {
			start = 0
		}
		if start > int64(len(data)) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)-int(start)))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
	}

	body := data[start:]
	if cut > 0 && int64(len(body)) > cut {
		w.Write(body[:cut])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// The connection drops in the middle of the object. The device keeps the
		// part file and sends a Range request at its next try (D24).
		panic(http.ErrAbortHandler)
	}
	w.Write(body)
}

func (f *fakeServer) checkToken(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	want, revoked := f.deviceToken, f.revoked
	f.mu.Unlock()

	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if revoked || got == "" || got != want {
		writeRevoked(w, "this device token is not valid")
		return false
	}
	return true
}

// writeRevoked is the 401 of the fleet API: the token itself is gone. The device drops
// its pairing only for this answer, and not for a 401 of a proxy in between.
func writeRevoked(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `{"error":%q,"code":%q}`, message, TokenRevokedCode)
}

func writeJSON(w http.ResponseWriter, body any) {
	data, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// ------------------------------------------------------------------ the device

// dev is one device under test: real directories, a real library with its hash
// cache, and a real Syncer. Everything that the daemon owns is a field, so a test
// reads what the device asked the daemon to do.
type dev struct {
	t     *testing.T
	media string
	state string
	lib   *library.Library
	s     *Syncer

	cfg config.Config
	st  identity.State

	rescans      int
	cleared      int
	fleetDefault string
	fleetRules   []manifest.Rule
	fleetScreen  *manifest.ScreenRule
	commands     []string
	updates      int
	updateErr    error
	savedURL     string
	savedToken   string
	saves        int
	// free is the free space that the fake partition reports.
	free uint64
	// renameErr fails a rename, to prove that a swap cannot leave half a set.
	renameErr func(oldPath, newPath string) error
	now       time.Time
}

// newDev makes a device that talks to a server.
func newDev(t *testing.T, f *fakeServer) *dev {
	t.Helper()
	dir := t.TempDir()
	return newDevIn(t, f, filepath.Join(dir, "media"), filepath.Join(dir, "state"))
}

// newDevIn makes a device over directories that a test already has. A restart of a
// device is a second call with the same two directories.
func newDevIn(t *testing.T, f *fakeServer, media, state string) *dev {
	t.Helper()
	for _, d := range []string{media, state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	d := &dev{
		t:     t,
		media: media,
		state: state,
		cfg:   config.Default(),
		free:  1 << 40,
		now:   time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
	}
	d.cfg.Device.Name = "Lobby"
	if f != nil {
		d.cfg.Server.URL = f.srv.URL
	}

	stored, err := identity.LoadState(state)
	if err != nil {
		t.Fatal(err)
	}
	d.st = stored

	log := opslog.New(filepath.Join(state, "ops.log"))
	d.lib = library.New(library.Options{
		MediaRoot: media,
		StateDir:  state,
		Log:       log,
		Paired:    func() bool { return d.st.Paired() },
	})
	d.lib.Rescan()

	d.s = New(Options{
		MediaRoot: media,
		Log:       log,
		Client:    &http.Client{},
		Now:       func() time.Time { return d.now },
		// No jitter in a test: a test that compares an interval must get the same
		// number every time.
		Jitter:   func() float64 { return 0.5 },
		Identity: identity.Identity{DeviceID: "px-1a2b3c4d", HardwareID: strings.Repeat("ab", 32)},
		Version:  "1.5.0",
		Config:   func() config.Config { return d.cfg },
		State:    func() identity.State { return d.st },
		SaveState: func(change func(*identity.State)) error {
			change(&d.st)
			return d.st.Save(d.state)
		},
		SaveServer: func(url, token string) error {
			d.savedURL, d.savedToken = url, token
			d.cfg.Server.URL, d.cfg.Server.Token = url, token
			d.saves++
			return nil
		},
		Status: func() manifest.Status {
			return manifest.Status{DeviceID: "px-1a2b3c4d", Name: d.cfg.Device.Name}
		},
		SetFleetRules: func(def string, rules []manifest.Rule, screen *manifest.ScreenRule) {
			d.fleetDefault, d.fleetRules, d.fleetScreen = def, rules, screen
		},
		ClearFleetRules: func() { d.cleared++ },
		Rescan:          func() { d.rescans++; d.lib.Rescan() },
		Command:         func(name string) error { d.commands = append(d.commands, name); return nil },
		Update: func(ctx context.Context) error {
			d.updates++
			return d.updateErr
		},
		CachedSHA: d.lib.CachedSHA,
		FindSHA:   d.lib.FindSHA,
		NoteSHA:   d.lib.NoteSHA,
		FreeBytes: func(string) (uint64, error) { return d.free, nil },
		Rename: func(oldPath, newPath string) error {
			if d.renameErr != nil {
				if err := d.renameErr(oldPath, newPath); err != nil {
					return err
				}
			}
			return os.Rename(oldPath, newPath)
		},
	})
	return d
}

// fleetPath gives a path under _fleet.
func (d *dev) fleetPath(parts ...string) string {
	return filepath.Join(append([]string{d.media, library.FleetDir}, parts...)...)
}

// objectPath gives the store path of one object of the server.
func (d *dev) objectPath(ref manifest.MediaRef) string {
	name, err := store.ObjectName(ref.SHA256, ref.Name)
	if err != nil {
		d.t.Fatal(err)
	}
	return d.fleetPath(library.FleetMediaDir, name)
}

// readFleetPlaylist gives the text of one fleet playlist.toml.
func (d *dev) readFleetPlaylist(name string) string {
	data, err := os.ReadFile(d.fleetPath(name, "playlist.toml"))
	if err != nil {
		d.t.Fatalf("read the fleet playlist %s: %v", name, err)
	}
	return string(data)
}

// readState reads state.json from the disk. A test that says "the device
// remembers" must read the file and not the value in memory.
func readState(t *testing.T, stateDir string) identity.State {
	t.Helper()
	st, err := identity.LoadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// write puts one file under the media root and makes the directories on the way.
func (d *dev) write(name, content string) string {
	d.t.Helper()
	p := filepath.Join(d.media, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		d.t.Fatal(err)
	}
	return p
}
