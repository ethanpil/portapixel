package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/opslog"
)

// The partition types and the labels of the image. They are the values of
// os/build-image.sh, and the boot chain and every fstab line find the partitions by
// label (plan section 5).
const (
	BootLabel  = "PPBOOT"
	RootLabel  = "PPROOT"
	MediaLabel = "PPMEDIA"

	// The GPT type codes that sgdisk takes: an EFI system partition, a Linux
	// filesystem and a Microsoft basic data partition.
	bootType  = "ef00"
	rootType  = "8300"
	mediaType = "0700"
)

// mbrCodeBytes is the size of the MBR boot code. The bytes after it are the
// partition table of the protective MBR, so a copy must stop here.
const mbrCodeBytes = 440

// partitionWait is how long the install waits for the kernel to make the partition
// nodes of the new table. partitionPoll is the rest between two looks. Five seconds
// is generous: the kernel needs milliseconds, and a disk that needs longer is a disk
// that we must not write to.
const (
	partitionWait = 5 * time.Second
	partitionPoll = 100 * time.Millisecond
)

// ErrBusy says that an install runs already. Two installs on one machine would
// write over each other.
var ErrBusy = errors.New("an install onto a disk runs already")

// Runner runs one program. The daemon gives a runner that starts the real program;
// a test gives a fake and records the calls.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Progress is one event of the stream that the admin UI reads.
type Progress struct {
	Phase   string `json:"phase"`
	Percent int    `json:"percent"`
	Message string `json:"message"`
}

// Done is the last event of the stream.
type Done struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Instruction is the text that the person must follow now. It is here and not
	// only in the UI, so that the command line says the same words.
	Instruction string `json:"instruction,omitempty"`
}

// FinalInstruction is what a person must do after a good install. The two disks
// carry the same three labels, so taking the stick out is the step that decides
// which disk the machine uses (D54).
const FinalInstruction = "Power the machine off. Take the USB stick out. Start the machine again and let it boot from the disk."

// FailedInstruction is what a person must know after an install that stopped in the
// middle. The disk holds part of a system and cannot start, and the stick still can.
const FailedInstruction = "The disk holds only part of the system and cannot start. Keep the USB stick in the machine and start the install again."

// Options are the parameters of an Installer.
type Options struct {
	// SysRoot is /sys, ProcRoot is /proc and DevRoot is /dev. A test gives
	// directories that it made.
	SysRoot  string
	ProcRoot string
	DevRoot  string
	// MediaRoot is the mounted media partition of the source.
	MediaRoot string
	// MountRoot is where the new media partition is mounted while the files are
	// copied. "" uses /run/portapixel.
	MountRoot string
	// Arch is "amd64" or "arm64". The boot loader step is different.
	Arch string
	// Run runs sgdisk, partprobe, mkfs.exfat, mount and umount.
	Run Runner
	Log *opslog.Log
}

// Installer holds one install at a time.
type Installer struct {
	opt Options

	mu   sync.Mutex
	busy bool
}

// New makes an Installer.
func New(opt Options) *Installer {
	if opt.SysRoot == "" {
		opt.SysRoot = "/sys"
	}
	if opt.ProcRoot == "" {
		opt.ProcRoot = "/proc"
	}
	if opt.DevRoot == "" {
		opt.DevRoot = "/dev"
	}
	if opt.MountRoot == "" {
		opt.MountRoot = "/run/portapixel"
	}
	return &Installer{opt: opt}
}

// Busy reports if an install runs.
func (i *Installer) Busy() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.busy
}

// Check holds every refusal. The API calls it before it starts the work, so that a
// bad request answers at once and the progress stream never opens.
//
// confirm must be the device path, character for character. The admin UI asks the
// person to type it, and the device checks it again: a request that a script made
// must pass the same test as a person.
func (i *Installer) Check(device, confirm string) (Layout, Disk, error) {
	layout, err := i.Source()
	if err != nil {
		return Layout{}, Disk{}, err
	}
	if confirm != device {
		return Layout{}, Disk{}, fmt.Errorf("type the name of the disk, %s, to confirm", device)
	}
	if device == layout.Device {
		return Layout{}, Disk{}, fmt.Errorf("%s is the disk that this system runs from", device)
	}
	// The second test, and a different one. See Installer.InUse.
	if mount, err := i.InUse(device); err != nil {
		return Layout{}, Disk{}, err
	} else if mount != "" {
		return Layout{}, Disk{}, fmt.Errorf("%s holds a filesystem that this system has mounted on %s", device, mount)
	}

	disks, err := i.Disks()
	if err != nil {
		return Layout{}, Disk{}, err
	}
	for _, d := range disks {
		if d.Device != device {
			continue
		}
		if d.TooSmall {
			return Layout{}, Disk{}, fmt.Errorf("%s holds %d MB and the system needs %d MB",
				device, d.SizeBytes>>20, layout.NeedBytes()>>20)
		}
		return layout, d, nil
	}
	return Layout{}, Disk{}, fmt.Errorf("%s is not a disk that this machine can install onto", device)
}

