package health

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/manifest"
)

// fakeRoots makes a small /proc, /sys, /etc and share directory.
type fakeRoots struct {
	src Sources
}

func newRoots(t *testing.T) *fakeRoots {
	t.Helper()
	base := t.TempDir()
	f := &fakeRoots{src: Sources{
		ProcRoot:  filepath.Join(base, "proc"),
		SysRoot:   filepath.Join(base, "sys"),
		EtcRoot:   filepath.Join(base, "etc"),
		ShareRoot: filepath.Join(base, "share"),
		MediaRoot: filepath.Join(base, "media"),
		StateDir:  filepath.Join(base, "state"),
	}}
	for _, dir := range []string{f.src.ProcRoot, f.src.SysRoot, f.src.EtcRoot, f.src.ShareRoot, f.src.MediaRoot, f.src.StateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fakeRoots) write(t *testing.T, path, content string) {
	t.Helper()
	full := filepath.Join(path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeRoots) proc(t *testing.T, name, content string) {
	f.write(t, filepath.Join(f.src.ProcRoot, name), content)
}

func TestStatusReadsTheSystemFiles(t *testing.T) {
	f := newRoots(t)
	f.proc(t, "uptime", "123456.78 987654.32\n")
	f.proc(t, "loadavg", "0.42 0.31 0.25 1/123 4567\n")
	f.proc(t, "meminfo", "MemTotal:        4014520 kB\nMemFree:          123456 kB\nMemAvailable:    3012345 kB\n")
	f.write(t, filepath.Join(f.src.SysRoot, "class", "thermal", "thermal_zone0", "temp"), "48312\n")
	f.write(t, filepath.Join(f.src.SysRoot, "class", "thermal", "thermal_zone1", "temp"), "51987\n")
	f.write(t, filepath.Join(f.src.EtcRoot, "portapixel-release"), "PORTAPIXEL_VERSION=0.1.0\nALPINE_RELEASE=3.23.2\nARCH=x86_64\n")
	f.write(t, filepath.Join(f.src.ShareRoot, "packages.manifest"), "chromium-149.0\ncage-0.2.1\n")

	cfg := config.Default()
	cfg.Device.Name = "Lobby Screen"
	cfg.Device.Timezone = "America/New_York"
	cfg.Web.Password = "a better password"

	got := New(f.src).Status(Inputs{
		Config:           cfg,
		DeviceID:         "px-1a2b3c4d",
		BrowserState:     "running",
		NavigationRung:   "cdp",
		DisplayConnected: true,
		ScreenOn:         true,
		ClockSynced:      true,
	})

	if got.UptimeSeconds != 123456 {
		t.Errorf("uptime = %d", got.UptimeSeconds)
	}
	if got.Load != 0.42 {
		t.Errorf("load = %v", got.Load)
	}
	if got.RAMTotalBytes != 4014520*1024 || got.RAMFreeBytes != 3012345*1024 {
		t.Errorf("ram = %d / %d", got.RAMFreeBytes, got.RAMTotalBytes)
	}
	if got.TempC != 51.987 {
		t.Errorf("temperature = %v, want the highest zone", got.TempC)
	}
	if got.ImageVersion != "0.1.0" {
		t.Errorf("image version = %q", got.ImageVersion)
	}
	if len(got.PackageManifestHash) != 64 {
		t.Errorf("package manifest hash = %q", got.PackageManifestHash)
	}
	if got.MDNSName != "lobby-screen.local" {
		t.Errorf("mdns name = %q", got.MDNSName)
	}
	if got.LastSyncResult != "never" {
		t.Errorf("last sync result = %q", got.LastSyncResult)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("warnings = %v", got.Warnings)
	}
}

func TestStatusWithNoSystemFiles(t *testing.T) {
	// Every number is zero and nothing panics. This is the Windows case.
	f := newRoots(t)
	got := New(f.src).Status(Inputs{Config: config.Default(), DeviceID: "px-00000000", ClockSynced: true})
	if got.UptimeSeconds != 0 || got.RAMTotalBytes != 0 || got.TempC != 0 {
		t.Errorf("status = %+v", got)
	}
	if got.DeviceID != "px-00000000" {
		t.Errorf("device id = %q", got.DeviceID)
	}
}

func TestMDNSNameOfANewDevice(t *testing.T) {
	f := newRoots(t)
	got := New(f.src).Status(Inputs{Config: config.Default(), DeviceID: "px-1a2b3c4d"})
	if got.MDNSName != "portapixel-3c4d.local" {
		t.Errorf("mdns name = %q", got.MDNSName)
	}
}

func TestWarnings(t *testing.T) {
	f := newRoots(t)
	// The default root password: the same hash in both files.
	f.write(t, filepath.Join(f.src.EtcRoot, "shadow"), "root:$6$abc$hash:19000:0:::::\nkiosk:!::0:::::\n")
	f.write(t, filepath.Join(f.src.StateDir, RootHashFile), "$6$abc$hash\n")

	cfg := config.Default() // the default web password and UTC
	got := New(f.src).Status(Inputs{
		Config:           cfg,
		DeviceID:         "px-1a2b3c4d",
		ConfigFromShadow: true,
		ClockSynced:      false,
		Problems:         []string{`The playlist "bad" is skipped: bad playlist file`},
		// The whole list goes to the device itself, to an admin with a session and to
		// the fleet server. TestUntrustedReportHoldsNoSecret covers the other caller.
		Trusted: true,
	})

	// The codes are the contract with the two admin UIs. The words are for a
	// person, so a better sentence must never break a banner.
	codes := map[string]string{}
	for _, w := range got.Warnings {
		codes[w.Code] = w.Message
	}
	for _, code := range []string{
		manifest.WarnWebPassword, manifest.WarnRootPassword, manifest.WarnTimezoneUTC,
		manifest.WarnClockUnsynced, manifest.WarnConfigShadow, manifest.WarnPlaylistProblem,
	} {
		if codes[code] == "" {
			t.Errorf("the warnings hold no %q: %+v", code, got.Warnings)
		}
	}
	if !strings.Contains(codes[manifest.WarnPlaylistProblem], "bad") {
		t.Errorf("the playlist warning reads %q", codes[manifest.WarnPlaylistProblem])
	}
}

// A mixer that refuses the volume is a warning and not a fault of the picture
// (D11). No fault means no warning: the report of a healthy device must stay
// short.
func TestAudioWarning(t *testing.T) {
	tests := []struct {
		name  string
		error string
		want  bool
	}{
		{name: "the mixer answers", error: ""},
		{name: "the mixer refused", error: "The volume is not set. amixer refused Master and PCM.", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRoots(t)
			got := New(f.src).Status(Inputs{
				Config:      config.Default(),
				ClockSynced: true,
				AudioError:  tt.error,
			})
			if has := hasCode(got.Warnings, manifest.WarnAudioApplyFailed); has != tt.want {
				t.Fatalf("the warning is %v, want %v: %+v", has, tt.want, got.Warnings)
			}
		})
	}
}

// The code of the configuration warning says which fault it is, so the dashboard
// can tell a repaired value from a hand edit that the daemon refused.
func TestConfigWarningCode(t *testing.T) {
	tests := []struct {
		name string
		code string
		want string
	}{
		{"a repaired value", manifest.WarnConfigRepaired, manifest.WarnConfigRepaired},
		{"a bad hand edit", manifest.WarnConfigBadEdit, manifest.WarnConfigBadEdit},
		{"no code at all", "", manifest.WarnConfigRepaired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRoots(t)
			got := New(f.src).Status(Inputs{
				Config:            config.Default(),
				ClockSynced:       true,
				ConfigWarning:     "display.rotation was 45",
				ConfigWarningCode: tt.code,
			})
			found := false
			for _, w := range got.Warnings {
				if w.Code == tt.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("the warnings hold no %q: %+v", tt.want, got.Warnings)
			}
		})
	}
}

