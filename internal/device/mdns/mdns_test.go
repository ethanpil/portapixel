package mdns

import (
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
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
