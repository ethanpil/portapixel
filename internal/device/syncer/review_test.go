package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/manifest"
)

// A 401 that is not the 401 of the fleet API must not destroy a pairing. A proxy
// with basic authentication left on, a captive portal and a web application
// firewall all answer 401, and a device paired by code needs a person on two screens
// to pair again.
func TestA401WithoutTheCodeKeepsThePairing(t *testing.T) {
	f := newFakeServer(t)
	d := pairedDev(t, f)
	d.s.Once(context.Background()) // one good poll

	// Something between the device and the server answers 401 with no code.
	blocker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer blocker.Close()
	d.st.ServerURL = blocker.URL
	if err := d.st.Save(d.state); err != nil {
		t.Fatal(err)
	}

	next := d.s.Once(context.Background())
	if !d.st.Paired() {
		t.Fatal("a 401 of a box in between took the pairing away")
	}
	if next <= 0 || next > MaxPoll {
		t.Errorf("the wait after the fault is %v", next)
	}
	got := d.s.Report().SyncError
	if !strings.Contains(got, "401") || !strings.Contains(got, "not with an answer of PortaPixel") {
		t.Errorf("the sync error is %q; it must say that the 401 did not come from the fleet API", got)
	}
}

// POST /api/pair on a device that is paired must answer that it is paired. Before
// this the call wrote the new address beside the token of the old server, and the
// next poll sent that token to a stranger.
func TestPairRefusesASecondServer(t *testing.T) {
	f := newFakeServer(t)
	d := pairedDev(t, f)
	token := d.st.DeviceToken

	other := newFakeServer(t)
	_, err := d.s.Pair(context.Background(), other.srv.URL, "", true)
	var already ErrAlreadyPaired
	if err == nil || !errors.As(err, &already) {
		t.Fatalf("Pair() = %v, want ErrAlreadyPaired", err)
	}
	if d.st.ServerURL != f.srv.URL || d.st.DeviceToken != token {
		t.Errorf("the pairing changed: %q %q", d.st.ServerURL, d.st.DeviceToken)
	}
	if len(other.enrolls) != 0 {
		t.Error("the device talked to the second server")
	}
}

// A pairing that the server refuses must leave portapixel.toml as it was. A save
// that came first wiped a working address and the enrollment token beside it.
func TestPairWritesTheConfigOnlyAfterTheServerAccepted(t *testing.T) {
	f := newFakeServer(t)
	d := newDev(t, f)
	d.cfg.Server.URL = "http://the-old-server.example.com"
	d.cfg.Server.Token = "the enrollment token"

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer dead.Close()

	if _, err := d.s.Pair(context.Background(), dead.URL, "a token", true); err == nil {
		t.Fatal("Pair() gave no error for a server that answered 500")
	}
	if d.saves != 0 {
		t.Errorf("the configuration was written %d times", d.saves)
	}
	if d.cfg.Server.URL != "http://the-old-server.example.com" || d.cfg.Server.Token != "the enrollment token" {
		t.Errorf("the configuration changed: %q %q", d.cfg.Server.URL, d.cfg.Server.Token)
	}
}

// The settings page shows a saved token as the mask of the configuration API. A
// Connect with the mask means "use the token that is in the file", and the mask must
// never travel to the fleet server.
func TestPairResolvesTheSecretMask(t *testing.T) {
	f := newFakeServer(t)
	d := newDev(t, f)
	d.cfg.Server.Token = f.enrollToken

	if _, err := d.s.Pair(context.Background(), f.srv.URL, config.Mask, true); err != nil {
		t.Fatal(err)
	}
	if !d.st.Paired() {
		t.Fatal("the device did not pair with the saved token")
	}
	for _, sent := range f.enrolls {
		if sent.Token == config.Mask {
			t.Error("the mask of the configuration API went to the fleet server")
		}
	}
	if d.cfg.Server.Token != f.enrollToken {
		t.Errorf("the saved token became %q", d.cfg.Server.Token)
	}
}