// phase is one step of the install with its share of the progress bar.
type phase struct {
	name string
	// share is the part of the whole job, as a percentage.
	share int
}

// The phases and their weights. The two block copies take nearly all of the time,
// so they take nearly all of the bar.
var phases = []phase{
	{"checking the disk", 2},
	{"writing the partition table", 3},
	{"copying the boot partition", 10},
	{"copying the system partition", 70},
	{"making the media partition", 3},
	{"copying the media files", 10},
	{"writing the boot loader", 2},
}

// Run does the install. It sends a Progress event for each step and one Done event
// at the end. It never sends an event after Done.
//
// The caller runs it in a goroutine of its own and turns the events into the SSE
// stream. Nothing here returns until the disk is written.
func (i *Installer) Run(ctx context.Context, device, confirm string, send func(name string, data any)) {
	i.mu.Lock()
	if i.busy {
		i.mu.Unlock()
		send("done", Done{Error: ErrBusy.Error()})
		return
	}
	i.busy = true
	i.mu.Unlock()
	defer func() {
		i.mu.Lock()
		i.busy = false
		i.mu.Unlock()
	}()

	written := false
	err := i.run(ctx, device, confirm, send, func() { written = true })
	if err != nil {
		i.log("install.fail", device+": "+err.Error())
		out := Done{Error: err.Error()}
		if written {
			// The disk holds part of a system. A person who takes the stick out now
			// has a machine that does not start, and nothing else would say so.
			out.Instruction = FailedInstruction
		}
		send("done", out)
		return
	}
	i.log("install.done", device+" holds a copy of this system")
	send("done", Done{OK: true, Instruction: FinalInstruction})
}

// run is the work. Each step gives its own progress band to the reporter, so one
// step cannot move the bar backwards.
func (i *Installer) run(ctx context.Context, device, confirm string, send func(string, any), wrote func()) error {
	at := 0
	// step reports the start of a phase and gives a function that reports the
	// progress inside it.
	step := func(message string) func(done, total int64) {
		p := phases[at]
		base := 0
		for _, earlier := range phases[:at] {
			base += earlier.share
		}
		at++
		send("progress", Progress{Phase: p.name, Percent: base, Message: message})
		return func(done, total int64) {
			if total <= 0 {
				return
			}
			percent := base + int(int64(p.share)*done/total)
			send("progress", Progress{Phase: p.name, Percent: percent, Message: message})
		}
	}

	step(fmt.Sprintf("checking that %s is not the disk we boot from", device))
	layout, disk, err := i.Check(device, confirm)
	if err != nil {
		return err
	}
	i.log("install.start", fmt.Sprintf("%s (%s, %d MB) from %s", device, disk.Model, disk.SizeBytes>>20, layout.Device))

	step("the same labels and types as the stick")
	// The last look before the first write. Minutes can pass between the POST and
	// this line: the person can plug a disk in or out, and a service can mount
	// something. Every refusal is read again here, against the world as it is now.
	if _, _, err := i.Check(device, confirm); err != nil {
		return err
	}
	wrote()
	if err := i.writeTable(ctx, device, layout); err != nil {
		return err
	}

	report := step(fmt.Sprintf("%d MB", layout.BootBytes>>20))
	if err := clone(ctx, i.PartitionPath(layout.Device, 1), i.PartitionPath(device, 1), layout.BootBytes, report); err != nil {
		return fmt.Errorf("copy the boot partition: %w", err)
	}

	report = step(fmt.Sprintf("%d MB", layout.RootBytes>>20))
	if err := clone(ctx, i.PartitionPath(layout.Device, 2), i.PartitionPath(device, 2), layout.RootBytes, report); err != nil {
		return fmt.Errorf("copy the system partition: %w", err)
	}

	step("exFAT over the rest of the disk")
	media := i.PartitionPath(device, 3)
	if _, err := i.opt.Run.Run(ctx, "mkfs.exfat", "-L", MediaLabel, media); err != nil {
		return fmt.Errorf("make the media partition: %w", err)
	}

	report = step("playlists, pictures and videos")
	if err := i.copyMedia(ctx, media, report); err != nil {
		return err
	}

	step("")
	return i.writeBootCode(ctx, device, layout)
}

