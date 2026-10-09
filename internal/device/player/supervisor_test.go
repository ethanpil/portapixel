package player

import (
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/device/fallback"
	"github.com/ethanpil/portapixel/internal/device/library"
	"github.com/ethanpil/portapixel/internal/playlist"
)

func videos() library.PlayerManifest {
	return manifestOf("clips", "fade", 400,
		library.ManifestItem{Name: "a.mp4"}, library.ManifestItem{Name: "b.mp4"})
}

func threeItems() library.PlayerManifest {
	return manifestOf("lobby", "fade", 400,
		library.ManifestItem{Name: "welcome.jpg", Duration: 15},
		library.ManifestItem{Name: "promo.mp4", Mute: true, MaxDuration: 30},
		library.ManifestItem{Name: "tour.mp4"},
	)
}

// The supervisor gives mpv the whole playlist, each item with its own options,
// and the transition of the playlist goes to transitions.lua.
func TestPlaylistLoadsWithFileOptions(t *testing.T) {
	h := newHarness(t, threeItems(), nil)
	h.waitPlaying(0)

	d := h.dump()
	if len(d.List) != 3 {
		t.Fatalf("mpv has %d entries, want 3: %+v", len(d.List), d.List)
	}
	for i, name := range []string{"welcome.jpg", "promo.mp4", "tour.mp4"} {
		if want := "/media/lobby/" + name; d.List[i].Path != want {
			t.Errorf("entry %d is %q, want %q", i, d.List[i].Path, want)
		}
		if got := d.List[i].Opts["script-opts"]; got != "pptr-kind=fade,pptr-ms=400" {
			t.Errorf("entry %d script-opts = %q", i, got)
		}
	}
	img, promo, tour := d.List[0].Opts, d.List[1].Opts, d.List[2].Opts
	if img["image-display-duration"] != "15" {
		t.Errorf("image options = %v", img)
	}
	if promo["mute"] != "yes" || promo["end"] != "30" || promo["loop-file"] != "" {
		t.Errorf("muted video with a cap: options = %v", promo)
	}
	if tour["mute"] != "no" || tour["end"] != "" || tour["vd-lavc-o"] != "" {
		t.Errorf("video options = %v", tour)
	}

	// The command line: the override program gets the arguments of the daemon
	// first, and the socket and the script are in the run directory.
	for _, want := range []string{"--input-ipc-server=" + h.sup.opt.Command.SocketPath(),
		"--script=" + h.sup.opt.Command.ScriptPath(), "--vo=drm", "--loop-playlist=inf", "--hwdec=auto-safe"} {
		if !slices.Contains(d.Args, want) {
			t.Errorf("the arguments %q do not hold %q", d.Args, want)
		}
	}
	if data, err := os.ReadFile(h.sup.opt.Command.ScriptPath()); err != nil || !strings.Contains(string(data), "on_unload") {
		t.Errorf("the transition script is not in the run directory: %v", err)
	}
	st := h.sup.State()
	if st.Player != StateRunning || st.VideoOutput != OutputDRM {
		t.Errorf("state = %+v, want running on drm (virtio_gpu has no GL driver)", st)
	}
	if !h.sup.Started() {
		t.Error("Started is false after the first picture")
	}
}

// The supervisor reads the board model one time and gives it to the command
// line: a Raspberry Pi gets the hardware decoder of the SoC. The video items, and
// only they, get the smaller buffer count of that decoder.
func TestTheBoardModelChoosesTheDecoder(t *testing.T) {
	h := newHarness(t, threeItems(), func(o *Options, h *harness) {
		o.ModelPath = filepath.Join(h.run, "model")
		writeFile(t, o.ModelPath, "Raspberry Pi Zero 2 W Rev 1.0\x00")
	})
	h.waitPlaying(0)
	d := h.dump()
	if !slices.Contains(d.Args, "--hwdec=v4l2m2m-copy") {
		t.Errorf("the arguments %q do not hold --hwdec=v4l2m2m-copy", d.Args)
	}
	for _, a := range d.Args {
		if strings.HasPrefix(a, "--vd-lavc-o") {
			t.Errorf("the command line holds %q; it must be on the video items only", a)
		}
	}
	for i, want := range []string{"", "num_capture_buffers=8", "num_capture_buffers=8"} {
		if got := d.List[i].Opts["vd-lavc-o"]; got != want {
			t.Errorf("entry %d (%s) vd-lavc-o = %q, want %q", i, d.List[i].Path, got, want)
		}
	}
}