// The codec report comes from a web page, so the daemon keeps the values that it
// knows and drops everything else.
func TestCleanCodecs(t *testing.T) {
	yes := true
	in := manifest.CodecReport{
		"h264":   {"1080": {Supported: true, Smooth: true, PowerEfficient: &yes}, "720": {}},
		"hevc":   {"2160": {Supported: false}},
		"madeUp": {"1080": {Supported: true}},
		"vp9":    {"999": {Supported: true}},
	}
	got := CleanCodecs(in)
	if len(got) != 2 {
		t.Fatalf("codecs = %+v, want h264 and hevc only", got)
	}
	if len(got["h264"]) != 1 || !got["h264"]["1080"].Supported {
		t.Errorf("h264 = %+v", got["h264"])
	}
	if _, ok := got["madeUp"]; ok {
		t.Error("a codec name that this product does not know was kept")
	}
	if _, ok := got["vp9"]; ok {
		t.Error("a codec with no size that this product knows was kept")
	}
	if CleanCodecs(nil) != nil {
		t.Error("CleanCodecs(nil) gave a map")
	}
}

func TestRootPasswordWarningNeedsBothFiles(t *testing.T) {
	f := newRoots(t)
	r := New(f.src)
	if r.rootPasswordIsDefault() {
		t.Error("the warning appeared with no files at all")
	}
	// A changed password: the hashes are different.
	f.write(t, filepath.Join(f.src.EtcRoot, "shadow"), "root:$6$new$hash:19000:0:::::\n")
	f.write(t, filepath.Join(f.src.StateDir, RootHashFile), "$6$abc$hash\n")
	if r.rootPasswordIsDefault() {
		t.Error("the warning appeared after the password changed")
	}
}

