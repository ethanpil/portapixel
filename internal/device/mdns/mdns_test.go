package mdns

import (
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakePublisher records every announcement and every stop, so a test can see
// that the old announcement ended before the new one started.
type fakePublisher struct {
	steps []string
	// fail makes the next announcement fail.
	fail bool
	open int
}

func (f *fakePublisher) publish(host string, port int, ips []net.IP) (io.Closer, error) {
	if f.fail {
		f.steps = append(f.steps, "fail "+host)
		return nil, errors.New("no network")
	}
	addresses := make([]string, 0, len(ips))
	for _, ip := range ips {
		addresses = append(addresses, ip.String())
	}
	f.steps = append(f.steps, "start "+host+":"+strconv.Itoa(port)+" "+strings.Join(addresses, ","))
	f.open++
	return closerFunc(func() error {
		f.open--
		f.steps = append(f.steps, "stop "+host)
		return nil
	}), nil
}

// freeName is the probe of a unit test: no other device holds the name. A test must
// never send a multicast query, and the real probe waits two seconds for an answer.
func freeName(string, time.Duration) bool { return false }

// The loop must announce again when the name or the addresses change, and it must
// end the old announcement first. Two announcements of one name on one network is
// the fault that a person sees as a device that comes and goes.
func TestRefresh(t *testing.T) {
	tests := []struct {
		name  string
		hosts []string
		ips   [][]string
		want  []string
	}{
		{
			name:  "the first announcement",
			hosts: []string{"lobby.local"},
			ips:   [][]string{{"192.168.1.10"}},
			want:  []string{"start lobby.local:8080 192.168.1.10"},
		},
		{
			name:  "nothing changed, so nothing happens",
			hosts: []string{"lobby.local", "lobby.local"},
			ips:   [][]string{{"192.168.1.10"}, {"192.168.1.10"}},
			want:  []string{"start lobby.local:8080 192.168.1.10"},
		},
		{
			name:  "a new name",
			hosts: []string{"lobby.local", "front-desk.local"},
			ips:   [][]string{{"192.168.1.10"}, {"192.168.1.10"}},
			want: []string{
				"start lobby.local:8080 192.168.1.10",
				"stop lobby.local",
				"start front-desk.local:8080 192.168.1.10",
			},
		},
		{
			name:  "a new address",
			hosts: []string{"lobby.local", "lobby.local"},
			ips:   [][]string{{"192.168.1.10"}, {"192.168.1.11"}},
			want: []string{
				"start lobby.local:8080 192.168.1.10",
				"stop lobby.local",
				"start lobby.local:8080 192.168.1.11",
			},
		},
		{
			name:  "no address yet, and then one arrives",
			hosts: []string{"lobby.local", "lobby.local"},
			ips:   [][]string{nil, {"192.168.1.10"}},
			want:  []string{"start lobby.local:8080 192.168.1.10"},
		},
		{
			name:  "the cable came out",
			hosts: []string{"lobby.local", "lobby.local"},
			ips:   [][]string{{"192.168.1.10"}, nil},
			want: []string{
				"start lobby.local:8080 192.168.1.10",
				"stop lobby.local",
			},
		},
		{
			name:  "a name that is not usable yet",
			hosts: []string{"", ""},
			ips:   [][]string{{"192.168.1.10"}, {"192.168.1.10"}},
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakePublisher{}
			at := 0
			a := New(Options{
				Name:    func() string { return tt.hosts[at] },
				IPs:     func() []string { return tt.ips[at] },
				Port:    8080,
				Publish: f.publish,
				Probe:   freeName,
			})
			for at = range tt.hosts {
				a.refresh()
			}
			if got := strings.Join(f.steps, " | "); got != strings.Join(tt.want, " | ") {
				t.Errorf("steps = %q, want %q", got, tt.want)
			}
		})
	}
}

// An announcement that cannot start must be tried again, and it must write one log
// line and not one for each tick.
func TestAFailedAnnouncementIsTriedAgain(t *testing.T) {
	f := &fakePublisher{fail: true}
	a := New(Options{
		Name:    func() string { return "lobby.local" },
		IPs:     func() []string { return []string{"192.168.1.10"} },
		Port:    80,
		Publish: f.publish,
		Probe:   freeName,
	})
	a.refresh()
	a.refresh()
	f.fail = false
	a.refresh()

	want := "fail lobby.local | fail lobby.local | start lobby.local:80 192.168.1.10"
	if got := strings.Join(f.steps, " | "); got != want {
		t.Errorf("steps = %q, want %q", got, want)
	}
	if f.open != 1 {
		t.Errorf("open announcements = %d, want 1", f.open)
	}
}

