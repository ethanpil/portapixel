package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethanpil/portapixel/internal/opslog"
)

// write makes a file and every directory above it.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nic makes a fake interface in the fake sysfs.
func nic(t *testing.T, root, name, mac, assign string, physical bool) {
	t.Helper()
	dir := filepath.Join(root, netDir, name)
	write(t, filepath.Join(dir, "address"), mac+"\n")
	if assign != "" {
		write(t, filepath.Join(dir, "addr_assign_type"), assign+"\n")
	}
	if physical {
		// A real device link points at a bus. A directory is enough here: the
		// code only asks if the name exists.
		if err := os.MkdirAll(filepath.Join(dir, "device"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDerivePiSerialWins(t *testing.T) {
	root := t.TempDir()
	// The device tree ends the value with a NUL character.
	write(t, filepath.Join(root, piSerialPath), "100000003d1f4e2a\x00")
	write(t, filepath.Join(root, dmiUUIDPath), "4c4c4544-0037-3010-8057-b8c04f503432")
	nic(t, root, "eth0", "dc:a6:32:11:22:33", "0", true)

	got := Derive(root)
	if got.Source != SourcePiSerial {
		t.Fatalf("source = %q, want %q", got.Source, SourcePiSerial)
	}
	if !strings.HasPrefix(got.DeviceID, "px-") || len(got.DeviceID) != 11 {
		t.Fatalf("device id %q has the wrong shape", got.DeviceID)
	}
	if len(got.HardwareID) != 64 {
		t.Fatalf("hardware id %q is not a full sha256", got.HardwareID)
	}
	if got.DeviceID[3:] != got.HardwareID[:8] {
		t.Fatalf("the device id is not the start of the hardware id")
	}
	// The same input must always give the same identity.
	if again := Derive(root); again != got {
		t.Fatalf("Derive is not stable: %+v then %+v", got, again)
	}
}

func TestDeriveDMI(t *testing.T) {
	tests := []struct {
		name  string
		uuid  string
		wants string // the source that must win
	}{
		{"good uuid", "4c4c4544-0037-3010-8057-b8c04f503432", SourceDMIUUID},
		{"all zeros", "00000000-0000-0000-0000-000000000000", SourceMAC},
		{"all f", "ffffffff-ffff-ffff-ffff-ffffffffffff", SourceMAC},
		{"not settable", "Not Settable", SourceMAC},
		{"default string", "Default string", SourceMAC},
		{"too short", "4c4c4544-0037", SourceMAC},
		{"empty", "", SourceMAC},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, dmiUUIDPath), tt.uuid+"\n")
			nic(t, root, "eth0", "dc:a6:32:11:22:33", "0", true)
			if got := Derive(root); got.Source != tt.wants {
				t.Fatalf("source = %q, want %q", got.Source, tt.wants)
			}
		})
	}
}

func TestFirstMACSkipsVirtualAndPicksLowestName(t *testing.T) {
	root := t.TempDir()
	nic(t, root, "lo", "00:00:00:00:00:00", "0", false)
	nic(t, root, "docker0", "02:42:aa:bb:cc:dd", "3", false)
	nic(t, root, "veth1234", "02:42:11:22:33:44", "3", false)
	nic(t, root, "wlan0", "aa:bb:cc:dd:ee:02", "0", true)
	nic(t, root, "enp3s0", "aa:bb:cc:dd:ee:01", "0", true)
	// A software interface with a device link but a random address.
	nic(t, root, "eth9", "aa:bb:cc:dd:ee:99", "1", true)

	if got := firstMAC(root); got != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("firstMAC = %q, want the enp3s0 address", got)
	}
}

func TestFirstMACNeedsAPhysicalInterface(t *testing.T) {
	root := t.TempDir()
	nic(t, root, "br0", "aa:bb:cc:dd:ee:01", "0", false)
	if got := firstMAC(root); got != "" {
		t.Fatalf("firstMAC = %q, want an empty answer", got)
	}
}

func TestDeriveFallsBackToTheHostName(t *testing.T) {
	root := t.TempDir() // nothing in it
	got := Derive(root)
	if got.Source != SourceHostname {
		t.Fatalf("source = %q, want %q", got.Source, SourceHostname)
	}
	if got.DeviceID == "" || got.HardwareID == "" {
		t.Fatalf("Derive gave no identity: %+v", got)
	}
}

func TestResolveFirstBoot(t *testing.T) {
	dir := t.TempDir()
	log := opslog.New(filepath.Join(dir, "ops.log"))
	id := Identity{DeviceID: "px-11112222", HardwareID: strings.Repeat("a", 64), Source: SourceMAC}

	state, err := Resolve(dir, id, log)
	if err != nil {
		t.Fatal(err)
	}
	if state.DeviceID != id.DeviceID || state.HardwareChanged {
		t.Fatalf("state = %+v", state)
	}
	// The file must hold the same values.
	again, err := LoadState(dir)
	if err != nil || again.HardwareID != id.HardwareID {
		t.Fatalf("LoadState gave %+v, %v", again, err)
	}
}

func TestResolveRepairKeepsThePairing(t *testing.T) {
	dir := t.TempDir()
	log := opslog.New(filepath.Join(dir, "ops.log"))
	old := State{
		DeviceID:    "px-11112222",
		HardwareID:  strings.Repeat("a", 64),
		DeviceToken: "token-123",
		ServerURL:   "https://fleet.example.com",
	}
	if err := old.Save(dir); err != nil {
		t.Fatal(err)
	}

	next := Identity{DeviceID: "px-33334444", HardwareID: strings.Repeat("b", 64), Source: SourceDMIUUID}
	state, err := Resolve(dir, next, log)
	if err != nil {
		t.Fatal(err)
	}
	if state.DeviceID != next.DeviceID || state.HardwareID != next.HardwareID {
		t.Fatalf("the device did not adopt the new identity: %+v", state)
	}
	if !state.HardwareChanged {
		t.Fatalf("HardwareChanged is false after a repair")
	}
	if state.DeviceToken != "token-123" || state.ServerURL != old.ServerURL {
		t.Fatalf("the repair lost the pairing: %+v", state)
	}
	if entries := log.Tail(10); len(entries) == 0 || entries[len(entries)-1].Event != "identity.repair" {
		t.Fatalf("no identity.repair line in the ops log: %+v", entries)
	}
}

func TestResolveSameIdentityChangesNothing(t *testing.T) {
	dir := t.TempDir()
	log := opslog.New(filepath.Join(dir, "ops.log"))
	id := Identity{DeviceID: "px-11112222", HardwareID: strings.Repeat("a", 64), Source: SourceMAC}
	if _, err := Resolve(dir, id, log); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(StatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	state, err := Resolve(dir, id, log)
	if err != nil {
		t.Fatal(err)
	}
	if state.HardwareChanged {
		t.Fatalf("HardwareChanged is true without a change")
	}
	info2, err := os.Stat(StatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !info2.ModTime().Equal(info.ModTime()) {
		t.Fatalf("Resolve wrote the state file again without a change")
	}
}

func TestLoadStateBadFile(t *testing.T) {
	dir := t.TempDir()
	write(t, StatePath(dir), "{not json")
	if _, err := LoadState(dir); err == nil {
		t.Fatalf("LoadState accepted a damaged file")
	}
}

func TestMarkBadReleaseIsIdempotent(t *testing.T) {
	var s State
	s.MarkBadRelease("1.2.3")
	s.MarkBadRelease("1.2.3")
	s.MarkBadRelease("")
	if len(s.BadReleases) != 1 {
		t.Fatalf("BadReleases = %v", s.BadReleases)
	}
}
