package health

import (
	"os"
	"path/filepath"
	"slices"
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
		PlayerState:      "running",
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

	// A device with a name of its own also takes the factory name. The announcer
	// uses it when another device holds the name, and a name that is not on the
	// list answers 421.
	if !slices.Contains(hosts, "portapixel-3c4d.local") || !slices.Contains(hosts, "portapixel-3c4d.local:8099") {
		t.Errorf("the allowlist of a named device has no factory name: %v", hosts)
	}
}

// /api/status needs no session, so an untrusted caller must not read a secret
// from it.
//
// The two change-me warnings told a port sweep which box on the LAN still answers
// to the password that the manual prints, so every hit was a confirmed root shell
// and a confirmed admin session.
func TestUntrustedReportHoldsNoSecret(t *testing.T) {
	f := newRoots(t)
	// The default root password: the same hash in both files.
	f.write(t, filepath.Join(f.src.EtcRoot, "shadow"), "root:$6$abc$hash:19000:0:::::\nkiosk:!::0:::::\n")
	f.write(t, filepath.Join(f.src.StateDir, RootHashFile), "$6$abc$hash\n")

	in := Inputs{
		Config:     config.Default(), // the default web password
		DeviceID:   "px-1a2b3c4d",
		Paired:     true,
		ServerURL:  "https://fleet.example.com/pp",
		SyncError:  "dial tcp 10.1.2.3:443: connect: no route to host",
		NowPlaying: &manifest.NowPlaying{Playlist: "lobby", Index: 2, Kind: "image", Item: "welcome.jpg"},
	}

	// The trusted view keeps everything. The dashboard and the fleet need it.
	full := New(f.src).Status(withTrust(in, true))
	if full.ServerURL == "" || full.SyncError == "" {
		t.Fatal("the trusted report lost the fleet fields")
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
	// A picture keeps its file name: the names of the slides are not a secret, and
	// the dashboard of the fleet shows them.
	if got.NowPlaying == nil || got.NowPlaying.Item != "welcome.jpg" {
		t.Errorf("now playing is %+v, want the file name", got.NowPlaying)
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

// The hash of a file tells a caller nothing that the file name does not. It stays
// for every caller.
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

// A device with less than 1 GiB of memory and no zram swap gets a warning (final
// review 20). The memory is the whole test, the same as the zram rule of
// os/install.sh. The operating system switches zram on; the daemon only reports
// the state, so a person and the fleet dashboard can see a device that will not
// hold.
func TestZramWarning(t *testing.T) {
	tests := []struct {
		name  string
		ram   string
		swaps string
		want  bool
	}{
		{
			name:  "little memory and no swap at all",
			ram:   "MemTotal:         512000 kB\nMemAvailable:     200000 kB\n",
			swaps: "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n",
			want:  true,
		},
		{
			name:  "little memory with zram",
			ram:   "MemTotal:         512000 kB\nMemAvailable:     200000 kB\n",
			swaps: "Filename\t\t\t\tType\t\tSize\tUsed\tPriority\n/dev/zram0\tpartition\t524284\t0\t100\n",
			want:  false,
		},
		{
			name:  "little memory with swap on a disk, which is not zram",
			ram:   "MemTotal:         512000 kB\nMemAvailable:     200000 kB\n",
			swaps: "Filename\t\t\t\tType\t\tSize\tUsed\tPriority\n/dev/sda4\tpartition\t524284\t0\t-2\n",
			want:  true,
		},
		{
			name:  "enough memory needs no zram",
			ram:   "MemTotal:        4014520 kB\nMemAvailable:    3012345 kB\n",
			swaps: "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n",
			want:  false,
		},
		{
			name:  "no memory number is not little memory",
			swaps: "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n",
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRoots(t)
			if tt.ram != "" {
				f.proc(t, "meminfo", tt.ram)
			}
			f.proc(t, "swaps", tt.swaps)

			got := New(f.src).Status(Inputs{Config: config.Default(), DeviceID: "px-1a2b3c4d", Trusted: true})
			if has := hasCode(got.Warnings, manifest.WarnZramOff); has != tt.want {
				t.Errorf("the zram warning is %v, want %v (memory %d)", has, tt.want, got.RAMTotalBytes)
			}
		})
	}
}