// writeTable makes the new GPT. p1 and p2 take the sizes of the source and p3 takes
// the rest of the disk.
//
// The labels and the type codes are the ones of the image. A partition of another
// type or another name is a partition that the boot chain does not find.
func (i *Installer) writeTable(ctx context.Context, device string, layout Layout) error {
	if _, err := i.opt.Run.Run(ctx, "sgdisk", "--zap-all", device); err != nil {
		return fmt.Errorf("clear the partition table of %s: %w", device, err)
	}
	args := []string{
		"-n", fmt.Sprintf("1:0:+%dK", layout.BootBytes>>10), "-t", "1:" + bootType, "-c", "1:" + BootLabel,
		"-n", fmt.Sprintf("2:0:+%dK", layout.RootBytes>>10), "-t", "2:" + rootType, "-c", "2:" + RootLabel,
		"-n", "3:0:0", "-t", "3:" + mediaType, "-c", "3:" + MediaLabel,
		device,
	}
	if _, err := i.opt.Run.Run(ctx, "sgdisk", args...); err != nil {
		return fmt.Errorf("write the partition table of %s: %w", device, err)
	}
	if i.opt.Arch == "amd64" {
		// The legacy BIOS bootable attribute, GPT bit 2. The MBR boot code of
		// syslinux boots only a partition that carries it.
		//
		// A call of its own, and after the table. sgdisk works through its options in
		// the order that it reads them, so an attribute in front of the -n options
		// named a partition that did not exist yet. os/build-image.sh does the same
		// two steps.
		if _, err := i.opt.Run.Run(ctx, "sgdisk", "--attributes=1:set:2", device); err != nil {
			return fmt.Errorf("set the boot attribute of %s: %w", device, err)
		}
	}

	// The kernel must read the new table before the partition devices exist. partx
	// is its own package and partprobe is in parted; the image has both, and one of
	// them is enough.
	if _, err := i.opt.Run.Run(ctx, "partx", "-u", device); err != nil {
		if _, second := i.opt.Run.Run(ctx, "partprobe", device); second != nil {
			return fmt.Errorf("the kernel did not read the new partition table of %s: %w", device, second)
		}
	}
	return i.waitForPartitions(ctx, device)
}

// waitForPartitions waits until the kernel made the three partition nodes.
//
// partx and partprobe ask the kernel to read the table; they do not wait for the
// nodes. A write before the nodes exist made an ordinary file in memory, and the
// install then reported success on a disk that held an empty table (see openTarget).
func (i *Installer) waitForPartitions(ctx context.Context, device string) error {
	deadline := time.Now().Add(partitionWait)
	for {
		missing := ""
		for n := 1; n <= 3; n++ {
			path := i.PartitionPath(device, n)
			if _, err := os.Stat(path); err != nil {
				missing = path
				break
			}
		}
		if missing == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the kernel did not make %s in %s", missing, partitionWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(partitionPoll):
		}
	}
}

// copyMedia mounts the new media partition and walks the files across.
//
// rsync is not in the image, and a walk with an fsync for each file is what the
// write discipline asks for anyway (D41). The reserved directories go too: _fleet
// holds the objects of a paired device, and the next boot would download them all
// again.
func (i *Installer) copyMedia(ctx context.Context, partition string, report func(done, total int64)) error {
	if i.opt.MediaRoot == "" {
		return nil
	}
	mount := filepath.Join(i.opt.MountRoot, "install-media")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		return fmt.Errorf("make the mount point %s: %w", mount, err)
	}
	if _, err := i.opt.Run.Run(ctx, "mount", partition, mount); err != nil {
		return fmt.Errorf("mount the new media partition: %w", err)
	}
	defer func() {
		if _, err := i.opt.Run.Run(ctx, "umount", mount); err != nil {
			i.log("install.umount.fail", err.Error())
		}
	}()

	if err := copyTree(ctx, i.opt.MediaRoot, mount, report); err != nil {
		return fmt.Errorf("copy the media files: %w", err)
	}
	return nil
}

// writeBootCode puts the boot bits that live outside every partition on the target.
//
// On x86 that is the GPT-aware MBR boot code of syslinux: the first 440 bytes of
// the disk. The bytes after it are the protective partition table, which sgdisk
// wrote, so the copy stops at 440. The partition boot record of PPBOOT came across
// with the block copy of p1.
//
// A Raspberry Pi needs nothing: its firmware reads the FAT partition, and that
// partition is already a copy.
func (i *Installer) writeBootCode(ctx context.Context, device string, layout Layout) error {
	if i.opt.Arch != "amd64" {
		i.log("install.bootloader.skip", "this architecture boots from the FAT partition, so there is no boot code outside it")
		return nil
	}
	if err := copyHead(layout.Device, device, mbrCodeBytes); err != nil {
		return fmt.Errorf("write the master boot record of %s: %w", device, err)
	}
	i.log("install.bootloader", fmt.Sprintf("%d bytes of boot code from %s to %s", mbrCodeBytes, layout.Device, device))
	return nil
}

func (i *Installer) log(event, details string) {
	if i.opt.Log != nil {
		i.opt.Log.Log(event, details)
	}
}
