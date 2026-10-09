package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/device/fallback"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/opslog"
)

func TestMain(m *testing.M) {
	if slices.Contains(os.Args, fakeFlag) {
		os.Exit(runFakeMPV(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// waitFor waits until check is true. The deadline is generous, because
// `go test ./...` runs the packages together and each of these tests starts a
// process. A test that is right must never fail because the machine was busy.
func waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// clock is the time of a test. Only the test moves it.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// harness runs a Supervisor with the fake mpv, a fast tick and a clock that only
// the test moves.
type harness struct {
	t       *testing.T
	sup     *Supervisor
	clock   *clock
	log     *opslog.Log
	run     string
	drm     string
	done    chan struct{}
	stopped chan struct{}

	mu       sync.Mutex
	manifest library.PlayerManifest
	info     fallback.Info
	renders  []fallback.Info
	reboots  []string
	nightly  string
	covered  bool
	display  DisplaySettings
}

// newHarness starts a supervisor. opts may change the options before New.
func newHarness(t *testing.T, m library.PlayerManifest, opts func(*Options, *harness)) *harness {
	t.Helper()
	// A short path: a unix socket path has a limit of about 100 bytes, also on
	// Windows, and t.TempDir() holds the name of the test.
	run, err := os.MkdirTemp("", "pp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(run) })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t:        t,
		clock:    &clock{t: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)},
		log:      opslog.New(filepath.Join(run, "ops.log")),
		run:      run,
		drm:      t.TempDir(),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
		manifest: m,
		info:     fallback.Info{Name: "Lobby", DeviceID: "px-1234abcd", URL: "http://lobby.local/"},
		display:  DisplaySettings{VideoOutput: OutputAuto},
	}
	writeFile(t, filepath.Join(h.drm, "card0", "device", "uevent"), "DRIVER=virtio_gpu\n")
	o := Options{
		// Quotation marks, because a temporary path can hold a space.
		Command:      CommandConfig{Override: fmt.Sprintf("%q %s", self, fakeFlag), RunDir: run},
		Log:          h.log,
		Now:          h.clock.Now,
		Tick:         2 * time.Millisecond,
		DisplayProbe: 5 * time.Second,
		DRMRoot:      h.drm,
		ModelPath:    filepath.Join(run, "no-model"),
		Manifest:     func() library.PlayerManifest { h.mu.Lock(); defer h.mu.Unlock(); return h.manifest },
		Fallback:     func() fallback.Info { h.mu.Lock(); defer h.mu.Unlock(); return h.info },
		Render: func(info fallback.Info, w, h2 int) ([]byte, error) {
			h.mu.Lock()
			h.renders = append(h.renders, info)
			h.mu.Unlock()
			return []byte(fmt.Sprintf("PNG %dx%d %s", w, h2, info.Now.Format("15:04"))), nil
		},
		Display: func() DisplaySettings { h.mu.Lock(); defer h.mu.Unlock(); return h.display },
		Reboot: func(reason string) {
			h.mu.Lock()
			h.reboots = append(h.reboots, reason)
			h.mu.Unlock()
		},
		NightlyRestart:  func() string { h.mu.Lock(); defer h.mu.Unlock(); return h.nightly },
		ScreenOffCovers: func(time.Time) bool { h.mu.Lock(); defer h.mu.Unlock(); return h.covered },
	}
	if opts != nil {
		opts(&o, h)
	}
	h.sup = New(o)
	go func() {
		h.sup.Run(h.done)
		close(h.stopped)
	}()
	// Wait for Run to end. Its shutdown stops the fake, and a fake that nobody
	// stops takes the processor from the tests that come after.
	t.Cleanup(func() {
		close(h.done)
		select {
		case <-h.stopped:
		case <-time.After(20 * time.Second):
			t.Error("the supervisor did not stop and its mpv may still run")
		}
	})
	return h
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setManifest changes what must play and tells the supervisor.
func (h *harness) setManifest(m library.PlayerManifest) {
	h.mu.Lock()
	h.manifest = m
	h.mu.Unlock()
	h.sup.PlaylistChanged()
}

func (h *harness) rebootCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.reboots)
}

func (h *harness) renderCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.renders)
}

