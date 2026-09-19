package syncer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// TestPairWithAnEnrollmentToken is the zero-touch flow of D25: one token on any
// number of cards, and each device gets a token of its own.
func TestPairWithAnEnrollmentToken(t *testing.T) {
	f := newFakeServer(t)
	d := newDev(t, f)

	state, err := d.s.Pair(context.Background(), f.srv.URL, f.enrollToken, true)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if state.Status != StatusPaired {
		t.Fatalf("the status is %q", state.Status)
	}
	if d.st.DeviceToken != f.deviceToken {
		t.Errorf("the device token is %q", d.st.DeviceToken)
	}

	// The per-device token is in state.json and never in the TOML. A flashed card
	// must stay clonable (D25).
	if d.cfg.Server.Token == f.deviceToken {
		t.Error("the per-device token went into portapixel.toml")
	}
	if d.savedURL != f.srv.URL || d.savedToken != f.enrollToken {
		t.Errorf("the configuration save got %q and %q", d.savedURL, d.savedToken)
	}
	saved := readState(t, d.state)
	if saved.DeviceToken != f.deviceToken || saved.ServerURL != f.srv.URL {
		t.Errorf("state.json holds %+v", saved)
	}

	// The enroll request carries the full hardware ID. It is the secret that
	// permits a re-pair with the fleet token, and this is one of the two places
	// where it leaves the device.
	if len(f.enrolls) != 1 {
		t.Fatalf("the server got %d enroll requests", len(f.enrolls))
	}
	if got := f.enrolls[0].HardwareID; got != strings.Repeat("ab", 32) {
		t.Errorf("the hardware ID of the request is %q", got)
	}
	for _, ct := range f.contentTypes {
		if ct != "application/json" {
			t.Errorf("a request carried the Content-Type %q", ct)
		}
	}
}

// TestPairWithADeviceToken is the manual paste flow: the server answers "paired"
// the same way, so the device needs no second code path.
func TestPairWithADeviceToken(t *testing.T) {
	f := newFakeServer(t)
	f.enrollToken = "a per-device token"
	d := newDev(t, f)

	if _, err := d.s.Pair(context.Background(), f.srv.URL, "a per-device token", true); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if !d.st.Paired() {
		t.Fatal("the device is not paired")
	}
}

// TestCodePairingSurvivesARestart is the third flow of D25 and the reboot rule: the
// claim secret lives in state.json, so a device that reboots while it waits keeps
// its place in the pending list.
func TestCodePairingSurvivesARestart(t *testing.T) {
	f := newFakeServer(t)
	d := newDev(t, f)

	state, err := d.s.Pair(context.Background(), f.srv.URL, "", true)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if state.Status != StatusPending || state.PairingCode != f.pairingCode {
		t.Fatalf("the state is %+v", state)
	}
	if d.st.ClaimSecret == "" {
		t.Fatal("the device kept no claim secret")
	}

	// A poll while the admin has not answered keeps the device waiting, on the
	// 10 second interval.
	if next := d.s.Once(context.Background()); next != claimPoll {
		t.Errorf("the wait is %v, want %v", next, claimPoll)
	}
	if got := d.s.State().Status; got != StatusPending {
		t.Errorf("the status is %q", got)
	}

	// The device reboots. A new Syncer over the same directories reads the claim
	// secret out of state.json.
	again := newDevIn(t, f, d.media, d.state)
	again.cfg.Server.URL = f.srv.URL
	if got := again.s.State().Status; got != StatusPending {
		t.Fatalf("after the restart the status is %q", got)
	}

	// The admin approves the code.
	f.mu.Lock()
	f.approved = true
	f.mu.Unlock()

	if next := again.s.Once(context.Background()); next != 0 {
		t.Errorf("a device that just paired waits %v, want no wait", next)
	}
	if !again.st.Paired() {
		t.Fatal("the device is not paired after the approval")
	}
	if again.st.ClaimSecret != "" || again.st.PairingCode != "" {
		t.Errorf("the claim secret or the code stayed: %+v", again.st)
	}
}

