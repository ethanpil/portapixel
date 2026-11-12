package priority

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// fakeSystem is a kernel in memory.
type fakeSystem struct {
	// threads gives the thread list of each call of ThreadIDs. The last list
	// repeats.
	threads [][]int
	calls   int
	nice    map[int]int
	io      map[int][2]int
	files   map[string]string
	written map[string]string
	failAll error
}

func newFake(cgroup string) *fakeSystem {
	return &fakeSystem{
		threads: [][]int{{100, 101, 102}},
		nice:    map[int]int{},
		io:      map[int][2]int{},
		files: map[string]string{
			"/proc/self/cgroup":                                    cgroup,
			"/proc/self/oom_score_adj":                             "0",
			"/sys/fs/cgroup/openrc.portapixeld/cgroup.controllers": "cpuset cpu io memory pids",
			"/sys/fs/cgroup/openrc.portapixeld/cpu.weight":         "100",
		},
		written: map[string]string{},
	}
}

func (f *fakeSystem) ThreadIDs() ([]int, error) {
	i := min(f.calls, len(f.threads)-1)
	f.calls++
	return f.threads[i], nil
}

func (f *fakeSystem) SetNice(tid, nice int) error {
	if f.failAll != nil {
		return f.failAll
	}
	f.nice[tid] = nice
	return nil
}

func (f *fakeSystem) SetIOPriority(tid, class, level int) error {
	if f.failAll != nil {
		return f.failAll
	}
	f.io[tid] = [2]int{class, level}
	return nil
}

func (f *fakeSystem) ReadFile(path string) ([]byte, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	return []byte(data), nil
}

func (f *fakeSystem) WriteFile(path string, data []byte) error {
	if f.failAll != nil {
		return f.failAll
	}
	if _, ok := f.files[path]; !ok {
		return &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	f.written[path] = string(data)
	return nil
}

type fakeLog struct{ lines []string }

func (l *fakeLog) Log(event, details string) { l.lines = append(l.lines, event+" "+details) }

const serviceCgroupFile = "0::/openrc.portapixeld\n"

func TestHighAppliesAllFour(t *testing.T) {
	f := newFake(serviceCgroupFile)
	res := Apply("high", f, &fakeLog{})
	if res.CgroupError != "" {
		t.Fatalf("CgroupError = %q", res.CgroupError)
	}
	for _, tid := range []int{100, 101, 102} {
		if f.nice[tid] != -5 {
			t.Errorf("nice of thread %d = %d, want -5", tid, f.nice[tid])
		}
		if f.io[tid] != [2]int{2, 0} {
			t.Errorf("io of thread %d = %v, want best-effort 0", tid, f.io[tid])
		}
	}
	if got := f.written["/proc/self/oom_score_adj"]; got != "-500" {
		t.Errorf("oom_score_adj = %q, want -500", got)
	}
	if got := f.written["/sys/fs/cgroup/openrc.portapixeld/cpu.weight"]; got != "1000" {
		t.Errorf("cpu.weight = %q, want 1000", got)
	}
}

// A thread that the runtime starts while Apply works gets the values too.
func TestHighReachesALateThread(t *testing.T) {
	f := newFake(serviceCgroupFile)
	f.threads = [][]int{{100, 101}, {100, 101, 103}}
	Apply("high", f, nil)
	if f.nice[103] != -5 || f.io[103] != [2]int{2, 0} {
		t.Errorf("the late thread has nice %d, io %v", f.nice[103], f.io[103])
	}
	if f.calls != 3 {
		t.Errorf("%d reads of the thread list, want 3 (the last one finds nothing new)", f.calls)
	}
}

func TestNormalAppliesNothing(t *testing.T) {
	f := newFake(serviceCgroupFile)
	res := Apply("normal", f, nil)
	if res.CgroupError != "" || len(f.nice) > 0 || len(f.io) > 0 || len(f.written) > 0 || f.calls > 0 {
		t.Errorf("normal changed something: %+v, nice %v, io %v, written %v", res, f.nice, f.io, f.written)
	}
}

func TestNoSystemAppliesNothing(t *testing.T) {
	if res := Apply("high", nil, nil); res.CgroupError != "" {
		t.Errorf("CgroupError = %q away from Linux", res.CgroupError)
	}
}

// With no cgroup of its own, the daemon applies the other three and warns.
func TestCgroupUnavailable(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeSystem)
		want  string
	}{
		{"the root cgroup", func(f *fakeSystem) { f.files["/proc/self/cgroup"] = "0::/\n" }, "root cgroup"},
		{"cgroup v1 only", func(f *fakeSystem) { f.files["/proc/self/cgroup"] = "1:name=openrc:/portapixeld\n" }, "no cgroup v2"},
		{"no cgroup file", func(f *fakeSystem) { delete(f.files, "/proc/self/cgroup") }, "no cgroups"},
		{"no cpu controller", func(f *fakeSystem) {
			f.files["/sys/fs/cgroup/openrc.portapixeld/cgroup.controllers"] = "memory pids"
		}, "cpu controller is off"},
		{"not mounted at /sys/fs/cgroup", func(f *fakeSystem) {
			delete(f.files, "/sys/fs/cgroup/openrc.portapixeld/cgroup.controllers")
		}, "not at /sys/fs/cgroup"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(serviceCgroupFile)
			tt.setup(f)
			res := Apply("high", f, &fakeLog{})
			if !strings.Contains(res.CgroupError, tt.want) {
				t.Errorf("CgroupError = %q, want it to hold %q", res.CgroupError, tt.want)
			}
			if _, ok := f.written["/sys/fs/cgroup/openrc.portapixeld/cpu.weight"]; ok {
				t.Error("cpu.weight was written")
			}
			if f.nice[100] != -5 || f.written["/proc/self/oom_score_adj"] != "-500" {
				t.Errorf("the other steps did not apply: nice %v, written %v", f.nice, f.written)
			}
		})
	}
}

