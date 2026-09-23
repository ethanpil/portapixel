package syncer

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// TestCommandsRunOnceAndAreAckedAgain is the re-delivery rule. The server sends a
// command again when no acknowledgement arrives, so a repeated ID is acknowledged
// and never run twice.
func TestCommandsRunOnceAndAreAckedAgain(t *testing.T) {
	f := newFakeServer(t)
	f.setManifest(manifest.Manifest{Commands: []manifest.Command{
		{ID: 7, Type: CmdRescan},
		{ID: 8, Type: CmdRestartBrowser},
	}})

	d := pairedDev(t, f)
	d.s.Once(context.Background())

	if got := d.commands; !slices.Equal(got, []string{CmdRescan, CmdRestartBrowser}) {
		t.Fatalf("the device ran %v", got)
	}
	if len(f.beats) != 1 || !slices.Equal(f.beats[0].Acks, []int64{7, 8}) {
		t.Fatalf("the heartbeat acknowledged %v", f.beats[0].Acks)
	}
	// The IDs are in state.json, so a reboot does not forget them.
	if saved := readState(t, d.state); !saved.RanCommand(7) || !saved.RanCommand(8) {
		t.Errorf("state.json holds %v", saved.Commands)
	}

	// The server sends the same commands again.
	d.s.Once(context.Background())
	if got := d.commands; len(got) != 2 {
		t.Errorf("the device ran a command twice: %v", got)
	}
	if len(f.beats) != 2 || !slices.Equal(f.beats[1].Acks, []int64{7, 8}) {
		t.Errorf("the second heartbeat acknowledged %v", f.beats[1].Acks)
	}
}

// TestRebootIsAckedBeforeTheMachineGoesDown is the rule of the reboot command: the
// ID is on the disk and the acknowledgement is on the wire before anything reboots.
func TestRebootIsAckedBeforeTheMachineGoesDown(t *testing.T) {
	f := newFakeServer(t)
	f.setManifest(manifest.Manifest{Commands: []manifest.Command{
		{ID: 3, Type: CmdRescan},
		{ID: 4, Type: CmdReboot},
		{ID: 5, Type: CmdRestartBrowser},
	}})

	d := pairedDev(t, f)
	// The order of the calls says if the acknowledgement came first. The command
	// hook records the reboot, and the heartbeat is on the fake server.
	acksWhenRebooting := -1
	d.s.opt.Command = func(name string) error {
		d.commands = append(d.commands, name)
		if name == CmdReboot {
			f.mu.Lock()
			acksWhenRebooting = len(f.beats)
			f.mu.Unlock()
		}
		return nil
	}

	d.s.Once(context.Background())

	if acksWhenRebooting != 1 {
		t.Errorf("the device had sent %d heartbeats when it rebooted, want 1", acksWhenRebooting)
	}
	if len(f.beats) != 1 || !slices.Equal(f.beats[0].Acks, []int64{3, 4}) {
		t.Fatalf("the heartbeat acknowledged %v", f.beats[0].Acks)
	}
	// The command after the reboot did not run: the machine is going down.
	if got := d.commands; !slices.Equal(got, []string{CmdRescan, CmdReboot}) {
		t.Errorf("the device ran %v", got)
	}
	if saved := readState(t, d.state); !saved.RanCommand(4) {
		t.Error("the reboot ID is not in state.json")
	}
}

// TestUpdateCommandAsksTheUpdater covers D28: the fleet command is what applies an
// approved release when [updates] auto is off.
func TestUpdateCommandAsksTheUpdater(t *testing.T) {
	f := newFakeServer(t)
	f.setManifest(manifest.Manifest{Commands: []manifest.Command{{ID: 9, Type: CmdUpdate}}})

	d := pairedDev(t, f)
	d.s.Once(context.Background())

	if d.updates != 1 {
		t.Errorf("the device asked the updater %d times", d.updates)
	}
	if !slices.Equal(f.beats[0].Acks, []int64{9}) {
		t.Errorf("the heartbeat acknowledged %v", f.beats[0].Acks)
	}
}