// TestPendingSlowsDownAfterTenMinutes keeps a screen that waits for days from
// asking six times a minute for ever.
func TestPendingSlowsDownAfterTenMinutes(t *testing.T) {
	f := newFakeServer(t)
	d := newDev(t, f)
	if _, err := d.s.Pair(context.Background(), f.srv.URL, "", true); err != nil {
		t.Fatalf("pair: %v", err)
	}

	d.now = d.now.Add(claimPatience + time.Minute)
	if next := d.s.Once(context.Background()); next != claimSlow {
		t.Errorf("the wait is %v, want %v", next, claimSlow)
	}
}

// TestRevokedTokenDropsToUnpairedAndRateLimits is the 401 rule. The device keeps
// playing, it stops using the fleet playlists, and it asks again at most once every
// five minutes.
func TestRevokedTokenDropsToUnpairedAndRateLimits(t *testing.T) {
	f := newFakeServer(t)
	d := newDev(t, f)
	if _, err := d.s.Pair(context.Background(), f.srv.URL, f.enrollToken, true); err != nil {
		t.Fatalf("pair: %v", err)
	}
	d.s.Once(context.Background()) // one good poll

	f.mu.Lock()
	f.revoked = true
	f.enrollToken = "" // the token of the TOML is not valid any more either
	f.mu.Unlock()

	next := d.s.Once(context.Background())
	if next != reEnrollGap {
		t.Errorf("the wait after a 401 is %v, want %v", next, reEnrollGap)
	}
	if d.st.Paired() {
		t.Error("the device kept its token after a 401")
	}
	if d.cleared == 0 {
		t.Error("the scheduler kept the fleet rules after a 401")
	}
	if got := d.s.Report().SyncError; got == "" {
		t.Error("the report says nothing about the refused token")
	}

	// A second pass inside the five minutes makes no request at all.
	before := len(f.enrolls)
	if next := d.s.Once(context.Background()); next <= 0 || next > reEnrollGap {
		t.Errorf("the wait is %v", next)
	}
	if len(f.enrolls) != before {
		t.Errorf("the device asked again inside the gap: %d requests", len(f.enrolls)-before)
	}

	// After the gap it tries once with the token of the TOML.
	d.now = d.now.Add(reEnrollGap + time.Second)
	d.cfg.Server.Token = "the token of the toml"
	d.s.Once(context.Background())
	if len(f.enrolls) != before+1 {
		t.Errorf("the device made %d requests after the gap", len(f.enrolls)-before)
	}
}

// TestUnpairKeepsTheCacheAndStopsUsingFleet is the unpair rule (D25): the objects
// stay, the fleet playlists stop, and the TOML no longer names a server.
func TestUnpairKeepsTheCacheAndStopsUsingFleet(t *testing.T) {
	f := newFakeServer(t)
	ref := f.addObject("welcome.jpg", "the picture of the lobby")
	f.setManifest(manifest.Manifest{
		DefaultPlaylist: "lobby",
		Media:           []manifest.MediaRef{ref},
		Playlists: []manifest.Playlist{{
			Name: "lobby", Title: "Lobby loop",
			Items: []manifest.Item{{SHA256: ref.SHA256, Duration: 10}},
		}},
	})
	d := newDev(t, f)
	if _, err := d.s.Pair(context.Background(), f.srv.URL, f.enrollToken, true); err != nil {
		t.Fatalf("pair: %v", err)
	}
	d.s.Once(context.Background())

	object := d.objectPath(ref)
	if _, err := os.Stat(object); err != nil {
		t.Fatalf("the object is not in the store: %v", err)
	}
	if _, ok := d.lib.Snapshot().Find("lobby"); !ok {
		t.Fatal("the library does not serve the fleet playlist")
	}

	if err := d.s.Unpair(); err != nil {
		t.Fatalf("unpair: %v", err)
	}
	if d.st.Paired() || d.st.ClaimSecret != "" || d.st.Fleet != nil {
		t.Errorf("the state after the unpair is %+v", d.st)
	}
	if d.cleared == 0 {
		t.Error("the scheduler kept the fleet rules")
	}
	if _, ok := d.lib.Snapshot().Find("lobby"); ok {
		t.Error("the library still serves the fleet playlist")
	}
	// The cache stays. A re-pair must not download the object again.
	if _, err := os.Stat(object); err != nil {
		t.Errorf("the unpair removed the cached object: %v", err)
	}
	if _, err := os.Stat(d.fleetPath("lobby", "playlist.toml")); err != nil {
		t.Errorf("the unpair removed the fleet playlist file: %v", err)
	}
	// The TOML no longer names a server, so the poll loop does not pair the device
	// again inside a minute.
	if d.cfg.Server.URL != "" || d.cfg.Server.Token != "" {
		t.Errorf("the TOML still names %q", d.cfg.Server.URL)
	}
	if next := d.s.Once(context.Background()); next != standaloneWait {
		t.Errorf("a standalone device waits %v", next)
	}
}