// The script works when an item ends, so the transition INTO an item goes to the
// entry before it. An item can name its own transition and its own length. The
// last entry gets the transition into the first one, because the list loops.
func TestItemTransitionsGoToTheEntryBefore(t *testing.T) {
	h := newHarness(t, manifestOf("lobby", "fade", 400,
		library.ManifestItem{Name: "a.jpg"},
		library.ManifestItem{Name: "b.jpg", Transition: "split", TransitionMS: 900},
		library.ManifestItem{Name: "c.mp4", TransitionMS: 250},
	), nil)
	h.waitPlaying(0)
	check := func(want ...string) {
		t.Helper()
		d := h.dump()
		if len(d.List) != len(want) {
			t.Fatalf("mpv has %d entries, want %d: %+v", len(d.List), len(want), d.List)
		}
		for i, w := range want {
			if got := d.List[i].Opts["script-opts"]; got != w {
				t.Errorf("entry %d (%s) script-opts = %q, want %q", i, d.List[i].Path, got, w)
			}
		}
	}
	check("pptr-kind=split,pptr-ms=900", "pptr-kind=fade,pptr-ms=250", "pptr-kind=fade,pptr-ms=400")

	// A restart starts the list at the next item. The words stay with their
	// items, and the list is b, c, a now.
	first := h.sup.proc.pid()
	h.ctl("fake-exit", 9)
	waitFor(t, "a new mpv", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	h.waitPlaying(1)
	check("pptr-kind=fade,pptr-ms=250", "pptr-kind=fade,pptr-ms=400", "pptr-kind=split,pptr-ms=900")
}

// Ken Burns is a choice of the playlist. On vo=gpu each image that shows for a
// while gets pptr-kb, the seconds that it shows. A video gets none, a playlist
// that did not ask gets none, and one image alone (it stays on the screen) gets
// none.
func TestKenBurnsGoesToTheImagesOnGPU(t *testing.T) {
	onGPU := func(o *Options, h *harness) { h.display = DisplaySettings{VideoOutput: OutputGPU} }
	m := threeItems() // welcome.jpg 15 s, promo.mp4, tour.mp4
	m.Playlist.KenBurns = true
	m.Playlist.Items[2].Name, m.Playlist.Items[2].Kind, m.Playlist.Items[2].Duration = "tour.jpg", playlist.KindImage, 6
	h := newHarness(t, m, onGPU)
	h.waitPlaying(0)
	d := h.dump()
	want := []string{
		"pptr-kind=fade,pptr-ms=400,pptr-kb=15",
		"pptr-kind=fade,pptr-ms=400",
		"pptr-kind=fade,pptr-ms=400,pptr-kb=6",
	}
	for i, w := range want {
		if got := d.List[i].Opts["script-opts"]; got != w {
			t.Errorf("entry %d script-opts = %q, want %q", i, got, w)
		}
	}
	if h.countEvent("player.kenburns.off") != 0 {
		t.Errorf("the ops log says that Ken Burns is off:\n%s", h.events())
	}

	// One image alone has no end, so it gets no Ken Burns.
	one := manifestOf("one", "fade", 400, library.ManifestItem{Name: "only.jpg"})
	one.Playlist.KenBurns = true
	h.setManifest(one)
	waitFor(t, "the single image", func() bool { item, _ := h.playing(); return item == "only.jpg" })
	if got := h.dump().List[0].Opts["script-opts"]; got != "pptr-kind=fade,pptr-ms=400" {
		t.Errorf("one image has script-opts = %q, want no pptr-kb", got)
	}
}

func TestNoKenBurnsWhenThePlaylistDoesNotAsk(t *testing.T) {
	h := newHarness(t, threeItems(), func(o *Options, h *harness) { h.display = DisplaySettings{VideoOutput: OutputGPU} })
	h.waitPlaying(0)
	for i, e := range h.dump().List {
		if strings.Contains(e.Opts["script-opts"], "pptr-kb") {
			t.Errorf("entry %d has %q", i, e.Opts["script-opts"])
		}
	}
}

// vo=drm scales the picture in software at each step of the zoom, so Ken Burns
// is off there. The ops log says so one time, and every transition goes on.
func TestKenBurnsIsOffOnDRM(t *testing.T) {
	m := threeItems()
	m.Playlist.KenBurns = true
	h := newHarness(t, m, nil) // the fake driver is virtio_gpu: vo=drm
	h.waitPlaying(0)
	if st := h.sup.State(); st.VideoOutput != OutputDRM {
		t.Fatalf("the output is %q, want drm", st.VideoOutput)
	}
	for i, e := range h.dump().List {
		if e.Opts["script-opts"] != "pptr-kind=fade,pptr-ms=400" {
			t.Errorf("entry %d script-opts = %q", i, e.Opts["script-opts"])
		}
	}
	if n := h.countEvent("player.kenburns.off"); n != 1 {
		t.Errorf("the ops log has %d lines about Ken Burns, want 1:\n%s", n, h.events())
	}
}

// One image alone stays on the screen and one video alone loops in its file:
// neither gets a transition into itself.
func TestSingleItemsStay(t *testing.T) {
	h := newHarness(t, manifestOf("one", "fade", 400, library.ManifestItem{Name: "only.jpg"}), nil)
	h.waitPlaying(0)
	if got := h.dump().List[0].Opts["image-display-duration"]; got != "inf" {
		t.Errorf("one image has image-display-duration=%q, want inf", got)
	}

	h.setManifest(manifestOf("clip", "fade", 400, library.ManifestItem{Name: "loop.mp4"}))
	waitFor(t, "the video", func() bool { item, _ := h.playing(); return item == "loop.mp4" })
	if got := h.dump().List[0].Opts["loop-file"]; got != "inf" {
		t.Errorf("one video has loop-file=%q, want inf", got)
	}
}

// now_playing follows mpv: the item, its kind and its start time, the decoder
// of a video, and the frames that it dropped.
func TestNowPlayingFollowsMPV(t *testing.T) {
	h := newHarness(t, threeItems(), nil)
	h.waitPlaying(0)
	start := h.clock.Now()

	np := h.sup.State().NowPlaying
	if np.Playlist != "lobby" || np.Item != "welcome.jpg" || np.Kind != playlist.KindImage || !np.Since.Equal(start) {
		t.Fatalf("now_playing = %+v", np)
	}
	if hw := h.sup.State().Hwdec; hw != "" {
		t.Errorf("hwdec = %q for an image, want \"\"", hw)
	}

	h.clock.Advance(15 * time.Second)
	h.ctl("fake-next")
	h.waitPlaying(1)
	np = h.sup.State().NowPlaying
	if np.Item != "promo.mp4" || np.Kind != playlist.KindVideo || !np.Since.Equal(h.clock.Now()) {
		t.Fatalf("now_playing = %+v", np)
	}
	waitFor(t, "the decoder of the video", func() bool { return h.sup.State().Hwdec == fakeHwdec })
	// The start values of the counters are in, before the counters move.
	h.advance(2 * time.Second)
	h.ctl("fake-drops", 5, 2)
	h.advance(2 * time.Second)
	if got := h.sup.State().NowPlaying.DroppedFrames; got != 7 {
		t.Errorf("dropped_frames = %d, want 7", got)
	}
	// The next item starts its own count.
	h.ctl("fake-next")
	h.waitPlaying(2)
	h.advance(2 * time.Second)
	h.ctl("fake-drops", 6, 2)
	h.advance(2 * time.Second)
	if got := h.sup.State().NowPlaying.DroppedFrames; got != 1 {
		t.Errorf("dropped_frames of the next video = %d, want 1", got)
	}
}

// A schedule change replaces the list. The same manifest again changes nothing:
// a rescan must not start the playlist from the top.
func TestPlaylistChangeReplacesTheList(t *testing.T) {
	h := newHarness(t, threeItems(), nil)
	h.waitPlaying(0)
	loads := h.dump().Loads

	h.setManifest(threeItems())
	h.advance(2 * time.Second) // the loop handled the message before this poll
	if got := h.dump().Loads; got != loads {
		t.Fatalf("the same manifest made %d new loads", got-loads)
	}

	h.setManifest(manifestOf("evening", "cut", 0,
		library.ManifestItem{Name: "night.jpg"}, library.ManifestItem{Name: "stars.mp4"}))
	waitFor(t, "the evening playlist", func() bool {
		np := h.sup.State().NowPlaying
		return np != nil && np.Playlist == "evening"
	})
	d := h.dump()
	if len(d.List) != 2 || !strings.HasSuffix(d.List[0].Path, "night.jpg") {
		t.Fatalf("mpv has %+v", d.List)
	}
	if got := d.List[1].Opts["script-opts"]; got != "pptr-kind=cut,pptr-ms=0" {
		t.Errorf("script-opts = %q", got)
	}
}

// With no content, mpv shows the fallback picture that the daemon draws. The
// picture is drawn again for a new minute and for new data, at the size of the
// display.
func TestFallbackScreen(t *testing.T) {
	h := newHarness(t, library.PlayerManifest{}, func(o *Options, h *harness) {
		writeFile(t, filepath.Join(h.drm, "card0-HDMI-A-1", "status"), "connected\n")
		writeFile(t, filepath.Join(h.drm, "card0-HDMI-A-1", "modes"), "1280x720\n1920x1080\n")
	})
	path := h.sup.opt.Command.FallbackPath()
	waitFor(t, "the fallback picture", func() bool {
		d, _ := h.tryDump()
		return len(d.List) == 1 && d.List[0].Path == path
	})
	opts := h.dump().List[0].Opts
	if opts["image-display-duration"] != "inf" || opts["script-opts"] != "pptr-kind=cut,pptr-ms=0" {
		t.Errorf("fallback options = %v", opts)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(data), "PNG 1280x720 12:00") {
		t.Fatalf("fallback.png = %q, %v; want the render at the mode of the connector", data, err)
	}
	waitFor(t, "the first picture", h.sup.Started)
	if h.sup.State().NowPlaying != nil {
		t.Error("now_playing is set while the fallback screen shows")
	}

	// A new minute draws the clock again.
	h.settle()
	n := h.renderCount()
	h.advance(61 * time.Second)
	waitFor(t, "a new render", func() bool { return h.renderCount() > n })
	waitFor(t, "the new clock in the file", func() bool {
		data, _ := os.ReadFile(path)
		return strings.Contains(string(data), "12:01")
	})

	// New data draws it again at once.
	n = h.renderCount()
	h.mu.Lock()
	h.info.PairingCode = "ABC234"
	h.mu.Unlock()
	h.advance(6 * time.Second)
	waitFor(t, "a render with the pairing code", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.renders) > n && h.renders[len(h.renders)-1].PairingCode == "ABC234"
	})

	// The layout moves a little every few minutes, against burn-in.
	h.settle()
	h.mu.Lock()
	shift := h.renders[len(h.renders)-1].Shift
	h.mu.Unlock()
	h.advance(shiftEvery)
	waitFor(t, "a shifted render", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.renders[len(h.renders)-1].Shift != shift
	})

	// Content comes back.
	h.setManifest(threeItems())
	h.waitPlaying(0)
}