// TestAnUnknownCommandIsAcked keeps a newer server from stopping an older device.
func TestAnUnknownCommandIsAcked(t *testing.T) {
	f := newFakeServer(t)
	f.setManifest(manifest.Manifest{Commands: []manifest.Command{
		{ID: 11, Type: "take-a-screenshot"},
	}})

	d := pairedDev(t, f)
	d.s.Once(context.Background())

	if len(d.commands) != 0 {
		t.Errorf("the device ran %v", d.commands)
	}
	if !slices.Equal(f.beats[0].Acks, []int64{11}) {
		t.Errorf("the heartbeat acknowledged %v", f.beats[0].Acks)
	}
}

// TestRenameSavesTheNameAndTheHeartbeatReportsIt is the rename command of the
// fleet server. The device saves the name, and the heartbeat of the same round
// carries it, so the server row changes in one poll.
func TestRenameSavesTheNameAndTheHeartbeatReportsIt(t *testing.T) {
	f := newFakeServer(t)
	f.setManifest(manifest.Manifest{Commands: []manifest.Command{
		{ID: 21, Type: CmdRename, Args: map[string]string{"name": "  Front desk  "}},
	}})

	d := pairedDev(t, f)
	d.s.Once(context.Background())

	if !slices.Equal(d.names, []string{"Front desk"}) {
		t.Fatalf("the device saved %q", d.names)
	}
	if len(f.beats) != 1 || !slices.Equal(f.beats[0].Acks, []int64{21}) {
		t.Fatalf("the heartbeat acknowledged %v", f.beats[0].Acks)
	}
	if got := f.beats[0].Name; got != "Front desk" {
		t.Errorf("the heartbeat reported the name %q", got)
	}

	// The server sends the command again after a lost acknowledgement. The device
	// acknowledges it and does not save again.
	d.s.Once(context.Background())
	if len(d.names) != 1 {
		t.Errorf("a command that came again saved %q", d.names)
	}
	if !slices.Equal(f.beats[1].Acks, []int64{21}) {
		t.Errorf("the second heartbeat acknowledged %v", f.beats[1].Acks)
	}
}

// A name that the device cannot use, and a save that fails, are both acknowledged
// with a line in the ops log. The name stays, and the round goes on.
func TestABadRenameIsAckedAndLogged(t *testing.T) {
	f := newFakeServer(t)
	f.setManifest(manifest.Manifest{Commands: []manifest.Command{
		{ID: 31, Type: CmdRename, Args: map[string]string{"name": "two\nlines"}},
		{ID: 32, Type: CmdRename},
		{ID: 33, Type: CmdRename, Args: map[string]string{"name": strings.Repeat("x", manifest.MaxNameLength+1)}},
		{ID: 34, Type: CmdRescan},
	}})

	d := pairedDev(t, f)
	d.s.Once(context.Background())

	if len(d.names) != 0 {
		t.Errorf("the device saved %q", d.names)
	}
	if d.cfg.Device.Name != "Lobby" {
		t.Errorf("the name is %q, want it unchanged", d.cfg.Device.Name)
	}
	if !slices.Equal(f.beats[0].Acks, []int64{31, 32, 33, 34}) {
		t.Errorf("the heartbeat acknowledged %v", f.beats[0].Acks)
	}
	if !slices.Equal(d.commands, []string{CmdRescan}) {
		t.Errorf("the round stopped: the device ran %v", d.commands)
	}
	if tail := d.opsTail(t); strings.Count(tail, "sync.command.rename") != 3 || strings.Contains(tail, "two\nlines") {
		t.Errorf("the ops log is:\n%s", tail)
	}

	// The save path of the daemon refuses the name.
	f.setManifest(manifest.Manifest{Commands: []manifest.Command{
		{ID: 35, Type: CmdRename, Args: map[string]string{"name": "Front desk"}},
	}})
	d.nameErr = errors.New("device.name: must not be empty")
	d.s.Once(context.Background())
	if !slices.Contains(f.beats[1].Acks, 35) {
		t.Errorf("the heartbeat acknowledged %v", f.beats[1].Acks)
	}
	if tail := d.opsTail(t); !strings.Contains(tail, "cannot save the name") {
		t.Errorf("the ops log is:\n%s", tail)
	}
}