// A daemon that is not root fails each call. That goes to the log, not to the
// status, and the start goes on.
func TestFaultsGoToTheLog(t *testing.T) {
	f := newFake(serviceCgroupFile)
	f.failAll = errors.New("operation not permitted")
	log := &fakeLog{}
	Apply("high", f, log)
	text := strings.Join(log.lines, "\n")
	for _, event := range []string{"priority.nice.fail", "priority.io.fail", "priority.oom.fail", "priority.cgroup.fail"} {
		if !strings.Contains(text, event+" ") {
			t.Errorf("the log has no %s:\n%s", event, text)
		}
	}
}

func TestUnifiedPath(t *testing.T) {
	tests := []struct {
		data string
		want string
		ok   bool
	}{
		{"0::/openrc.portapixeld\n", "/openrc.portapixeld", true},
		{"0::/\n", "/", true},
		// Hybrid: v1 lines and the v2 line.
		{"12:pids:/openrc.portapixeld\n1:name=openrc:/portapixeld\n0::/openrc.portapixeld\n", "/openrc.portapixeld", true},
		{"1:name=openrc:/portapixeld\n", "", false},
		{"", "", false},
		{"0::/../etc\n", "", false},
	}
	for _, tt := range tests {
		got, ok := UnifiedPath([]byte(tt.data))
		if got != tt.want || ok != tt.ok {
			t.Errorf("UnifiedPath(%q) = %q, %v; want %q, %v", tt.data, got, ok, tt.want, tt.ok)
		}
	}
}

// Check the arithmetic of the values against the kernel words.
func TestValues(t *testing.T) {
	if got := fmt.Sprint(IOClass<<13 | IOLevel); got != "16384" {
		t.Errorf("ioprio = %s, want 16384 (IOPRIO_PRIO_VALUE(IOPRIO_CLASS_BE, 0))", got)
	}
}