// The burn-in step changes with the minute on the screen, so it needs no render
// of its own. It counted from the time that the fallback screen came, and a
// screen that came at 12:00:37 drew again at 12:03:37 with the same clock.
func TestTheBurnInStepChangesWithTheClock(t *testing.T) {
	h := newHarness(t, library.PlayerManifest{}, func(o *Options, h *harness) {
		h.clock.Set(time.Date(2026, 10, 8, 12, 0, 37, 0, time.UTC))
	})
	waitFor(t, "a render", func() bool { return h.renderCount() > 0 })
	h.settle()
	for range 48 { // four minutes in steps of 5 s
		h.advance(fallbackCheck)
	}
	h.mu.Lock()
	renders := slices.Clone(h.renders)
	h.mu.Unlock()
	for i := 1; i < len(renders); i++ {
		if renders[i].Now.Equal(renders[i-1].Now) {
			t.Errorf("render %d has the clock %s of the render before it; shift %d -> %d",
				i, renders[i].Now.Format("15:04"), renders[i-1].Shift, renders[i].Shift)
		}
	}
	if first, last := renders[0].Shift, renders[len(renders)-1].Shift; first == last {
		t.Errorf("the step stayed at %d for four minutes", first)
	}
}

// The check of the fallback screen reads the data of the daemon, and a redraw
// uses that data. A second read is a second report of the daemon, which reads
// the disk and the network interfaces.
func TestAFallbackRedrawReadsTheDataOneTime(t *testing.T) {
	h := newHarness(t, library.PlayerManifest{}, nil)
	waitFor(t, "a render", func() bool { return h.renderCount() > 0 })
	h.settle()
	h.mu.Lock()
	reads, renders := h.infoReads, len(h.renders)
	h.mu.Unlock()

	h.advance(61 * time.Second) // a new minute: one check and one redraw
	waitFor(t, "the redraw", func() bool { return h.renderCount() > renders })
	h.mu.Lock()
	got := h.infoReads - reads
	h.mu.Unlock()
	if got != 1 {
		t.Errorf("the check and the redraw read the data %d times, want 1", got)
	}
}

// If the daemon cannot draw the fallback picture, mpv has no file and sends no
// event. The loop must try again by itself, or the screen stays black.
func TestFallbackDrawFailureIsRetried(t *testing.T) {
	failures := 2
	h := newHarness(t, library.PlayerManifest{}, func(o *Options, h *harness) {
		o.Render = func(info fallback.Info, w, h2 int) ([]byte, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if failures > 0 {
				failures--
				return nil, errors.New("the render failed")
			}
			return []byte("PNG"), nil
		}
	})
	waitFor(t, "the first failed draw", func() bool { return h.eventWith("player.fallback.fail", "the render failed") })
	if h.sup.Started() {
		t.Fatal("Started is true while mpv shows nothing")
	}

	h.settle()
	h.advance(fallbackCheck + time.Second)
	h.advance(fallbackCheck + time.Second)
	path := h.sup.opt.Command.FallbackPath()
	waitFor(t, "the fallback picture", func() bool {
		d, ok := h.tryDump()
		return ok && len(d.List) == 1 && d.List[0].Path == path
	})
	waitFor(t, "the first picture", h.sup.Started)
}