// Unpair must not report success when it could not write portapixel.toml. The poll
// loop reads that file, so the device would pair itself again inside a minute.
func TestUnpairFailsWhenTheConfigCannotBeWritten(t *testing.T) {
	f := newFakeServer(t)
	d := pairedDev(t, f)

	broken := errNoWrite{}
	d.s.opt.SaveServer = func(string, string) error { return broken }
	if err := d.s.Unpair(); err == nil {
		t.Fatal("Unpair() gave no error for a configuration that could not be written")
	}
	if !d.st.Paired() {
		t.Error("the device dropped its token although the configuration still names the server")
	}
}

// An enroll answer of "pending" with no claim secret is a protocol fault. The device
// cannot ask again with it, so a pass that accepted it made a request loop with a
// state file write in each turn.
func TestEnrollRefusesAPendingAnswerWithNoClaimSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(manifest.EnrollResponse{Status: "pending", PairingCode: "ABC123"})
	}))
	defer server.Close()

	d := newDev(t, nil)
	d.cfg.Server.URL = server.URL
	if _, err := d.s.Pair(context.Background(), server.URL, "", false); err == nil {
		t.Fatal("Pair() took an answer with no claim secret")
	}
	if d.st.ClaimSecret != "" || d.st.PairingCode != "" {
		t.Errorf("the state took the answer: %+v", d.st)
	}
}

// The loop must never ask for no wait many passes in a row. A state that does not
// move would make a request every few milliseconds with a state file write in each
// one, which is thousands of flash writes a minute.
func TestTheLoopGuardStopsARunOfPassesWithNoWait(t *testing.T) {
	var immediate int
	for i := 0; i < maxImmediate; i++ {
		if got := nextWait(0, &immediate); got != 0 {
			t.Fatalf("pass %d waited %v, and the first passes must not wait", i+1, got)
		}
	}
	if got := nextWait(0, &immediate); got != MinPoll {
		t.Errorf("the pass after the guard waited %v, want %v", got, MinPoll)
	}
	// A pass that asks for a wait clears the count.
	if got := nextWait(time.Minute, &immediate); got != time.Minute {
		t.Errorf("the guard changed a wait of one minute into %v", got)
	}
	if immediate != 0 {
		t.Errorf("the count is %d after a pass that waited", immediate)
	}
}

// A media reference with no size is not usable: the space rule of D41 would have
// nothing to add up, and the download would run with no limit. The object is left
// out and the rest of the manifest still applies.
func TestAnObjectWithNoSizeIsLeftOut(t *testing.T) {
	f := newFakeServer(t)
	d := pairedDev(t, f)

	ref := f.addObject("welcome.jpg", "the picture of the lobby")
	noSize := ref
	noSize.Size = 0
	objects := d.s.planObjects(f.srv.URL, manifest.Manifest{Media: []manifest.MediaRef{noSize}}, false)
	if len(objects) != 0 {
		t.Errorf("planObjects took an object with no size: %+v", objects)
	}

	objects = d.s.planObjects(f.srv.URL, manifest.Manifest{Media: []manifest.MediaRef{ref}}, false)
	if len(objects) != 1 {
		t.Errorf("planObjects left out an object with a size: %+v", objects)
	}
}

// The steady state of a paired device writes nothing at all to the flash card. A
// poll that brings the same manifest must not touch one file.
func TestASteadyPollWritesNothing(t *testing.T) {
	f := newFakeServer(t)
	ref := f.addObject("welcome.jpg", "the picture of the lobby")
	f.setManifest(lobbyManifest(ref))
	d := pairedDev(t, f)
	d.s.Once(context.Background())

	before := treeTimes(t, d.media)
	beforeState := fileTime(t, filepath.Join(d.state, "state.json"))
	for i := 0; i < 3; i++ {
		d.s.Once(context.Background())
	}
	if got := treeTimes(t, d.media); !reflect.DeepEqual(got, before) {
		t.Errorf("a steady poll wrote to the card:\nbefore %v\nafter  %v", before, got)
	}
	if got := fileTime(t, filepath.Join(d.state, "state.json")); got != beforeState {
		t.Error("a steady poll wrote state.json")
	}
}