// Run must end the announcement when the daemon stops. A device that keeps a
// record on the network after it went away sends a person to an address that
// answers nothing.
func TestRunStopsTheAnnouncement(t *testing.T) {
	f := &fakePublisher{}
	a := New(Options{
		Name:    func() string { return "lobby.local" },
		IPs:     func() []string { return []string{"10.0.0.5"} },
		Port:    80,
		Publish: f.publish,
		Probe:   freeName,
	})
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() { a.Run(done); close(stopped) }()
	close(done)
	<-stopped

	if f.open != 0 {
		t.Errorf("open announcements = %d, want 0", f.open)
	}
	if got := strings.Join(f.steps, " | "); !strings.HasSuffix(got, "stop lobby.local") {
		t.Errorf("steps = %q, want a stop at the end", got)
	}
}

func TestParseIPs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want int
	}{
		{"two good addresses", []string{"192.168.1.10", "fe80::1"}, 2},
		{"one value that is not an address", []string{"192.168.1.10", "lobby"}, 1},
		{"nothing", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseIPs(tt.in); len(got) != tt.want {
				t.Errorf("parseIPs() gave %d addresses, want %d", len(got), tt.want)
			}
		})
	}
}

// Two screens that a person gave one name must not both answer for it. The probe
// asks the network first, and the device falls back to its factory name, which is
// unique by construction (D20).
func TestACollisionFallsBackToTheFactoryName(t *testing.T) {
	f := &fakePublisher{}
	probes := 0
	a := New(Options{
		Name:     func() string { return "lobby.local" },
		Fallback: func() string { return "portapixel-3c4d.local" },
		IPs:      func() []string { return []string{"192.168.1.10"} },
		Port:     80,
		Publish:  f.publish,
		Probe: func(host string, _ time.Duration) bool {
			probes++
			return host == "lobby.local"
		},
	})

	a.refresh()
	if probes != 1 {
		t.Errorf("the announcer probed %d times", probes)
	}
	if got := strings.Join(f.steps, " | "); got != "start portapixel-3c4d.local:80 192.168.1.10" {
		t.Errorf("steps = %q", got)
	}
	if got := a.NameTaken(); got != "lobby.local" {
		t.Errorf("NameTaken() = %q, want the name that the other device holds", got)
	}

	// While the collision holds, every tick probes again: a collision that goes away
	// must repair itself. The announcement itself must NOT be stopped and started
	// again, or the device would come and go in a browser.
	a.refresh()
	if probes != 2 {
		t.Errorf("the announcer probed %d times; a collision must be probed again", probes)
	}
	if len(f.steps) != 1 {
		t.Errorf("steps = %v; the announcement was made again", f.steps)
	}
}

// The factory name is never probed against itself: it is unique by construction, and
// a probe of it would find this device's own announcement of the tick before.
func TestTheFactoryNameIsNotProbed(t *testing.T) {
	f := &fakePublisher{}
	probes := 0
	a := New(Options{
		Name:     func() string { return "portapixel-3c4d.local" },
		Fallback: func() string { return "portapixel-3c4d.local" },
		IPs:      func() []string { return []string{"192.168.1.10"} },
		Port:     80,
		Publish:  f.publish,
		Probe:    func(string, time.Duration) bool { probes++; return true },
	})
	a.refresh()
	if probes != 0 {
		t.Errorf("the announcer probed its own factory name %d times", probes)
	}
	if got := strings.Join(f.steps, " | "); got != "start portapixel-3c4d.local:80 192.168.1.10" {
		t.Errorf("steps = %q", got)
	}
	if a.NameTaken() != "" {
		t.Errorf("NameTaken() = %q", a.NameTaken())
	}
}

// A name that is free again must clear the warning and announce the chosen name.
func TestACollisionThatGoesAway(t *testing.T) {
	f := &fakePublisher{}
	taken := true
	a := New(Options{
		Name:     func() string { return "lobby.local" },
		Fallback: func() string { return "portapixel-3c4d.local" },
		IPs:      func() []string { return []string{"192.168.1.10"} },
		Port:     80,
		Publish:  f.publish,
		Probe:    func(string, time.Duration) bool { return taken },
	})
	a.refresh()
	if a.NameTaken() == "" {
		t.Fatal("the collision was not recorded")
	}

	// The other device goes away. The announcer holds the fallback name now, so the
	// next refresh sees a name that changed and probes again.
	taken = false
	a.refresh()
	if a.NameTaken() != "" {
		t.Errorf("NameTaken() = %q after the other device went away", a.NameTaken())
	}
	if got := f.steps[len(f.steps)-1]; got != "start lobby.local:80 192.168.1.10" {
		t.Errorf("the last step is %q", got)
	}
}