// A draw of the fallback screen that fails leaves the content on the screen.
// When the same content comes back before the draw works again, the retry of
// the draw must not replace the content: it showed the fallback screen for
// hours, with content in the manifest.
func TestFallbackRetryDoesNotReplaceContentThatCameBack(t *testing.T) {
	fail := false
	h := newHarness(t, threeItems(), func(o *Options, h *harness) {
		o.Render = func(info fallback.Info, w, h2 int) ([]byte, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if fail {
				return nil, errors.New("the render failed")
			}
			return []byte("PNG"), nil
		}
	})
	h.waitPlaying(0)
	h.mu.Lock()
	fail = true
	h.mu.Unlock()
	h.setManifest(library.PlayerManifest{}) // a gap in the schedule
	waitFor(t, "the failed draw", func() bool { return h.eventWith("player.fallback.fail", "the render failed") })
	h.setManifest(threeItems()) // the schedule comes back to the same playlist
	h.settle()
	h.mu.Lock()
	fail = false
	h.mu.Unlock()
	h.advance(fallbackCheck + time.Second) // the time of the retry
	h.settle()
	if d := h.dump(); len(d.List) != 3 || filepath.Base(d.List[0].Path) != "welcome.jpg" {
		t.Fatalf("the content is gone from mpv: %+v", d.List)
	}
}

// When no item can play, mpv goes idle. The fallback screen then shows, each
// fault is in the ops log, and the player tries the list again later.
func TestEveryItemFails(t *testing.T) {
	m := manifestOf("bad", "fade", 400,
		library.ManifestItem{Name: "broken-a.mp4"}, library.ManifestItem{Name: "broken-b.jpg"})
	h := newHarness(t, m, nil)
	waitFor(t, "the fallback screen", func() bool {
		d, _ := h.tryDump()
		return len(d.List) == 1 && d.List[0].Path == h.sup.opt.Command.FallbackPath()
	})
	// One line for the playlist: the first fault, and a count for the others.
	if !h.eventWith("player.item.fail", "broken-a.mp4") || h.countEvent("player.item.fail") != 1 {
		t.Errorf("player.item.fail lines:\n%s", h.events())
	}
	if h.countEvent("player.playlist.fail") != 1 {
		t.Errorf("player.playlist.fail lines:\n%s", h.events())
	}

	h.settle()
	loads := h.dump().Loads
	h.advance(failedRetry)
	waitFor(t, "a second try of the playlist", func() bool { return h.dump().Loads >= loads+2 })
}

// A note goes in the ops log one time per hour for each event and playlist, with
// a count of the times that it came in between. The key held the details: each
// broken item was a note of its own, and more than noteMemory of them pushed
// each other out, so each pass of the loop wrote each fault to the flash again.
func TestManyBrokenItemsGiveOneLine(t *testing.T) {
	items := []library.ManifestItem{{Name: "good.mp4"}}
	for i := range 40 {
		items = append(items, library.ManifestItem{Name: fmt.Sprintf("broken-%02d.jpg", i)})
	}
	h := newHarness(t, manifestOf("lobby", "fade", 400, items...), nil)
	h.waitPlaying(0)
	for range 2 { // two passes of the loop
		h.settle()
		h.ctl("fake-next")
		h.waitPlaying(0)
	}
	h.settle()
	if n := h.countEvent("player.item.fail"); n != 1 {
		t.Fatalf("%d player.item.fail lines, want 1:\n%s", n, h.events())
	}

	h.advance(noteRepeat)
	h.ctl("fake-next")
	waitFor(t, "the line of the next hour", func() bool { return h.countEvent("player.item.fail") == 2 })
	if !h.eventWith("player.item.fail", "(and 79 more times since the last line of this kind)") {
		t.Errorf("the line of the next hour has no count of the faults in between:\n%s", h.events())
	}
}

// The error of a failed draw names a temporary file with a random name. With the
// error in the key, each retry of the draw was a new note and a new line.
func TestAFallbackFaultWithANewTextIsOneNote(t *testing.T) {
	tries := 0
	h := newHarness(t, library.PlayerManifest{}, func(o *Options, h *harness) {
		o.Render = func(info fallback.Info, w, h2 int) ([]byte, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			tries++
			return nil, fmt.Errorf("write /run/portapixel/fallback.png.tmp%d: no space left on device", tries)
		}
	})
	waitFor(t, "the first failed draw", func() bool { return h.countEvent("player.fallback.fail") == 1 })
	h.settle()
	for range 3 {
		h.advance(fallbackCheck + time.Second)
	}
	h.mu.Lock()
	n := tries
	h.mu.Unlock()
	if n < 3 {
		t.Fatalf("the draw was tried %d times, want a retry at each check", n)
	}
	if got := h.countEvent("player.fallback.fail"); got != 1 {
		t.Errorf("%d player.fallback.fail lines, want 1:\n%s", got, h.events())
	}
}

