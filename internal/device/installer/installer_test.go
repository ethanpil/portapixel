package installer

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// allowOrdinaryFiles lets a test write to the files that it made itself.
//
// The rule on a device is that the target of a partition copy is a block device
// node (see openTarget). A test cannot make such a node: that needs root rights
// and a real disk. So a test replaces the rule and nothing else, and
// TestTheRealTargetCheck proves the real rule on Linux.
func allowOrdinaryFiles(t *testing.T) {
	t.Helper()
	old := isBlockDevice
	isBlockDevice = func(string, os.FileInfo) error { return nil }
	t.Cleanup(func() { isBlockDevice = old })
}

// fakeRunner records every program call and can make one of them fail.
//
// after runs when a call worked. The kernel of a real machine makes the partition
// nodes some time after partx, and the install waits for them, so the fake has to
// make them as well.
type fakeRunner struct {
	calls []string
	fail  map[string]bool
	after func(name string, args []string)
}

func newRunner() *fakeRunner { return &fakeRunner{fail: map[string]bool{}} }

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	if f.fail[name] {
		return []byte("the tool says no"), fmt.Errorf("exit status 1")
	}
	if f.after != nil {
		f.after(name, args)
	}
	return nil, nil
}

func (f *fakeRunner) joined() string { return strings.Join(f.calls, " | ") }

// machine is a fake /sys, /proc and /dev. The partitions are ordinary files, so the
// copy is a real copy that a test can compare byte for byte.
type machine struct {
	t        *testing.T
	sys      string
	proc     string
	dev      string
	media    string
	mount    string
	run      *fakeRunner
	inst     *Installer
	bootBody []byte
	rootBody []byte
	mbrBody  []byte
}

const (
	bootBytes = 4 << 20  // p1 of the fake stick
	rootBytes = 12 << 20 // p2 of the fake stick
)

// newMachine builds a stick (sda, 64 MB) and a disk (sdb, 2 GB).
func newMachine(t *testing.T) *machine {
	t.Helper()
	allowOrdinaryFiles(t)
	base := t.TempDir()
	m := &machine{
		t:     t,
		sys:   filepath.Join(base, "sys"),
		proc:  filepath.Join(base, "proc"),
		dev:   filepath.Join(base, "dev"),
		media: filepath.Join(base, "media"),
		mount: filepath.Join(base, "run"),
		run:   newRunner(),
	}
	mkdir(t, m.dev, m.media, m.mount)

	// The stick: three partitions, the two that matter hold random bytes.
	m.block("sda", 64<<20, false, false, "Generic", "Flash Disk")
	m.part("sda", "sda1", bootBytes)
	m.part("sda", "sda2", rootBytes)
	m.part("sda", "sda3", 40<<20)
	m.bootBody = randomBody(t, bootBytes)
	m.rootBody = randomBody(t, rootBytes)
	m.mbrBody = randomBody(t, 512)
	write(t, filepath.Join(m.dev, "sda1"), m.bootBody)
	write(t, filepath.Join(m.dev, "sda2"), m.rootBody)
	write(t, filepath.Join(m.dev, "sda"), m.mbrBody)

	// The target disk. Its partition nodes are NOT there: a real kernel makes them
	// after it has read the new table, and the install must wait for them.
	m.block("sdb", 2<<30, false, false, "Samsung", "SSD 860 EVO")
	write(t, filepath.Join(m.dev, "sdb"), make([]byte, 512))
	m.run.after = func(name string, args []string) {
		if name != "partx" && name != "partprobe" {
			return
		}
		for n := 1; n <= 3; n++ {
			write(t, filepath.Join(m.dev, fmt.Sprintf("sdb%d", n)), nil)
		}
	}

	// The kernel says that the root filesystem is the second partition of the
	// stick.
	write(t, filepath.Join(m.proc, "mounts"), []byte(
		"proc /proc proc rw 0 0\n"+
			filepath.Join(m.dev, "sda2")+" / ext4 rw,noatime 0 0\n"))

	// Content on the media partition of the source.
	write(t, filepath.Join(m.media, "portapixel.toml"), []byte("[device]\nname = \"Lobby\"\n"))
	write(t, filepath.Join(m.media, "default", "playlist.toml"), []byte("[[item]]\nfile = \"a.jpg\"\n"))
	write(t, filepath.Join(m.media, "default", "a.jpg"), randomBody(t, 1024))

	m.inst = New(Options{
		SysRoot: m.sys, ProcRoot: m.proc, DevRoot: m.dev,
		MediaRoot: m.media, MountRoot: m.mount, Arch: "amd64", Run: m.run,
	})
	return m
}