// A schedule edit is not a content edit. It must not make the device render and
// write every playlist again, swap the directories and rescan the card.
func TestARuleChangeWritesNoPlaylist(t *testing.T) {
	f := newFakeServer(t)
	ref := f.addObject("welcome.jpg", "the picture of the lobby")
	f.setManifest(lobbyManifest(ref))
	d := pairedDev(t, f)
	d.s.Once(context.Background())

	before := treeTimes(t, d.media)
	rescans := d.rescans

	m := lobbyManifest(ref)
	m.Schedule = []manifest.Rule{{Playlist: "lobby", Days: []string{"mon"}, Start: "08:00", End: "18:00"}}
	f.setManifest(m)
	d.s.Once(context.Background())

	if got := treeTimes(t, d.media); !reflect.DeepEqual(got, before) {
		t.Errorf("a schedule edit wrote to the card:\nbefore %v\nafter  %v", before, got)
	}
	if d.rescans != rescans {
		t.Error("a schedule edit made the device rescan the card")
	}
	if len(d.fleetRules) != 1 {
		t.Errorf("the scheduler did not get the new rules: %+v", d.fleetRules)
	}
}

// A file that somebody removed from the card with a laptop must come back, although
// the manifest did not change.
func TestAMissingObjectIsFetchedAgain(t *testing.T) {
	f := newFakeServer(t)
	ref := f.addObject("welcome.jpg", "the picture of the lobby")
	f.setManifest(lobbyManifest(ref))
	d := pairedDev(t, f)
	d.s.Once(context.Background())

	object := d.objectPath(ref)
	if err := os.Remove(object); err != nil {
		t.Fatal(err)
	}
	d.s.Once(context.Background())
	if _, err := os.Stat(object); err != nil {
		t.Errorf("the object did not come back: %v", err)
	}
}

// A playlist directory that somebody removed must come back too.
func TestAMissingPlaylistIsWrittenAgain(t *testing.T) {
	f := newFakeServer(t)
	ref := f.addObject("welcome.jpg", "the picture of the lobby")
	f.setManifest(lobbyManifest(ref))
	d := pairedDev(t, f)
	d.s.Once(context.Background())

	if err := os.RemoveAll(d.fleetPath("lobby")); err != nil {
		t.Fatal(err)
	}
	d.s.Once(context.Background())
	if _, err := os.Stat(d.fleetPath("lobby", "playlist.toml")); err != nil {
		t.Errorf("the playlist did not come back: %v", err)
	}
}