// The watchdog: an mpv that ends is started again, the restart counts, and the
// new mpv starts on the item after the one that was on the screen.
func TestRestartAfterExit(t *testing.T) {
	h := newHarness(t, threeItems(), nil)
	h.waitPlaying(0)
	first := h.sup.proc.pid()

	h.ctl("fake-exit", 9)
	waitFor(t, "a new mpv", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	h.waitPlaying(1)
	if got := h.sup.State().Restarts; got != 1 {
		t.Errorf("restarts = %d, want 1", got)
	}
	if h.countEvent("player.exit") != 1 {
		t.Errorf("player.exit lines:\n%s", h.events())
	}
	d := h.dump()
	if !strings.HasSuffix(d.List[0].Path, "promo.mp4") || !strings.HasSuffix(d.List[2].Path, "welcome.jpg") {
		t.Errorf("the new list does not start at the next item: %+v", d.List)
	}
}

// A file that ends mpv while it opens has shown nothing, so the item on the
// screen is the item before it. The new mpv must start after the file that mpv
// opened, and leave that file out: mpv loops the list, and each pass would end
// mpv again and count a step on the ladder. A playlist change gives the file a
// new try.
func TestAFileThatEndsMPVIsLeftOut(t *testing.T) {
	m := manifestOf("lobby", "fade", 400,
		library.ManifestItem{Name: "a.jpg"},
		library.ManifestItem{Name: "b.jpg"},
		library.ManifestItem{Name: "crash.mp4"},
		library.ManifestItem{Name: "d.jpg"},
	)
	h := newHarness(t, m, nil)
	h.waitPlaying(0)
	first := h.sup.proc.pid()
	h.settle()
	h.ctl("fake-next")
	h.waitPlaying(1)
	h.settle()
	h.call("fake-next") // crash.mp4 opens, and the fake ends

	waitFor(t, "a new mpv", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	h.waitPlaying(3)
	d := h.dump()
	var names []string
	for _, e := range d.List {
		names = append(names, filepath.Base(e.Path))
	}
	if !slices.Equal(names, []string{"d.jpg", "a.jpg", "b.jpg"}) {
		t.Fatalf("the new list is %v, want d.jpg, a.jpg, b.jpg", names)
	}
	// The transition of b.jpg goes into d.jpg, the item that now comes after it.
	if got := d.List[2].Opts["script-opts"]; got != "pptr-kind=fade,pptr-ms=400" {
		t.Errorf("b.jpg has the script options %q", got)
	}
	if !h.eventWith("player.item.crash", "crash.mp4") {
		t.Errorf("no player.item.crash line:\n%s", h.events())
	}

	// Two passes of the list: no more exits.
	for range 6 {
		h.settle()
		h.ctl("fake-next")
	}
	h.settle()
	if n := h.countEvent("player.exit"); n != 1 || h.sup.State().Restarts != 1 || h.rebootCount() != 0 {
		t.Fatalf("exits = %d, restarts = %d, reboots = %d:\n%s", n, h.sup.State().Restarts, h.rebootCount(), h.events())
	}

	// A new version of the playlist tries the file again.
	m2 := manifestOf("lobby", "cut", 0,
		library.ManifestItem{Name: "a.jpg"},
		library.ManifestItem{Name: "b.jpg"},
		library.ManifestItem{Name: "crash.mp4"},
		library.ManifestItem{Name: "d.jpg"},
	)
	h.setManifest(m2)
	waitFor(t, "the new list with the file", func() bool {
		d, ok := h.tryDump()
		return ok && len(d.List) == 4
	})
}

// The resume point is for the first load after a restart. If a fallback screen
// takes that load, the point is gone: the same playlist, much later, starts at its
// first item and not in the middle.
func TestResumePointDoesNotOutliveAFallbackScreen(t *testing.T) {
	h := newHarness(t, threeItems(), nil)
	h.waitPlaying(0)
	h.ctl("fake-next")
	h.waitPlaying(1)

	// The content goes away, but the supervisor does not learn it before mpv
	// crashes. The new mpv then gets the fallback screen.
	h.mu.Lock()
	h.manifest = library.PlayerManifest{}
	h.mu.Unlock()
	h.ctl("fake-exit", 9)
	path := h.sup.opt.Command.FallbackPath()
	waitFor(t, "the fallback screen", func() bool {
		d, ok := h.tryDump()
		return ok && len(d.List) == 1 && d.List[0].Path == path
	})

	h.setManifest(threeItems())
	h.waitPlaying(0)
}

// An mpv that ends before its first picture waits for the launch backoff, and
// no mpv runs in that time. /api/status said "running" for it.
func TestAnExitIsStoppedDuringTheBackoff(t *testing.T) {
	h := newHarness(t, manifestOf("lobby", "fade", 400, library.ManifestItem{Name: "crash.mp4"}), nil)
	waitFor(t, "the exit", func() bool { return h.countEvent("player.exit") == 1 })
	waitFor(t, "the state stopped", func() bool { return h.sup.State().Player == StateStopped })
	if h.sup.proc.alive() {
		t.Fatal("mpv runs in the backoff")
	}
}

// An mpv that does not answer its IPC (SIGSTOP, a dead lock) is restarted.
func TestRestartWhenIPCIsSilent(t *testing.T) {
	h := newHarness(t, videos(), nil)
	h.waitPlaying(0)
	first := h.sup.proc.pid()

	h.ctl("fake-hang")
	// Each look moves the clock. A poll goes out and gets no answer, and three
	// looks later the answer is late by the timeout.
	waitFor(t, "the restart", func() bool {
		if h.eventWith("player.restart", "did not answer") {
			return true
		}
		h.clock.Advance(10 * time.Second)
		return false
	})
	waitFor(t, "a new mpv", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	if !h.eventWith("player.restart", "did not answer") || h.sup.State().Restarts != 1 {
		t.Fatalf("restarts = %d:\n%s", h.sup.State().Restarts, h.events())
	}
}

// A video whose position does not move is restarted.
func TestRestartWhenVideoStalls(t *testing.T) {
	h := newHarness(t, videos(), nil)
	h.waitPlaying(0)
	first := h.sup.proc.pid()

	// A video that moves is not a stall, also after a long time.
	for range 20 {
		h.advance(2 * time.Second)
	}
	if h.sup.State().Restarts != 0 {
		t.Fatalf("a moving video was restarted:\n%s", h.events())
	}

	h.ctl("fake-stall")
	h.advance(2 * time.Second)
	h.advance(heartbeatTimeout)
	waitFor(t, "a new mpv", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	if !h.eventWith("player.restart", "did not move") || h.sup.State().Restarts != 1 {
		t.Fatalf("restarts = %d:\n%s", h.sup.State().Restarts, h.events())
	}
}

// A video whose next frame is far ahead stands still on the screen, and its
// time-pos does not move: a slideshow video with one frame in 30 s, or a still
// part of a video with a variable frame rate. The demuxer has read the next
// frame, so mpv waits for its time and is not stuck. Such a video was a stall,
// and each pass of the playlist counted a restart.
func TestAVideoThatWaitsForItsNextFrameIsNotAStall(t *testing.T) {
	h := newHarness(t, videos(), nil)
	h.waitPlaying(0)
	first := h.sup.proc.pid()
	h.ctl("fake-gap", 60) // the next frame is 60 s ahead
	h.ctl("fake-stall")
	for range 20 { // 40 s: more than heartbeatTimeout
		h.advance(2 * time.Second)
	}
	if h.sup.proc.pid() != first || h.eventWith("player.restart", "did not move") {
		t.Fatalf("a video that waits for its next frame was restarted:\n%s", h.events())
	}

	// Past the time of the next frame and the timeout, it is a stall.
	h.advance(60 * time.Second)
	waitFor(t, "a new mpv", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	if !h.eventWith("player.restart", "did not move") {
		t.Fatalf("no stall line:\n%s", h.events())
	}
}

// An image that stays longer than its duration and the grace is restarted.
func TestRestartWhenImageOverruns(t *testing.T) {
	h := newHarness(t, threeItems(), nil)
	h.waitPlaying(0)
	first := h.sup.proc.pid()

	h.settle()
	h.advance(15*time.Second + heartbeatTimeout - 4*time.Second)
	if h.sup.State().Restarts != 0 {
		t.Fatalf("restarted before the grace ended:\n%s", h.events())
	}
	h.clock.Advance(3 * time.Second)
	waitFor(t, "a new mpv", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	if !h.eventWith("player.restart", "welcome.jpg stayed on the screen") {
		t.Fatalf("no overrun line:\n%s", h.events())
	}
}

// mpv plays an animated GIF as a video, for its own length, and it ignores
// image-display-duration. An animation longer than its duration and the grace
// is not stuck while its position moves; each such restart counted, and a long
// GIF in a loop rebooted the device every hour. An animation that stops moving
// is still restarted.
func TestAnAnimatedImageIsNotStuckWhileItMoves(t *testing.T) {
	m := manifestOf("lobby", "fade", 400,
		library.ManifestItem{Name: "anim.gif"}, // the fake moves the position of a .gif
		library.ManifestItem{Name: "b.jpg"},
	)
	h := newHarness(t, m, nil)
	h.waitPlaying(0)
	first := h.sup.proc.pid()
	h.settle()
	for range 30 { // 60 s, and the limit is 10 s and the grace of 30 s
		h.advance(2 * time.Second)
	}
	if h.sup.State().Restarts != 0 {
		t.Fatalf("an animation that moves was restarted:\n%s", h.events())
	}

	h.ctl("fake-stall")
	h.advance(2 * time.Second)
	h.advance(heartbeatTimeout)
	waitFor(t, "a new mpv", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	if !h.eventWith("player.restart", "anim.gif stayed on the screen") {
		t.Fatalf("no overrun line:\n%s", h.events())
	}
}

// A duration of some billion seconds must not overflow the limit of the image
// rule. The sum was less than zero, and the rule fired at each image.
func TestImageLimitDoesNotOverflow(t *testing.T) {
	if got := imageLimit(10, 30*time.Second); got != 40*time.Second {
		t.Errorf("imageLimit(10, 30s) = %s", got)
	}
	for _, seconds := range []int{9223372036, 9999999999, math.MaxInt} {
		if got := imageLimit(seconds, 30*time.Second); got != math.MaxInt64 {
			t.Errorf("imageLimit(%d, 30s) = %s, want the largest duration", seconds, got)
		}
	}
	if got := imageLimit(9223372000, 30*time.Second); got <= 0 {
		t.Errorf("imageLimit(9223372000, 30s) = %s", got)
	}
}

// Four counted restarts in one hour reboot the device. A restart that a person
// asks for does not count.
func TestRebootRule(t *testing.T) {
	h := newHarness(t, videos(), nil)
	h.waitPlaying(0)

	first := h.sup.proc.pid()
	if err := h.sup.Restart("the admin asked"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the restart", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	for i := 1; i <= 4; i++ {
		waitFor(t, "a running mpv", func() bool {
			return h.sup.State().Player == StateRunning && h.sup.State().NowPlaying != nil
		})
		pid := h.sup.proc.pid()
		h.ctl("fake-exit", 1)
		waitFor(t, "the exit", func() bool { return h.sup.proc.pid() != pid })
	}
	waitFor(t, "the reboot", func() bool { return h.rebootCount() == 1 })
	h.mu.Lock()
	reason := h.reboots[0]
	h.mu.Unlock()
	if !strings.Contains(reason, "4 player restarts") {
		t.Errorf("reboot reason = %q", reason)
	}
}

// No display is a wait, not a fault: mpv does not start and nothing counts.
func TestDisplayWait(t *testing.T) {
	var status string
	h := newHarness(t, threeItems(), func(o *Options, h *harness) {
		status = filepath.Join(h.drm, "card0-HDMI-A-1", "status")
		writeFile(t, status, "disconnected\n")
	})
	waitFor(t, "the wait state", func() bool { return h.sup.State().Player == StateWaiting })
	if h.sup.proc.pid() != 0 || h.sup.State().DisplayConnected {
		t.Fatalf("mpv started with no display: %+v", h.sup.State())
	}
	if !h.sup.Started() {
		t.Error("a device that waits for a display is not up for the health marker")
	}
	h.clock.Advance(10 * time.Minute)
	if h.countEvent("player.display.wait") != 1 || h.sup.State().Restarts != 0 {
		t.Fatalf("the wait wrote or counted too much:\n%s", h.events())
	}

	writeFile(t, status, "connected\n")
	h.clock.Advance(displayWaitMax)
	h.waitPlaying(0)
	if !h.eventWith("player.display.found", "player starts") {
		t.Errorf("no player.display.found line:\n%s", h.events())
	}
}

// A display that goes away can end mpv before the next look at the connectors.
// That ending is part of the wait. It is not a fault and it does not count.
func TestExitWithNoDisplayIsNotAFault(t *testing.T) {
	var status string
	h := newHarness(t, threeItems(), func(o *Options, h *harness) {
		status = filepath.Join(h.drm, "card0-HDMI-A-1", "status")
		writeFile(t, status, "connected\n")
	})
	h.waitPlaying(0)

	writeFile(t, status, "disconnected\n")
	h.ctl("fake-exit", 1)
	waitFor(t, "the wait state", func() bool { return h.sup.State().Player == StateWaiting })
	if st := h.sup.State(); st.Restarts != 0 || h.countEvent("player.exit") != 0 || st.DisplayConnected {
		t.Fatalf("the ending counted as a fault: %+v\n%s", st, h.events())
	}

	// The display comes back: mpv starts, and still nothing counts.
	writeFile(t, status, "connected\n")
	h.clock.Advance(displayWaitMax)
	h.waitPlaying(0)
	if h.sup.State().Restarts != 0 || h.countEvent("player.exit") != 0 {
		t.Fatalf("a restart counted:\n%s", h.events())
	}
}

// The nightly restart waits for an item boundary and does not count.
func TestNightlyRestart(t *testing.T) {
	h := newHarness(t, videos(), func(o *Options, h *harness) {
		h.nightly = "03:30"
		h.clock.Set(time.Date(2026, 10, 9, 3, 29, 0, 0, time.UTC))
	})
	h.waitPlaying(0)
	first := h.sup.proc.pid()

	h.settle()
	h.advance(58 * time.Second)
	waitFor(t, "the grace", func() bool { return h.countEvent("player.nightly.grace") == 1 })
	h.ctl("fake-next")
	waitFor(t, "a new mpv", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	if !h.eventWith("player.restart", "item boundary") || h.sup.State().Restarts != 0 {
		t.Fatalf("restarts = %d:\n%s", h.sup.State().Restarts, h.events())
	}
	// Item 0 ended, so the new mpv starts at item 1.
	h.waitPlaying(1)

	// One time per day.
	h.settle()
	h.advance(60 * time.Second)
	if h.countEvent("player.nightly.grace") != 1 {
		t.Errorf("a second nightly restart on the same day:\n%s", h.events())
	}
}

// The nightly restart time, the clock of the fallback screen and the start time
// of an item are times of day in the zone of the device. Options.Local gives them.
func TestLocalTimeComesFromTheZone(t *testing.T) {
	zone := time.FixedZone("test", 2*3600)
	local := func(t time.Time) time.Time { return t.In(zone) }

	// 03:29 UTC is 05:29 in the zone, so the restart at 05:30 is one minute away.
	h := newHarness(t, videos(), func(o *Options, h *harness) {
		o.Local = local
		h.nightly = "05:30"
		h.clock.Set(time.Date(2026, 10, 9, 3, 29, 0, 0, time.UTC))
	})
	h.waitPlaying(0)
	if got := h.sup.State().NowPlaying.Since.Location(); got != zone {
		t.Errorf("the start time of the item is in %v, want the zone of the device", got)
	}
	h.settle()
	h.advance(58 * time.Second)
	waitFor(t, "the grace", func() bool { return h.countEvent("player.nightly.grace") == 1 })

	f := newHarness(t, library.PlayerManifest{}, func(o *Options, h *harness) { o.Local = local })
	waitFor(t, "a render", func() bool { return f.renderCount() > 0 })
	f.mu.Lock()
	got := f.renders[0].Now.Location()
	f.mu.Unlock()
	if got != zone {
		t.Errorf("the clock of the fallback screen is in %v, want the zone of the device", got)
	}
}

// The supervisor measures each duration with Now. The default is time.Now, which
// keeps the monotonic reading. A step of the system clock then changes no
// duration, and the watchdog does not restart mpv at the first sync of the clock.
func TestDefaultNowKeepsTheMonotonicReading(t *testing.T) {
	s := New(Options{Command: CommandConfig{Override: DisableCommand}})
	if now := s.opt.Now(); !strings.Contains(now.String(), "m=") {
		t.Errorf("the default Now gives %q, which has no monotonic reading", now)
	}
}

// With no item boundary, the grace time ends the wait.
func TestNightlyRestartGraceEnds(t *testing.T) {
	h := newHarness(t, videos(), func(o *Options, h *harness) {
		h.nightly = "03:30"
		h.clock.Set(time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC))
	})
	h.waitPlaying(0)
	first := h.sup.proc.pid()
	waitFor(t, "the grace", func() bool { return h.countEvent("player.nightly.grace") == 1 })
	h.settle()
	h.clock.Advance(graceTimeout)
	waitFor(t, "a new mpv", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
	if !h.eventWith("player.restart", "grace time ended") {
		t.Fatalf("no grace line:\n%s", h.events())
	}
}

// The grace of the nightly restart ends with the mpv that it waits for. A
// screen-off or an exit in the grace minute stops that mpv, and the next mpv is a
// new start. The old deadline stopped the new mpv again just after it started.
func TestNightlyGraceEndsWithItsMPV(t *testing.T) {
	t.Run("a screen-off", func(t *testing.T) {
		h := newHarness(t, videos(), func(o *Options, h *harness) {
			h.nightly = "03:30"
			h.clock.Set(time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC))
		})
		h.waitPlaying(0)
		waitFor(t, "the grace", func() bool { return h.countEvent("player.nightly.grace") == 1 })
		if err := h.sup.Suspend(); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(time.Hour)
		if err := h.sup.Resume(); err != nil {
			t.Fatal(err)
		}
		h.waitPlaying(0)
		h.settle()
		h.advance(pollEvery)
		if h.eventWith("player.restart", "grace time ended") {
			t.Fatalf("the old grace stopped the new mpv:\n%s", h.events())
		}
	})
	t.Run("an exit", func(t *testing.T) {
		h := newHarness(t, videos(), func(o *Options, h *harness) {
			h.nightly = "03:30"
			h.clock.Set(time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC))
		})
		h.waitPlaying(0)
		first := h.sup.proc.pid()
		waitFor(t, "the grace", func() bool { return h.countEvent("player.nightly.grace") == 1 })
		h.ctl("fake-exit", 9)
		waitFor(t, "a new mpv", func() bool { pid := h.sup.proc.pid(); return pid != 0 && pid != first })
		h.waitPlaying(1)
		h.settle()
		h.advance(graceTimeout)
		h.advance(pollEvery)
		if h.eventWith("player.restart", "grace time ended") {
			t.Fatalf("the old grace stopped the new mpv:\n%s", h.events())
		}
	})
}

// A screen schedule that has the screen off at the time skips the restart.
func TestNightlyRestartSkippedWhenTheScreenIsOff(t *testing.T) {
	h := newHarness(t, videos(), func(o *Options, h *harness) {
		h.nightly, h.covered = "03:30", true
		h.clock.Set(time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC))
	})
	h.waitPlaying(0)
	waitFor(t, "the skip", func() bool { return h.countEvent("player.nightly.skip") == 1 })
}

// Suspend returns only when mpv has ended, so the DPMS call can take the DRM
// device. Resume starts mpv again. A watchdog restart while the screen is off
// waits for the screen.
func TestSuspendAndResume(t *testing.T) {
	h := newHarness(t, threeItems(), nil)
	h.waitPlaying(0)

	if err := h.sup.Suspend(); err != nil {
		t.Fatal(err)
	}
	if h.sup.proc.alive() {
		t.Fatal("Suspend returned while mpv still runs")
	}
	st := h.sup.State()
	if !st.Suspended || st.Player != StateStopped || st.NowPlaying != nil {
		t.Fatalf("state after Suspend = %+v", st)
	}
	if !h.sup.Started() {
		t.Error("a device with the screen off is not up for the health marker")
	}
	h.clock.Advance(time.Hour)
	if h.sup.proc.alive() || h.sup.State().Restarts != 0 {
		t.Fatal("mpv started while the screen is off")
	}

	if err := h.sup.Resume(); err != nil {
		t.Fatal(err)
	}
	h.waitPlaying(0)
}

// A fault of transitions.lua goes in the ops log, and the same fault again goes
// in again after noteRepeat. mpv sends an event only for a value that changed,
// so the report has a count: with the text alone, a fault that came again
// reached the daemon one time in the life of mpv.
func TestTransitionFaultIsLogged(t *testing.T) {
	const text = "fade: the copy of the screen failed; the transition is a cut"
	h := newHarness(t, videos(), nil)
	h.waitPlaying(0)
	h.ctl("fake-fault", text)
	waitFor(t, "the fault line", func() bool { return h.countEvent("player.transition.fault") == 1 })

	h.settle()
	h.advance(noteRepeat)
	h.ctl("fake-fault", text)
	waitFor(t, "the fault line one hour later", func() bool { return h.countEvent("player.transition.fault") == 2 })
	if !h.eventWith("player.transition.fault", text) {
		t.Errorf("the line does not hold the text of the fault:\n%s", h.events())
	}
}

// A disabled player starts nothing, and a screen-off does not wait for it.
func TestDisabledPlayer(t *testing.T) {
	s := New(Options{Command: CommandConfig{Override: DisableCommand}})
	if s.State().Player != StateDisabled || !s.Started() {
		t.Fatalf("state = %+v", s.State())
	}
	if err := s.Suspend(); err != nil || !s.State().Suspended {
		t.Fatalf("Suspend = %v, state %+v", err, s.State())
	}
	if err := s.Resume(); err != nil || s.State().Suspended {
		t.Fatalf("Resume = %v", err)
	}
}

// An mpv that does not read its socket makes each write wait for the write
// timeout. The load of a playlist stops at the first write that fails, so the loop
// is not held for the timeout times the number of items. The request that failed
// stays in the list, and the silence rule restarts mpv.
func TestLoadStopsAfterAFailedWrite(t *testing.T) {
	a, b := net.Pipe() // nobody reads b
	defer a.Close()
	defer b.Close()
	s := New(Options{Command: CommandConfig{Override: "mpv"}})
	s.ipc = &ipcConn{conn: a, msgs: make(chan message), done: make(chan struct{})}
	s.pending = make(map[int64]request)

	start := time.Now()
	s.loadManifest(threeItems(), time.Now())
	if took := time.Since(start); took >= 2*writeTimeout {
		t.Errorf("the load took %s, want one write timeout (%s)", took, writeTimeout)
	}
	if len(s.pending) != 1 {
		t.Errorf("%d requests are open, want the one that failed", len(s.pending))
	}
}

// A write that fails breaks the connection. A write that timed out can have sent
// a part of its line, and an mpv that does not read made each later write wait
// for the timeout too: each poll, motion message and fallback draw.
func TestASendAfterAFailedWriteFailsAtOnce(t *testing.T) {
	a, b := net.Pipe() // nobody reads b
	defer a.Close()
	defer b.Close()
	c := &ipcConn{conn: a, msgs: make(chan message), done: make(chan struct{})}
	if _, err := c.send("get_property", "time-pos"); err == nil {
		t.Fatal("a write that nobody read did not fail")
	}
	start := time.Now()
	if _, err := c.send("get_property", "time-pos"); err == nil {
		t.Fatal("a send on a broken connection did not fail")
	}
	if took := time.Since(start); took >= writeTimeout/2 {
		t.Errorf("the send on a broken connection took %s", took)
	}
}

// A queue that is full answers ErrBusy, so a person learns that the command did
// not happen.
func TestBusyQueue(t *testing.T) {
	s := New(Options{Command: CommandConfig{Override: "mpv"}})
	for range cap(s.cmds) {
		s.Resume()
	}
	if err := s.Restart("test"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Restart = %v, want ErrBusy", err)
	}
	if err := s.Suspend(); !errors.Is(err, ErrBusy) {
		t.Fatalf("Suspend = %v, want ErrBusy", err)
	}
}

// PlaylistChanged and DisplayChanged say that something changed, and nothing asks
// again. A full queue dropped them, and the screen kept the old playlist and the
// old rotation until the next change.
func TestAChangeIsNotLostWhenTheQueueIsFull(t *testing.T) {
	inRender := make(chan struct{}, 1)
	release := make(chan struct{})
	var hold atomic.Bool
	h := newHarness(t, threeItems(), func(o *Options, h *harness) {
		o.Render = func(info fallback.Info, w, h2 int) ([]byte, error) {
			if hold.Load() {
				inRender <- struct{}{}
				<-release
			}
			return []byte("PNG"), nil
		}
	})
	h.waitPlaying(0)

	// The loop draws the fallback screen and waits in the render.
	hold.Store(true)
	h.setManifest(library.PlayerManifest{})
	<-inRender
	hold.Store(false)
	for range cap(h.sup.cmds) {
		h.sup.Resume() // a message that changes nothing fills the queue
	}
	h.mu.Lock()
	h.display.Rotation = 90
	h.mu.Unlock()
	h.sup.DisplayChanged()
	h.setManifest(manifestOf("evening", "cut", 0, library.ManifestItem{Name: "night.jpg"}, library.ManifestItem{Name: "stars.mp4"}))
	close(release)

	waitFor(t, "the evening playlist", func() bool {
		np := h.sup.State().NowPlaying
		return np != nil && np.Playlist == "evening"
	})
	if d := h.dump(); !slices.Contains(d.Args, "--video-rotate=90") {
		t.Errorf("the display change was lost: the arguments are %q", d.Args)
	}
}