// TestRestoreGivesTheFleetScheduleBackAtStart proves that a paired device does not
// fall back to the rules of the TOML between its start and its first poll.
func TestRestoreGivesTheFleetScheduleBackAtStart(t *testing.T) {
	f := newFakeServer(t)
	f.setManifest(manifest.Manifest{
		DefaultPlaylist: "lobby",
		Schedule:        []manifest.Rule{{Playlist: "lobby", Start: "08:00", End: "18:00"}},
		Playlists: []manifest.Playlist{{
			Name: "lobby", Items: []manifest.Item{{URL: "https://example.com/a", Duration: 30}},
		}},
	})
	d := newDev(t, f)
	if _, err := d.s.Pair(context.Background(), f.srv.URL, f.enrollToken, true); err != nil {
		t.Fatalf("pair: %v", err)
	}
	d.s.Once(context.Background())

	again := newDevIn(t, f, d.media, d.state)
	if again.fleetDefault != "" {
		t.Fatal("a new Syncer must hand no rules over before Restore")
	}
	again.s.Restore()
	if again.fleetDefault != "lobby" || len(again.fleetRules) != 1 {
		t.Errorf("Restore gave %q and %d rules", again.fleetDefault, len(again.fleetRules))
	}
	if again.rescans != 0 {
		t.Errorf("Restore rescanned %d times; it must write and rescan nothing", again.rescans)
	}
}

func TestCheckServerURL(t *testing.T) {
	tests := []struct {
		name, in, want string
		bad            bool
	}{
		{name: "a plain address", in: "https://signage.example.com", want: "https://signage.example.com"},
		{name: "a slash at the end goes away", in: "https://a.example.com/", want: "https://a.example.com"},
		{name: "a path stays", in: "https://a.example.com/fleet", want: "https://a.example.com/fleet"},
		{name: "white space goes away", in: "  http://10.0.0.5:8080  ", want: "http://10.0.0.5:8080"},
		{name: "an empty address", in: "", bad: true},
		{name: "no scheme", in: "signage.example.com", bad: true},
		{name: "another scheme", in: "ftp://a.example.com", bad: true},
		{name: "credentials in the address", in: "https://user:pw@a.example.com", bad: true},
		{name: "a fragment", in: "https://a.example.com/#top", bad: true},
		{name: "a query", in: "https://a.example.com/?token=x", bad: true},
		{name: "no host", in: "https://", bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CheckServerURL(tt.in)
			if tt.bad {
				if err == nil {
					t.Fatalf("%q was accepted as %q", tt.in, got)
				}
				var bad BadURL
				if !errors.As(err, &bad) {
					t.Errorf("the fault is not a BadURL: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("gave %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInsecureURL(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"https://signage.example.com", false},
		{"http://signage.example.com", true},
		{"http://203.0.113.9", true},
		{"http://192.168.1.10", false},
		{"http://10.0.0.5:8080", false},
		{"http://172.16.4.4", false},
		{"http://127.0.0.1:8093", false},
		{"http://localhost:8093", false},
		{"http://fleet.local", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := InsecureURL(tt.in); got != tt.want {
			t.Errorf("InsecureURL(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