// block writes the /sys/block entry of a disk.
func (m *machine) block(name string, size int64, removable, readOnly bool, vendor, model string) {
	dir := filepath.Join(m.sys, "block", name)
	mkdir(m.t, dir, filepath.Join(dir, "device"))
	write(m.t, filepath.Join(dir, "size"), []byte(fmt.Sprint(size/sectorSize)))
	write(m.t, filepath.Join(dir, "removable"), []byte(boolText(removable)))
	write(m.t, filepath.Join(dir, "ro"), []byte(boolText(readOnly)))
	write(m.t, filepath.Join(dir, "device", "vendor"), []byte(vendor+"\n"))
	write(m.t, filepath.Join(dir, "device", "model"), []byte(model+"\n"))
}

// part writes the /sys/block entry of a partition.
func (m *machine) part(disk, name string, size int64) {
	dir := filepath.Join(m.sys, "block", disk, name)
	mkdir(m.t, dir)
	write(m.t, filepath.Join(dir, "size"), []byte(fmt.Sprint(size/sectorSize)))
}

func boolText(v bool) string {
	if v {
		return "1\n"
	}
	return "0\n"
}

func mkdir(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, path string, body []byte) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func randomBody(t *testing.T, n int64) []byte {
	t.Helper()
	body := make([]byte, n)
	if _, err := rand.Read(body); err != nil {
		t.Fatal(err)
	}
	return body
}

// ------------------------------------------------------------------ the tests

// The disk list must leave out everything that can never be a target, and it must
// never offer the disk that the system runs from.
func TestDisks(t *testing.T) {
	m := newMachine(t)
	m.block("loop0", 100<<20, false, false, "", "")
	m.block("zram0", 100<<20, false, false, "", "")
	m.block("sr0", 700<<20, true, false, "", "DVD")
	m.block("sdc", 16<<20, true, false, "Tiny", "Card") // too small
	m.block("sdd", 2<<30, false, true, "Locked", "SSD") // read only

	disks, err := m.inst.Disks()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range disks {
		names = append(names, filepath.Base(d.Device))
	}
	want := []string{"sdb", "sdc"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("disks = %v, want %v", names, want)
	}
	if disks[0].TooSmall {
		t.Error("sdb is marked too small")
	}
	if !disks[1].TooSmall {
		t.Error("sdc is not marked too small")
	}
	if disks[0].Model != "Samsung SSD 860 EVO" {
		t.Errorf("model = %q", disks[0].Model)
	}
	if disks[0].SizeBytes != 2<<30 {
		t.Errorf("size = %d", disks[0].SizeBytes)
	}
}