// ctl sends one command to the fake on a connection of its own and gives the
// data of the answer.
func (h *harness) ctl(args ...any) json.RawMessage {
	h.t.Helper()
	data, err := h.call(args...)
	if err != nil {
		h.t.Fatal(err)
	}
	return data
}

// call is ctl with an error in place of a failed test. The fake does not listen
// yet in the first moments of a test.
func (h *harness) call(args ...any) (json.RawMessage, error) {
	conn, err := net.DialTimeout("unix", filepath.Join(h.run, SocketDirName, SocketName), time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to the fake: %w", err)
	}
	defer conn.Close()
	line, _ := json.Marshal(map[string]any{"command": args, "request_id": 9999})
	conn.Write(append(line, '\n'))
	if s, _ := args[0].(string); s == "fake-exit" {
		return nil, nil
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Event == "" && m.RequestID == 9999 {
			return m.Data, nil
		}
	}
	return nil, fmt.Errorf("the fake did not answer %v", args)
}

// dump is the state of the fake.
type dump struct {
	Args  []string    `json:"args"`
	List  []fakeEntry `json:"list"`
	Loads int         `json:"loads"`
	Pos   int         `json:"pos"`
}

func (h *harness) dump() dump {
	h.t.Helper()
	var d dump
	if err := json.Unmarshal(h.ctl("fake-dump"), &d); err != nil {
		h.t.Fatalf("dump: %v", err)
	}
	return d
}

// tryDump is dump for a fake that may not listen yet.
func (h *harness) tryDump() (dump, bool) {
	var d dump
	data, err := h.call("fake-dump")
	return d, err == nil && json.Unmarshal(data, &d) == nil
}

// playing gives the item on the screen, or nil.
func (h *harness) playing() (string, int) {
	np := h.sup.State().NowPlaying
	if np == nil {
		return "", -1
	}
	return np.Item, np.Index
}

// waitPlaying waits until the item index is on the screen.
func (h *harness) waitPlaying(index int) {
	h.t.Helper()
	waitFor(h.t, fmt.Sprintf("item %d on the screen", index), func() bool {
		_, i := h.playing()
		return i == index
	})
}

// advance moves the clock and waits until the supervisor took the answer of
// the poll that the new time causes. d must be at least pollEvery.
func (h *harness) advance(d time.Duration) {
	h.t.Helper()
	h.clock.Advance(d)
	now := h.clock.Now()
	waitFor(h.t, "the answer to the poll at "+now.Format("15:04:05"), func() bool {
		return !h.sup.heardAt().Before(now) || h.sup.State().Player != StateRunning
	})
}

// settle waits until mpv answered every request. A test calls it before a jump
// of the clock that is longer than the timeout: an answer that is still on its
// way would then look like silence. The time of a device does not jump.
func (h *harness) settle() {
	h.t.Helper()
	h.advance(pollEvery)
}

// events gives the ops log, for the message of a test that fails.
func (h *harness) events() string {
	var b strings.Builder
	for _, e := range h.log.Tail(200) {
		b.WriteString(e.Event + " " + e.Details + "\n")
	}
	return b.String()
}

func (h *harness) countEvent(name string) int {
	n := 0
	for _, e := range h.log.Tail(1200) {
		if e.Event == name {
			n++
		}
	}
	return n
}

func (h *harness) eventWith(name, text string) bool {
	for _, e := range h.log.Tail(1200) {
		if e.Event == name && strings.Contains(e.Details, text) {
			return true
		}
	}
	return false
}

// playlist makes a manifest of items. A name that ends in .mp4 is a video.
func playlist(name, transition string, ms int, items ...library.ManifestItem) library.PlayerManifest {
	for i := range items {
		items[i].Index = i
		if items[i].Kind == "" {
			items[i].Kind = kindImage
			if strings.HasSuffix(items[i].Name, ".mp4") {
				items[i].Kind = kindVideo
			}
		}
		if items[i].Path == "" {
			items[i].Path = "/media/" + name + "/" + items[i].Name
		}
		if items[i].Kind == kindImage && items[i].Duration == 0 {
			items[i].Duration = 10
		}
	}
	return library.PlayerManifest{Playlist: &library.ManifestPlaylist{
		Name: name, Title: name, Transition: transition, TransitionMS: ms, Items: items,
	}}
}