func TestTier(t *testing.T) {
	tests := []struct {
		name     string
		tier     string
		memTotal string
		model    string
		want     string
	}{
		{"the configuration wins", "low", "MemTotal: 8000000 kB\n", "", "low"},
		{"high is high", "high", "MemTotal: 400000 kB\n", "Raspberry Pi 3 Model B", "high"},
		{"little memory is low", "auto", "MemTotal: 500000 kB\n", "", "low"},
		// Every Raspberry Pi model that is too slow for a crossfade has 1 GiB or
		// less, so the memory says it and a list of model names said it again.
		{"a Pi 3 is low by its memory", "auto", "MemTotal: 1000000 kB\n", "Raspberry Pi 3 Model B Plus Rev 1.3", "low"},
		{"a Zero 2 is low by its memory", "auto", "MemTotal: 500000 kB\n", "Raspberry Pi Zero 2 W Rev 1.0", "low"},
		{"a Pi 4 is high", "auto", "MemTotal: 4000000 kB\n", "Raspberry Pi 4 Model B Rev 1.4", "high"},
		{"no facts at all is high", "auto", "", "", "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRoots(t)
			if tt.memTotal != "" {
				f.proc(t, "meminfo", tt.memTotal)
			}
			if tt.model != "" {
				f.proc(t, "device-tree/model", tt.model+"\x00")
			}
			cfg := config.Default()
			cfg.Device.Tier = tt.tier
			if got := New(f.src).Tier(cfg); got != tt.want {
				t.Errorf("Tier = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHosts(t *testing.T) {
	cfg := config.Default()
	cfg.Device.Name = "Lobby Screen"
	hosts := Hosts(cfg, "px-1a2b3c4d", 8099)

	joined := strings.Join(hosts, " ")
	for _, want := range []string{"localhost", "127.0.0.1", "lobby-screen.local", "localhost:8099"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the allowlist has no %q: %v", want, hosts)
		}
	}

	// A device with the factory name announces the name with the ID in it.
	factory := Hosts(config.Default(), "px-1a2b3c4d", 80)
	if !strings.Contains(strings.Join(factory, " "), "portapixel-3c4d.local") {
		t.Errorf("the allowlist has no mDNS name: %v", factory)
	}
}

// /api/status needs no session, so an untrusted caller must not read a secret
// from it.
//
// Two faults hid here. The two change-me warnings told a port sweep which box on
// the LAN still answers to the password that the manual prints, so every hit was a
// confirmed root shell and a confirmed admin session. And the URL of the item that
// plays now went out whole, and a signage dashboard link very often carries a
// share token in its query.
func TestUntrustedReportHoldsNoSecret(t *testing.T) {
	f := newRoots(t)
	// The default root password: the same hash in both files.
	f.write(t, filepath.Join(f.src.EtcRoot, "shadow"), "root:$6$abc$hash:19000:0:::::\nkiosk:!::0:::::\n")
	f.write(t, filepath.Join(f.src.StateDir, RootHashFile), "$6$abc$hash\n")

	in := Inputs{
		Config:    config.Default(), // the default web password
		DeviceID:  "px-1a2b3c4d",
		Paired:    true,
		ServerURL: "https://fleet.example.com/pp",
		SyncError: "dial tcp 10.1.2.3:443: connect: no route to host",
		NowPlaying: &manifest.NowPlaying{
			Playlist: "lobby", Index: 2, Kind: "url",
			Item: "https://dash.example.com/board?token=s3cr3t-share-token",
		},
	}

	// The trusted view keeps everything. The dashboard and the fleet need it.
	full := New(f.src).Status(withTrust(in, true))
	if full.ServerURL == "" || full.SyncError == "" {
		t.Fatal("the trusted report lost the fleet fields")
	}
	if !strings.Contains(full.NowPlaying.Item, "s3cr3t") {
		t.Fatal("the trusted report lost the URL of the item")
	}
	if !hasCode(full.Warnings, manifest.WarnWebPassword) || !hasCode(full.Warnings, manifest.WarnRootPassword) {
		t.Fatal("the trusted report lost a change-me warning")
	}

	got := New(f.src).Status(withTrust(in, false))
	if got.ServerURL != "" {
		t.Errorf("server_url = %q", got.ServerURL)
	}
	if got.SyncError != "" {
		t.Errorf("sync_error = %q", got.SyncError)
	}
	if hasCode(got.Warnings, manifest.WarnWebPassword) {
		t.Error("the report names a device whose web password is the default one")
	}
	if hasCode(got.Warnings, manifest.WarnRootPassword) {
		t.Error("the report names a device whose root password is the default one")
	}
	if got.NowPlaying == nil {
		t.Fatal("the report holds no now playing at all; the playlist and the kind may go out")
	}
	if got.NowPlaying.Item != "https://dash.example.com" {
		t.Errorf("the item is %q, want the scheme and the host only", got.NowPlaying.Item)
	}
	// The trusted report must not have changed: redact works on a copy.
	if !strings.Contains(in.NowPlaying.Item, "s3cr3t") {
		t.Error("redact changed the value that the caller gave it")
	}
	// The warnings that name no secret stay, so a monitor still sees a real fault.
	if !hasCode(got.Warnings, manifest.WarnTimezoneUTC) {
		t.Error("an untrusted caller lost the warnings that hold no secret")
	}
	// The facts that a login page and a monitor need stay.
	if got.DeviceID == "" || got.MDNSName == "" {
		t.Error("the report lost the identity of the device")
	}
	if !got.Paired {
		t.Error("the report no longer says that the device is paired")
	}
}

// An item name that is not an address must never go out whole: we cannot tell what
// is in it.
func TestUntrustedReportHidesAnItemThatIsNotAnAddress(t *testing.T) {
	f := newRoots(t)
	in := Inputs{
		Config:     config.Default(),
		DeviceID:   "px-1a2b3c4d",
		NowPlaying: &manifest.NowPlaying{Kind: "url", Item: "not an address at all"},
	}
	if got := New(f.src).Status(in).NowPlaying.Item; got != "url" {
		t.Errorf("the item is %q, want %q", got, "url")
	}
	// A picture keeps its file name: the names of the slides are not a secret, and
	// the dashboard of the fleet shows them.
	in.NowPlaying = &manifest.NowPlaying{Kind: "image", Item: "welcome.jpg"}
	if got := New(f.src).Status(in).NowPlaying.Item; got != "welcome.jpg" {
		t.Errorf("the item is %q, want the file name", got)
	}
}

// The hash of a file tells a caller nothing that the file name does not. It stays
// for every caller, and a URL item carries none, whatever the input says.
func TestUntrustedReportKeepsTheHashOfAFile(t *testing.T) {
	f := newRoots(t)
	sha := strings.Repeat("ab", 32)
	in := Inputs{
		Config:     config.Default(),
		DeviceID:   "px-1a2b3c4d",
		NowPlaying: &manifest.NowPlaying{Kind: "image", Item: "55efb67e-lab2-teal.png", SHA256: sha},
	}
	if got := New(f.src).Status(in).NowPlaying.SHA256; got != sha {
		t.Errorf("an untrusted caller got the hash %q, want %q", got, sha)
	}
	if got := New(f.src).Status(withTrust(in, true)).NowPlaying.SHA256; got != sha {
		t.Errorf("a trusted caller got the hash %q, want %q", got, sha)
	}
	in.NowPlaying = &manifest.NowPlaying{Kind: "url", Item: "https://dash.example.com/board?k=s3cr3t", SHA256: sha}
	if got := New(f.src).Status(in).NowPlaying.SHA256; got != "" {
		t.Errorf("an untrusted caller got the hash %q for a URL item", got)
	}
}

func withTrust(in Inputs, trusted bool) Inputs {
	in.Trusted = trusted
	return in
}

func hasCode(list []manifest.Warning, code string) bool {
	for _, w := range list {
		if w.Code == code {
			return true
		}
	}
	return false
}

// A low tier device with no zram swap gets a warning (final review 20).
//
// Chromium needs more memory than the engine of the first design. CONTEXT.md
// measured 512 MB with no swap as a restart loop, and 512 MB with zram as usable.
// The operating system switches zram on; the daemon only reports the state, so a
// person and the fleet dashboard can see a device that will not hold.
func TestZramWarning(t *testing.T) {
	tests := []struct {
		name  string
		ram   string
		swaps string
		want  bool
	}{
		{
			name:  "low tier and no swap at all",
			ram:   "MemTotal:         512000 kB\nMemAvailable:     200000 kB\n",
			swaps: "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n",
			want:  true,
		},
		{
			name:  "low tier with zram",
			ram:   "MemTotal:         512000 kB\nMemAvailable:     200000 kB\n",
			swaps: "Filename\t\t\t\tType\t\tSize\tUsed\tPriority\n/dev/zram0\tpartition\t524284\t0\t100\n",
			want:  false,
		},
		{
			name:  "low tier with swap on a disk, which is not zram",
			ram:   "MemTotal:         512000 kB\nMemAvailable:     200000 kB\n",
			swaps: "Filename\t\t\t\tType\t\tSize\tUsed\tPriority\n/dev/sda4\tpartition\t524284\t0\t-2\n",
			want:  true,
		},
		{
			name:  "high tier needs no zram",
			ram:   "MemTotal:        4014520 kB\nMemAvailable:    3012345 kB\n",
			swaps: "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n",
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRoots(t)
			f.proc(t, "meminfo", tt.ram)
			f.proc(t, "swaps", tt.swaps)

			got := New(f.src).Status(Inputs{Config: config.Default(), DeviceID: "px-1a2b3c4d", Trusted: true})
			if has := hasCode(got.Warnings, manifest.WarnZramOff); has != tt.want {
				t.Errorf("the zram warning is %v, want %v (tier %q)", has, tt.want, got.Tier)
			}
		})
	}
}