// Every refusal of D54. There is no undo, so each one must answer before anything is
// written.
func TestCheckRefusals(t *testing.T) {
	tests := []struct {
		name     string
		device   string
		confirm  string
		wantText string
	}{
		{name: "a good target", device: "sdb", confirm: "sdb"},
		{name: "the boot disk", device: "sda", confirm: "sda", wantText: "runs from"},
		{name: "no confirmation", device: "sdb", confirm: "", wantText: "type the name"},
		{name: "the wrong name typed back", device: "sdb", confirm: "sdc", wantText: "type the name"},
		{name: "a disk that is too small", device: "sdc", confirm: "sdc", wantText: "needs"},
		{name: "a disk that is not there", device: "sdz", confirm: "sdz", wantText: "not a disk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t)
			m.block("sdc", 16<<20, true, false, "Tiny", "Card")

			device := filepath.ToSlash(filepath.Join(m.dev, tt.device))
			confirm := tt.confirm
			if confirm != "" {
				confirm = filepath.ToSlash(filepath.Join(m.dev, tt.confirm))
			}

			_, _, err := m.inst.Check(device, confirm)
			if tt.wantText == "" {
				if err != nil {
					t.Fatalf("Check() = %v, want no error", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Check() gave no error")
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("Check() = %v, want a message with %q in it", err, tt.wantText)
			}
		})
	}
}

// The destructive core. The partitions are files, so the test compares the bytes.
// This is the test that says that the install copies and does not corrupt.
func TestRunCopiesEverything(t *testing.T) {
	m := newMachine(t)
	target := filepath.ToSlash(filepath.Join(m.dev, "sdb"))

	var events []Progress
	var done Done
	send := func(name string, data any) {
		switch name {
		case "progress":
			events = append(events, data.(Progress))
		case "done":
			done = data.(Done)
		}
	}
	m.inst.Run(context.Background(), target, target, send)

	if !done.OK {
		t.Fatalf("done = %+v", done)
	}
	if done.Instruction != FinalInstruction {
		t.Errorf("instruction = %q", done.Instruction)
	}

	// The two block copies are byte for byte.
	same(t, filepath.Join(m.dev, "sdb1"), m.bootBody)
	same(t, filepath.Join(m.dev, "sdb2"), m.rootBody)

	// The MBR boot code came across, and nothing past it.
	head, err := os.ReadFile(filepath.Join(m.dev, "sdb"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(head[:mbrCodeBytes], m.mbrBody[:mbrCodeBytes]) {
		t.Error("the boot code of the target is not the boot code of the source")
	}
	if bytes.Equal(head[mbrCodeBytes:512], m.mbrBody[mbrCodeBytes:512]) {
		t.Error("the copy went past the boot code and into the partition table")
	}

	// The media files landed in the mount point.
	for _, rel := range []string{"portapixel.toml", "default/playlist.toml", "default/a.jpg"} {
		source := filepath.Join(m.media, filepath.FromSlash(rel))
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		same(t, filepath.Join(m.mount, "install-media", filepath.FromSlash(rel)), body)
	}

	// The programs, in order, with the labels and the types of the image.
	calls := m.run.joined()
	for _, want := range []string{
		"sgdisk --zap-all " + target,
		"--attributes=1:set:2",
		"-c 1:PPBOOT",
		"-c 2:PPROOT",
		"-c 3:PPMEDIA",
		"-t 1:ef00",
		"-t 2:8300",
		"-t 3:0700",
		"mkfs.exfat -L PPMEDIA " + target + "3",
		"mount " + target + "3",
		"umount ",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("the program calls hold no %q:\n%s", want, calls)
		}
	}

	// The progress never goes backwards and it ends at the top.
	last := -1
	for _, e := range events {
		if e.Percent < last {
			t.Fatalf("the progress went from %d back to %d at %q", last, e.Percent, e.Phase)
		}
		last = e.Percent
	}
	if last < 98 {
		t.Errorf("the last progress event is %d per cent", last)
	}
}

// A Raspberry Pi has no boot code outside its partitions, so the install must not
// write over the head of the disk.
func TestNoBootCodeOnARaspberryPi(t *testing.T) {
	m := newMachine(t)
	m.inst = New(Options{
		SysRoot: m.sys, ProcRoot: m.proc, DevRoot: m.dev,
		MediaRoot: m.media, MountRoot: m.mount, Arch: "arm64", Run: m.run,
	})
	target := filepath.ToSlash(filepath.Join(m.dev, "sdb"))
	before, err := os.ReadFile(filepath.Join(m.dev, "sdb"))
	if err != nil {
		t.Fatal(err)
	}

	var done Done
	m.inst.Run(context.Background(), target, target, func(name string, data any) {
		if name == "done" {
			done = data.(Done)
		}
	})
	if !done.OK {
		t.Fatalf("done = %+v", done)
	}
	after, err := os.ReadFile(filepath.Join(m.dev, "sdb"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before[:mbrCodeBytes], after[:mbrCodeBytes]) {
		t.Error("the install wrote boot code on an arm64 machine")
	}
	if strings.Contains(m.run.joined(), "--attributes") {
		t.Error("the install set the legacy boot attribute on an arm64 machine")
	}
}

// A program that fails must stop the install and say what failed, and the stream
// must end with a done event that carries the reason.
func TestAFailedProgramStopsTheInstall(t *testing.T) {
	tests := []string{"sgdisk", "mkfs.exfat", "mount"}
	for _, tool := range tests {
		t.Run(tool+" fails", func(t *testing.T) {
			m := newMachine(t)
			m.run.fail[tool] = true
			target := filepath.ToSlash(filepath.Join(m.dev, "sdb"))

			var done Done
			count := 0
			m.inst.Run(context.Background(), target, target, func(name string, data any) {
				if name == "done" {
					done = data.(Done)
					count++
				}
			})
			if done.OK {
				t.Fatal("done says the install worked")
			}
			if done.Error == "" {
				t.Error("done carries no reason")
			}
			if count != 1 {
				t.Errorf("%d done events, want 1", count)
			}
		})
	}
}

// One install at a time. Two of them on one machine would write over each other.
func TestOneInstallAtATime(t *testing.T) {
	m := newMachine(t)
	m.mu()
	target := filepath.ToSlash(filepath.Join(m.dev, "sdb"))

	var done Done
	m.inst.Run(context.Background(), target, target, func(name string, data any) {
		if name == "done" {
			done = data.(Done)
		}
	})
	if done.OK || !strings.Contains(done.Error, "runs already") {
		t.Fatalf("done = %+v", done)
	}
}

// mu marks the installer busy, the way a running install does.
func (m *machine) mu() {
	m.inst.mu.Lock()
	m.inst.busy = true
	m.inst.mu.Unlock()
}

// clone must refuse a source that is shorter than the size that the kernel
// reported. A partition copy that stopped early would leave a filesystem that
// nothing can mount.
func TestCloneRefusesAShortSource(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "from")
	to := filepath.Join(dir, "to")
	allowOrdinaryFiles(t)
	write(t, from, []byte("only a few bytes"))
	// The target is there already: clone never makes it (see openTarget).
	write(t, to, nil)

	err := clone(context.Background(), from, to, 1<<20, nil)
	if err == nil {
		t.Fatal("clone() gave no error")
	}
	if !strings.Contains(err.Error(), "ended after") {
		t.Errorf("clone() = %v", err)
	}
}

func TestPartitionName(t *testing.T) {
	tests := []struct {
		disk string
		n    int
		want string
	}{
		{"sda", 1, "sda1"},
		{"sdb", 3, "sdb3"},
		{"nvme0n1", 2, "nvme0n1p2"},
		{"mmcblk0", 1, "mmcblk0p1"},
	}
	for _, tt := range tests {
		t.Run(tt.disk, func(t *testing.T) {
			if got := partitionName(tt.disk, tt.n); got != tt.want {
				t.Errorf("partitionName(%q, %d) = %q, want %q", tt.disk, tt.n, got, tt.want)
			}
		})
	}
}

// same compares a file with the bytes that it must hold.
func same(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s holds %d bytes, want %d", path, len(got), len(want))
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s does not hold the same bytes as the source", path)
	}
}

// A partition node that the kernel has not made yet must stop the install. With
// O_CREATE the copy made an ordinary file in memory, every step after it worked, and
// the UI told the person to take the stick out of a machine that cannot start.
func TestInstallStopsWhenAPartitionNodeIsMissing(t *testing.T) {
	m := newMachine(t)
	m.run.after = nil // the kernel makes no nodes

	done := m.install(t)
	if done.OK {
		t.Fatal("the install reported success on a disk with no partition nodes")
	}
	if !strings.Contains(done.Error, "did not make") {
		t.Errorf("the error is %q", done.Error)
	}
	if done.Instruction != FailedInstruction {
		t.Errorf("the instruction is %q, and the person must learn that the disk cannot start", done.Instruction)
	}
	if _, err := os.Stat(filepath.Join(m.dev, "sdb1")); err == nil {
		t.Error("the install made a file where a partition node must be")
	}
}

// A disk that carries a mounted filesystem is a disk in use. It is the second lock
// on the door, beside the test against the disk that "/" comes from.
func TestCheckRefusesADiskWithAMountedPartition(t *testing.T) {
	m := newMachine(t)
	write(t, filepath.Join(m.proc, "mounts"), []byte(
		"proc /proc proc rw 0 0\n"+
			filepath.Join(m.dev, "sda2")+" / ext4 rw,noatime 0 0\n"+
			filepath.Join(m.dev, "sdb1")+" /mnt/data ext4 rw 0 0\n"))

	target := filepath.ToSlash(filepath.Join(m.dev, "sdb"))
	_, _, err := m.inst.Check(target, target)
	if err == nil {
		t.Fatal("Check() took a disk that holds a mounted filesystem")
	}
	if !strings.Contains(err.Error(), "/mnt/data") {
		t.Errorf("the refusal is %q and must name the mount point", err)
	}
}

// The boot attribute goes on after the partitions exist. sgdisk works through its
// options in the order that it reads them, so an attribute in front of the -n
// options named a partition that was not there yet.
func TestTheBootAttributeComesAfterTheTable(t *testing.T) {
	m := newMachine(t)
	if done := m.install(t); !done.OK {
		t.Fatalf("the install failed: %s", done.Error)
	}

	table, attributes := -1, -1
	for i, call := range m.run.calls {
		switch {
		case strings.HasPrefix(call, "sgdisk -n "):
			table = i
		case strings.Contains(call, "--attributes=1:set:2"):
			attributes = i
		}
	}
	if table < 0 || attributes < 0 {
		t.Fatalf("the calls are %s", m.run.joined())
	}
	if attributes < table {
		t.Errorf("the boot attribute was set before the partitions were made: %s", m.run.joined())
	}
}

// install runs one install onto sdb and gives the last event.
func (m *machine) install(t *testing.T) Done {
	t.Helper()
	target := filepath.ToSlash(filepath.Join(m.dev, "sdb"))
	var done Done
	m.inst.Run(context.Background(), target, target, func(name string, data any) {
		if name == "done" {
			done = data.(Done)
		}
	})
	return done
}

// The REAL rule of openTarget, with no test double: on Linux the target of a write
// must be a block device node.
//
// Two faults hide here. An ordinary file passes every step and the install then
// reports success on a disk that it did not change. And a partition node that the
// kernel has not made yet must be an error: with O_CREATE the copy made a file on
// devtmpfs, which is memory, and the person was told to take the stick out of a
// machine that cannot start.
func TestTheRealTargetCheck(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the block device rule is a Linux rule: another system makes no device nodes")
	}
	dir := t.TempDir()

	file := filepath.Join(dir, "sdb1")
	write(t, file, []byte("this is not a disk"))
	f, err := openTarget(file)
	if err == nil {
		f.Close()
		t.Fatal("openTarget took an ordinary file")
	}
	if !strings.Contains(err.Error(), "is not a block device") {
		t.Errorf("the refusal is %q and must name the reason", err)
	}

	missing := filepath.Join(dir, "sdb2")
	if f, err := openTarget(missing); err == nil {
		f.Close()
		t.Fatal("openTarget took a path with nothing at it")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("openTarget made a file where a partition node must be")
	}

	// A CHARACTER device is not a block device. Go sets os.ModeDevice for both, so
	// a rule that tests only that bit writes the whole system partition into
	// /dev/null and reports success.
	for _, path := range []string{"/dev/null", "/dev/zero"} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		f, err := openTarget(path)
		if err == nil {
			f.Close()
			t.Errorf("openTarget took %s, which is a character device", path)
			continue
		}
		if !strings.Contains(err.Error(), "is not a block device") {
			t.Errorf("the refusal of %s is %q and must name the reason", path, err)
		}
	}
}

// The rule must be the real rule when the package starts. A test replaces the
// variable and puts it back, and a test that forgot to put it back would leave
// every later test with no rule at all. This test fails in that case.
func TestTheBlockDeviceRuleIsTheRealOneByDefault(t *testing.T) {
	if reflect.ValueOf(isBlockDevice).Pointer() != reflect.ValueOf(realIsBlockDevice).Pointer() {
		t.Fatal("isBlockDevice is not realIsBlockDevice; a test left its own rule in place")
	}
}
