package priority

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/ethanpil/portapixel/internal/config"
)

// The values of "high".
const (
	Nice        = -5
	IOClass     = 2 // IOPRIO_CLASS_BE, best-effort
	IOLevel     = 0 // the highest level of the class
	OOMScoreAdj = -500
	CPUWeight   = 1000
)

// The paths that Apply reads and writes.
const (
	selfCgroup = "/proc/self/cgroup"
	selfOOM    = "/proc/self/oom_score_adj"
	cgroupRoot = "/sys/fs/cgroup"
)

// threadPasses is how many times Apply reads the thread list. The Go runtime can
// start a thread while Apply works, and a thread made from a thread that was not
// done yet does not inherit the value. A pass that finds no new thread ends the
// loop.
const threadPasses = 3

// System is the kernel. The tests give a fake.
type System interface {
	// ThreadIDs gives the threads of this process.
	ThreadIDs() ([]int, error)
	SetNice(tid, nice int) error
	SetIOPriority(tid, class, level int) error
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
}

// Logger is the ops log. *opslog.Log is one.
type Logger interface {
	Log(event, details string)
}

// Result is what the status needs from Apply.
type Result struct {
	// CgroupError is the sentence of the warning priority-cgroup-unavailable,
	// or "".
	CgroupError string
}

// Apply sets the priority of this process for the setting. sys is nil away from
// Linux, and Apply then does nothing.
//
// A fault of one step does not stop the other steps. Each fault goes into the
// ops log. Only the cgroup fault goes into the status: the other steps fail only
// for a daemon that does not run as root.
func Apply(setting string, sys System, log Logger) Result {
	logf := func(event, format string, args ...any) {
		if log != nil {
			log.Log(event, fmt.Sprintf(format, args...))
		}
	}
	if setting != config.PriorityHigh {
		logf("priority.normal", "setting=%s: no change", setting)
		return Result{}
	}
	if sys == nil {
		logf("priority.normal", "no priority calls away from Linux")
		return Result{}
	}

	// 1 and 2: each thread.
	seen := map[int]bool{}
	var niceErr, ioErr error
	for range threadPasses {
		tids, err := sys.ThreadIDs()
		if err != nil {
			niceErr, ioErr = err, err
			break
		}
		fresh := 0
		for _, tid := range tids {
			if seen[tid] {
				continue
			}
			seen[tid] = true
			fresh++
			if err := sys.SetNice(tid, Nice); err != nil && niceErr == nil {
				niceErr = err
			}
			if err := sys.SetIOPriority(tid, IOClass, IOLevel); err != nil && ioErr == nil {
				ioErr = err
			}
		}
		if fresh == 0 {
			break
		}
	}
	if niceErr != nil {
		logf("priority.nice.fail", "%v", niceErr)
	}
	if ioErr != nil {
		logf("priority.io.fail", "%v", ioErr)
	}

	// 3: the OOM killer.
	oomErr := sys.WriteFile(selfOOM, []byte(fmt.Sprint(OOMScoreAdj)))
	if oomErr != nil {
		logf("priority.oom.fail", "%v", oomErr)
	}

	// 4: the cgroup.
	var res Result
	dir, err := serviceCgroup(sys)
	if err == nil {
		err = sys.WriteFile(dir+"/cpu.weight", []byte(fmt.Sprint(CPUWeight)))
	}
	if err != nil {
		res.CgroupError = "The player priority is high, but the CPU weight is not set: " + err.Error() +
			". The other parts of the priority apply."
		logf("priority.cgroup.fail", "%v", err)
	}

	logf("priority.high", "threads=%d nice=%s io=%s oom=%s cpu.weight=%s",
		len(seen), okText(niceErr), okText(ioErr), okText(oomErr), okText(err))
	return res
}

// serviceCgroup gives the directory of the cgroup v2 of this process. It refuses
// the root cgroup, which belongs to the whole machine, and a cgroup where the cpu
// controller is off.
func serviceCgroup(sys System) (string, error) {
	data, err := sys.ReadFile(selfCgroup)
	if err != nil {
		return "", fmt.Errorf("no cgroups: %v", err)
	}
	path, ok := UnifiedPath(data)
	switch {
	case !ok:
		return "", fmt.Errorf("no cgroup v2 in %s. Set rc_cgroup_mode=\"unified\" in /etc/rc.conf", selfCgroup)
	case path == "/":
		return "", fmt.Errorf("the daemon is in the root cgroup. Start the cgroups service in the sysinit runlevel")
	}
	dir := cgroupRoot + path
	controllers, err := sys.ReadFile(dir + "/cgroup.controllers")
	if err != nil {
		return "", fmt.Errorf("cgroup v2 is not at %s: %v", cgroupRoot, err)
	}
	if !slices.Contains(strings.Fields(string(controllers)), "cpu") {
		return "", fmt.Errorf("the cpu controller is off in %s", dir)
	}
	return dir, nil
}

// UnifiedPath gives the cgroup v2 path of a /proc/<pid>/cgroup file: the line
// "0::<path>". A system with cgroup v1 only has no such line.
func UnifiedPath(data []byte) (string, bool) {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if rest, ok := bytes.CutPrefix(line, []byte("0::")); ok {
			path := strings.TrimSpace(string(rest))
			if strings.HasPrefix(path, "/") && !strings.Contains(path, "..") {
				return path, true
			}
		}
	}
	return "", false
}

func okText(err error) string {
	if err != nil {
		return "fail"
	}
	return "ok"
}