// TestTheCommandListStaysBounded keeps state.json from growing for the life of the
// device.
func TestTheCommandListStaysBounded(t *testing.T) {
	f := newFakeServer(t)
	d := pairedDev(t, f)

	for id := int64(1); id <= 150; id++ {
		f.setManifest(manifest.Manifest{Commands: []manifest.Command{{ID: id, Type: CmdRescan}}})
		d.s.Once(context.Background())
	}
	saved := readState(t, d.state)
	if len(saved.Commands) != 100 {
		t.Errorf("state.json holds %d IDs, want 100", len(saved.Commands))
	}
	if saved.RanCommand(1) {
		t.Error("the oldest ID is still in the list")
	}
	if !saved.RanCommand(150) {
		t.Error("the newest ID is not in the list")
	}
}

// TestUpdateSourceIsTheFleetMirrorWhilePaired is D28: the GitHub source is off
// while a device is paired.
func TestUpdateSourceIsTheFleetMirrorWhilePaired(t *testing.T) {
	f := newFakeServer(t)
	f.setManifest(manifest.Manifest{
		Release: &manifest.ReleaseRef{Version: "1.6.0", BaseURL: "/api/v1/releases/1.6.0"},
	})

	d := newDev(t, f)
	if src, ok := d.s.UpdateSource(); ok {
		t.Fatalf("a standalone device offered the fleet source %+v", src)
	}

	if _, err := d.s.Pair(context.Background(), f.srv.URL, f.enrollToken, true); err != nil {
		t.Fatalf("pair: %v", err)
	}
	d.s.Once(context.Background())

	src, ok := d.s.UpdateSource()
	if !ok {
		t.Fatal("a paired device offered no fleet source")
	}
	if src.Repo != "" {
		t.Errorf("the fleet source names the GitHub repository %q", src.Repo)
	}
	if src.Version != "1.6.0" || src.BaseURL != "/api/v1/releases/1.6.0" {
		t.Errorf("the source is %+v", src)
	}
	if src.Bearer != f.deviceToken || src.ServerURL != f.srv.URL {
		t.Errorf("the source carries %q and %q", src.Bearer, src.ServerURL)
	}

	// The name of the server is what the local refusals say (D48).
	if name, paired := d.s.Managed(); !paired || name != "Test fleet" {
		t.Errorf("Managed gave %q and %v", name, paired)
	}
}

// The boundary of D48 is exactly what the manifest carries. Everything else stays
// with the local admin while the device is paired.
func TestManagedFieldTable(t *testing.T) {
	managed := []string{
		"playback.default_playlist",
		"schedule", "display.on_time", "display.off_time", "display.power_days",
	}
	local := []string{
		"device.name", "device.timezone", "device.tier",
		"network.mode", "network.address", "network.gateway", "network.dns",
		"network.wifi_ssid", "network.wifi_psk", "network.wifi_country",
		"display.rotation", "display.video_mode", "display.power_method",
		"audio.output", "audio.volume",
		// The local defaults of this screen. A fleet playlist carries its own
		// transition and its own shuffle, so the server already says how its content
		// plays.
		"playback.transition", "playback.transition_ms", "playback.image_duration",
		"playback.shuffle", "playback.nightly_restart",
		// The server gates which release is approved (D28). Whether this device
		// installs it without a person belongs to the owner of the screen.
		"updates.auto",
		"server.url", "server.token", "server.poll_seconds",
		"web.port", "web.password", "ssh.enabled", "logging.persist",
	}
	for _, field := range managed {
		if !ManagedField(field) {
			t.Errorf("%s must belong to the fleet server (D48)", field)
		}
	}
	for _, field := range local {
		if ManagedField(field) {
			t.Errorf("%s must stay with the local admin (D48)", field)
		}
	}
}
