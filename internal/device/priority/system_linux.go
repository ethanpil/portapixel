//go:build linux

package priority

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// ioprioWhoProcess is IOPRIO_WHO_PROCESS, and ioprioClassShift is
// IOPRIO_CLASS_SHIFT (include/uapi/linux/ioprio.h). For this "who", the kernel
// reads the ID as one thread.
const (
	ioprioWhoProcess = 1
	ioprioClassShift = 13
)

// Host gives the kernel of this machine.
func Host() System { return host{} }

type host struct{}

func (host) ThreadIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return nil, err
	}
	tids := make([]int, 0, len(entries))
	for _, e := range entries {
		if tid, err := strconv.Atoi(e.Name()); err == nil {
			tids = append(tids, tid)
		}
	}
	return tids, nil
}

// SetNice sets the nice value of one thread. PRIO_PROCESS with a thread ID
// changes that thread only.
func (host) SetNice(tid, nice int) error {
	return unix.Setpriority(unix.PRIO_PROCESS, tid, nice)
}

// SetIOPriority calls ioprio_set. x/sys/unix has the number of the call but no
// wrapper. All three arguments are integers, so no pointer goes to the kernel.
func (host) SetIOPriority(tid, class, level int) error {
	_, _, errno := unix.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoProcess, uintptr(tid),
		uintptr(class<<ioprioClassShift|level))
	if errno != 0 {
		return errno
	}
	return nil
}

func (host) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// WriteFile writes a file of /proc or /sys. Such a file exists, and it takes
// one write: no create, no truncate of a real file.
func (host) WriteFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
