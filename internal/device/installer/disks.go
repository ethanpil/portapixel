package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// sectorSize is the unit of /sys/block/<name>/size. The kernel always reports
// 512-byte sectors there, whatever the real sector size of the disk is.
const sectorSize = 512

// mediaMinBytes is the smallest PPMEDIA that an install makes. A device with no
// room for content is a device that shows the fallback screen.
const mediaMinBytes = 512 << 20

// gptOverheadBytes is the space that the two partition tables and the alignment
// take. 2 MB is generous: a GPT is 17 KB at each end of the disk.
const gptOverheadBytes = 2 << 20

// skipPrefixes are the block devices that can never be an install target. A loop
// device is a file, ram and zram are memory, and sr is an optical drive.
var skipPrefixes = []string{"loop", "ram", "zram", "sr", "dm-", "md"}

// Disk is one candidate target. The JSON shape is what GET /api/disks answers.
type Disk struct {
	Device    string `json:"device"`
	Model     string `json:"model"`
	SizeBytes int64  `json:"size_bytes"`
	Removable bool   `json:"removable"`
	// TooSmall is true when the source layout does not fit. The admin UI shows the
	// disk and blocks the button, so a person sees why a disk is not offered.
	TooSmall bool `json:"too_small"`
}

// Layout is what the source disk holds. The install needs the two partition sizes,
// so that the target gets the same ones.
type Layout struct {
	// Device is the source disk, for example /dev/sda.
	Device string
	// BootBytes and RootBytes are the sizes of p1 and p2.
	BootBytes int64
	RootBytes int64
}

// NeedBytes is the smallest target that this layout fits on.
func (l Layout) NeedBytes() int64 {
	return l.BootBytes + l.RootBytes + mediaMinBytes + gptOverheadBytes
}

// Disks gives the candidate targets. The boot disk is never in the list: a copy of
// a disk onto itself is a disk with nothing on it.
func (i *Installer) Disks() ([]Disk, error) {
	layout, err := i.Source()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(i.opt.SysRoot, "block"))
	if err != nil {
		return nil, fmt.Errorf("read the block devices: %w", err)
	}

	need := layout.NeedBytes()
	out := make([]Disk, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if skipName(name) {
			continue
		}
		device := filepath.ToSlash(filepath.Join(i.opt.DevRoot, name))
		if device == layout.Device {
			continue
		}
		if i.readSysValue(name, "ro") == "1" {
			continue
		}
		size := i.readSysInt(name, "size") * sectorSize
		if size <= 0 {
			continue
		}
		out = append(out, Disk{
			Device:    device,
			Model:     i.model(name),
			SizeBytes: size,
			Removable: i.readSysValue(name, "removable") == "1",
			TooSmall:  size < need,
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Device < out[b].Device })
	return out, nil
}

// skipName reports if a block device can never be a target.
func skipName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return true
	}
	for _, prefix := range skipPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// Source gives the layout of the disk that the system runs from.
//
// The root filesystem is PPROOT, so the mount source of "/" names the partition,
// and the partition names the disk. A machine that boots from a label needs no
// guess: /proc/mounts holds the device that the kernel used.
func (i *Installer) Source() (Layout, error) {
	root, err := i.rootPartition()
	if err != nil {
		return Layout{}, err
	}
	diskName, err := i.diskOf(root)
	if err != nil {
		return Layout{}, err
	}
	out := Layout{Device: filepath.ToSlash(filepath.Join(i.opt.DevRoot, diskName))}

	// The two sizes come from the partition directories of the disk.
	for n, into := range map[int]*int64{1: &out.BootBytes, 2: &out.RootBytes} {
		part := partitionName(diskName, n)
		size := i.readPartitionInt(diskName, part, "size") * sectorSize
		if size <= 0 {
			return Layout{}, fmt.Errorf("cannot read the size of partition %d of %s", n, diskName)
		}
		*into = size
	}
	return out, nil
}

// rootPartition gives the device that the kernel mounted on "/".
func (i *Installer) rootPartition() (string, error) {
	data, err := os.ReadFile(filepath.Join(i.opt.ProcRoot, "mounts"))
	if err != nil {
		return "", fmt.Errorf("read the mount list: %w", err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "/" {
			continue
		}
		source := fields[0]
		// A root that is not a block device, for example "overlay" or "tmpfs". An
		// install onto a disk has no meaning then. The test on Windows also asks
		// filepath.IsAbs, because a temporary directory there starts with a drive
		// letter and not with a slash.
		if !strings.HasPrefix(source, "/") && !filepath.IsAbs(source) {
			return "", fmt.Errorf("the root filesystem is %q and not a disk partition", source)
		}
		return source, nil
	}
	return "", fmt.Errorf("the mount list names no root filesystem")
}

// diskOf gives the name of the disk that a partition belongs to, for example
// "sda" for "/dev/sda2".
//
// The answer comes from /sys/block: a disk directory holds a directory for each of
// its partitions. A name test would have to know that nvme0n1p2 belongs to nvme0n1
// and that sda2 belongs to sda, and the kernel already knows both.
func (i *Installer) diskOf(partition string) (string, error) {
	want := filepath.Base(partition)
	entries, err := os.ReadDir(filepath.Join(i.opt.SysRoot, "block"))
	if err != nil {
		return "", fmt.Errorf("read the block devices: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == want {
			return name, nil // the root is a whole disk, which is not our layout
		}
		if _, err := os.Stat(filepath.Join(i.opt.SysRoot, "block", name, want)); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("%s is not a partition of a disk that the kernel reports", partition)
}

// partitionName gives the kernel name of a partition. A disk name that ends with a
// digit takes a "p" in front of the number: nvme0n1p2 and mmcblk0p2 against sda2.
func partitionName(disk string, n int) string {
	last := disk[len(disk)-1]
	if last >= '0' && last <= '9' {
		return fmt.Sprintf("%sp%d", disk, n)
	}
	return fmt.Sprintf("%s%d", disk, n)
}

// PartitionPath gives the device path of a partition of a disk.
func (i *Installer) PartitionPath(disk string, n int) string {
	return filepath.ToSlash(filepath.Join(i.opt.DevRoot, partitionName(filepath.Base(disk), n)))
}

// model gives a name that a person recognises, for example "Samsung SSD 860 EVO".
func (i *Installer) model(name string) string {
	model := i.readSysValue(name, filepath.Join("device", "model"))
	vendor := i.readSysValue(name, filepath.Join("device", "vendor"))
	switch {
	case model == "" && vendor == "":
		return ""
	case vendor == "":
		return model
	case model == "":
		return vendor
	}
	return vendor + " " + model
}

func (i *Installer) readSysValue(name, file string) string {
	data, err := os.ReadFile(filepath.Join(i.opt.SysRoot, "block", name, file))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (i *Installer) readSysInt(name, file string) int64 {
	n, err := strconv.ParseInt(i.readSysValue(name, file), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (i *Installer) readPartitionInt(disk, partition, file string) int64 {
	data, err := os.ReadFile(filepath.Join(i.opt.SysRoot, "block", disk, partition, file))
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