// An object that the server renamed keeps its bytes. The eviction must not remove
// the file that the copy reads from: the object would then be on the card under
// neither name, and the fetch error would stop the whole round.
func TestARenamedObjectKeepsItsBytes(t *testing.T) {
	f := newFakeServer(t)
	ref := f.addObject("welcome.jpg", "the picture of the lobby")
	f.setManifest(lobbyManifest(ref))
	d := pairedDev(t, f)
	d.s.Once(context.Background())
	old := d.objectPath(ref)

	// The same bytes under another name, and a card with room for one copy and no
	// download. The server answers no object at all, so a download would fail.
	f.objectsFail = true
	renamed := ref
	renamed.Name = "welcome-v2.jpg"
	f.setManifest(lobbyManifest(renamed))
	d.free = SpaceReserve + uint64(ref.Size) + 64

	d.s.Once(context.Background())
	if got := d.s.Report().SyncError; got != "" {
		t.Fatalf("the sync failed: %s", got)
	}
	if _, err := os.Stat(d.objectPath(renamed)); err != nil {
		t.Fatalf("the object is not under its new name: %v", err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("the file that the copy read from is gone: %v", err)
	}
}

// A staging or a trash directory that a power cut left must go. It is invisible to
// the library and to the free space arithmetic, and a trash directory holds a whole
// set of playlists.
func TestSweepRemovesTheLeftoversOfAPowerCut(t *testing.T) {
	f := newFakeServer(t)
	d := pairedDev(t, f)

	leftovers := []string{
		d.fleetPath(stagingPrefix + "abcdef"),
		d.fleetPath(trashPrefix + "123456"),
	}
	for _, dir := range leftovers {
		if err := os.MkdirAll(filepath.Join(dir, "lobby"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d.s.Once(context.Background())
	for _, dir := range leftovers {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", filepath.Base(dir), err)
		}
	}
}

// The update command restarts the service, so its ID is recorded and acknowledged
// before the work starts. Without that the server sends it again three times, and
// after a rollback the device attempts the bad release three times.
func TestTheUpdateCommandIsAcknowledgedFirst(t *testing.T) {
	f := newFakeServer(t)
	d := pairedDev(t, f)

	var whenMarked bool
	d.updateErr = nil
	d.s.opt.Update = func(ctx context.Context) error {
		whenMarked = d.st.RanCommand(7)
		d.updates++
		return nil
	}
	m := manifest.Manifest{Commands: []manifest.Command{{ID: 7, Type: CmdUpdate}}}
	f.setManifest(m)
	d.s.Once(context.Background())

	if d.updates != 1 {
		t.Fatalf("the update command ran %d times", d.updates)
	}
	if !whenMarked {
		t.Error("the command ID was not in the state file before the update ran")
	}
}

// The release source of a paired device is the address of the pairing and never a
// value of the manifest. The updater measures the mirror path against it.
func TestUpdateSourceUsesThePairedAddress(t *testing.T) {
	f := newFakeServer(t)
	d := pairedDev(t, f)
	m := manifest.Manifest{Release: &manifest.ReleaseRef{
		Version: "1.6.0",
		BaseURL: "https://attacker.example.net/r",
	}}
	f.setManifest(m)
	d.s.Once(context.Background())

	src, ok := d.s.UpdateSource()
	if !ok {
		t.Fatal("UpdateSource() says the device is not paired")
	}
	if src.ServerURL != f.srv.URL {
		t.Errorf("ServerURL = %q, want the address of the pairing", src.ServerURL)
	}
	if src.Bearer != d.st.DeviceToken {
		t.Error("the source carries another token")
	}
}

// The list that the fleet server owns is exactly what the manifest carries. A new
// manifest field must not enter without a decision.
func TestManagedFieldsMatchTheManifest(t *testing.T) {
	// Each field of manifest.Manifest, and the configuration field that it owns.
	// "" means that the field is not a configuration setting of the device.
	owns := map[string]string{
		"ServerName":      "",
		"PollSeconds":     "",
		"Playlists":       "",
		"DefaultPlaylist": "playback.default_playlist",
		"Schedule":        "schedule",
		"Media":           "",
		"Commands":        "",
		"Screen":          "display.on_time display.off_time display.power_days",
		"Release":         "",
	}

	value := reflect.TypeOf(manifest.Manifest{})
	var want []string
	for i := 0; i < value.NumField(); i++ {
		name := value.Field(i).Name
		fields, ok := owns[name]
		if !ok {
			t.Fatalf("manifest.Manifest has the new field %s. "+
				"Say in this test whether the fleet server owns a configuration field with it, "+
				"and change internal/device/syncer/managed.go to match (D48).", name)
		}
		if fields != "" {
			want = append(want, strings.Fields(fields)...)
		}
	}
	got := ManagedFields()
	if len(got) != len(want) {
		t.Fatalf("ManagedFields() = %v, and the manifest owns %v", got, want)
	}
	for _, field := range want {
		if !ManagedField(field) {
			t.Errorf("the manifest carries %s and the table does not name it", field)
		}
	}
}

// errNoWrite is a configuration save that fails.
type errNoWrite struct{}

func (errNoWrite) Error() string { return "the media partition is read-only" }

// fileTime gives the modification time of one file, or the zero time.
func fileTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
