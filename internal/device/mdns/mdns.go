package mdns

import (
	"errors"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	hashicorp "github.com/hashicorp/mdns"

	"github.com/ethanpil/portapixel/internal/opslog"
)

// Service is the service type that the device announces. A browser is what a
// person opens, so the admin UI is the service (D20).
const Service = "_http._tcp"

// tick is how often the loop looks at the name and the addresses.
const tick = 30 * time.Second

// probeTimeout is how long the name probe waits for an answer. RFC 6762 asks for
// three probes 250 ms apart; one query with a two second window finds a device that
// answers at all, and the loop looks again at every tick.
const probeTimeout = 2 * time.Second

// Publisher starts one announcement and gives the object that ends it. A nil
// Publisher in Options uses the real one.
type Publisher func(host string, port int, ips []net.IP) (io.Closer, error)

// Prober reports if another device on the network already answers for host. A nil
// Prober in Options uses the real one.
type Prober func(host string, timeout time.Duration) bool

// Options are the parameters of an Announcer.
type Options struct {
	// Name gives the mDNS name, for example "lobby.local". netcfg.MDNSName makes
	// it. It is a function, because the person can rename the device.
	Name func() string
	// IPs gives the addresses of the device as text. health.LocalIPs makes the
	// list.
	IPs func() []string
	// Port is the port of the admin UI.
	Port int
	// Publish starts the announcement. A test gives a fake.
	Publish Publisher
	// Probe looks for another device that answers for the name. A test gives a fake.
	Probe Prober
	// Fallback gives the factory name of this device, portapixel-<last4>.local. The
	// announcement falls back to it when another device holds the chosen name (D20).
	// An empty value means "announce nothing on a collision".
	Fallback func() string
	Log      *opslog.Log
	// Tick is the period of the loop. 0 uses 30 seconds.
	Tick time.Duration
}

// Announcer keeps one announcement current. Only Run touches the fields, so it
// needs no lock.
type Announcer struct {
	opt Options

	closer io.Closer
	// host and ips are the values that the last refresh read. announced is the name
	// that goes out now, which is the fallback name after a collision.
	host      string
	announced string
	ips       []string
	// announcedIPs are the addresses of the announcement that runs. A change of the
	// addresses needs a new announcement even when the name is the same.
	announcedIPs []string
	// failed is true while the last announcement did not start. It stops one log
	// line for every tick on a device with no network.
	failed bool

	// taken is the name that another device on the network holds, or "". The status
	// report carries it as the warning mdns-name-taken.
	mu    sync.Mutex
	taken string
}

// NameTaken gives the name that another device holds, or "". The daemon puts it in
// the status report.
func (a *Announcer) NameTaken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.taken
}

// setTaken records the collision and gives true when it is a new one, so the ops log
// gets one line and not one line at every tick.
func (a *Announcer) setTaken(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.taken == name {
		return false
	}
	a.taken = name
	return name != ""
}

// New makes an Announcer. It announces nothing until Run starts.
func New(opt Options) *Announcer {
	if opt.Tick <= 0 {
		opt.Tick = tick
	}
	if opt.Publish == nil {
		opt.Publish = publish
	}
	if opt.Probe == nil {
		opt.Probe = probe
	}
	if opt.Fallback == nil {
		opt.Fallback = func() string { return "" }
	}
	if opt.IPs == nil {
		opt.IPs = func() []string { return nil }
	}
	if opt.Name == nil {
		opt.Name = func() string { return "" }
	}
	return &Announcer{opt: opt}
}

// Run announces the device until done is closed.
//
// The daemon starts this in a goroutine and never waits for it. A device with no
// network must come up and play (plan 3.3).
func (a *Announcer) Run(done <-chan struct{}) {
	defer a.stop()
	a.refresh()

	t := time.NewTicker(a.opt.Tick)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			a.refresh()
		}
	}
}

// refresh announces again when the name or the addresses changed.
//
// While another device holds the name, this also probes at every tick. A collision
// that goes away must repair itself: a person who renames the other screen should
// not have to restart this one.
func (a *Announcer) refresh() {
	host := strings.TrimSuffix(strings.TrimSpace(a.opt.Name()), ".")
	ips := a.opt.IPs()

	if host == a.host && slices.Equal(ips, a.ips) && !a.failed && a.NameTaken() == "" {
		return
	}
	a.host, a.ips = host, ips

	addresses := parseIPs(ips)
	if host == "" || len(addresses) == 0 {
		// No name or no address. There is nothing to announce, and that is not a
		// fault: a device with no cable plays what it has.
		a.stop()
		a.announced, a.announcedIPs = "", nil
		a.failed = false
		a.setTaken("")
		return
	}

	// Ask the network first, the way RFC 6762 asks a responder to. Two screens that a
	// person gave one name would else both answer for it, and a browser would reach
	// one of the two at random. The factory name portapixel-<last4>.local is unique
	// by construction, so it is the fallback (D20).
	//
	// The factory name is never probed against itself: this device answers for it,
	// and the probe would find its own announcement of the tick before.
	//
	// The probe costs two seconds at a change of the name and nothing in the steady
	// state. It runs in a goroutine of the daemon and never holds the boot or the
	// playback (plan 3.3).
	want := host
	fallback := strings.TrimSuffix(strings.TrimSpace(a.opt.Fallback()), ".")
	if host != fallback && a.opt.Probe(host, probeTimeout) {
		if a.setTaken(host) {
			a.log("mdns.name.taken", host+" answers on this network already; this device announces "+
				nameOrNothing(fallback)+" instead")
		}
		want = fallback
	} else {
		a.setTaken("")
	}

	if want == "" {
		// A collision and no factory name to fall back to. Announce nothing: two
		// answers for one name are worse than none.
		a.stop()
		a.announced, a.announcedIPs = "", nil
		a.failed = false
		return
	}
	if want == a.announced && slices.Equal(ips, a.announcedIPs) && !a.failed {
		// Nothing that the announcement carries changed, so it stands. A stop and a
		// start at every tick would make the device come and go in a browser.
		return
	}

	a.stop()
	closer, err := a.opt.Publish(want, a.opt.Port, addresses)
	if err != nil {
		a.announced, a.announcedIPs = "", nil
		if !a.failed {
			a.failed = true
			a.log("mdns.fail", err.Error()+"; the device tries again every "+a.opt.Tick.String())
		}
		return
	}
	a.closer = closer
	a.announced = want
	a.announcedIPs = ips
	a.failed = false
	a.log("mdns.announce", want+" on port "+strconv.Itoa(a.opt.Port)+" at "+strings.Join(ips, " "))
}

// stop ends the announcement that runs.
func (a *Announcer) stop() {
	if a.closer == nil {
		return
	}
	if err := a.closer.Close(); err != nil {
		a.log("mdns.stop.fail", err.Error())
	}
	a.closer = nil
}

// publish is the real announcement. hashicorp/mdns wants a fully qualified host
// name, so the name gets the trailing full stop here and in no other place.
func publish(host string, port int, ips []net.IP) (io.Closer, error) {
	if port <= 0 {
		return nil, errors.New("mdns needs the port of the admin UI")
	}
	instance := strings.TrimSuffix(host, ".local")
	service, err := hashicorp.NewMDNSService(instance, Service, "local.", host+".", port, ips, nil)
	if err != nil {
		return nil, err
	}
	server, err := hashicorp.NewServer(&hashicorp.Config{Zone: service})
	if err != nil {
		return nil, err
	}
	return closerFunc(server.Shutdown), nil
}

// probe asks the network if another device answers for host.
//
// hashicorp/mdns has no responder-side probe, so this is a query of the service
// type. Query does not close the channel and its sends are not blocking, so the
// channel needs a buffer or the answers go nowhere.
//
// A fault of the query answers false. A device with no network must announce what it
// can and must never stop because a probe failed.
func probe(host string, timeout time.Duration) bool {
	want := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if want == "" {
		return false
	}

	entries := make(chan *hashicorp.ServiceEntry, 32)
	params := hashicorp.DefaultParams(Service)
	params.Timeout = timeout
	params.Entries = entries
	// IPv6 multicast is off on many of these boxes, and a query on a socket that
	// cannot open makes the whole call fail.
	params.DisableIPv6 = true

	found := make(chan bool, 1)
	go func() {
		taken := false
		for e := range entries {
			if e == nil {
				continue
			}
			if strings.ToLower(strings.TrimSuffix(e.Host, ".")) == want {
				taken = true
			}
		}
		found <- taken
	}()

	err := hashicorp.Query(params)
	close(entries)
	if err != nil {
		// Drain the answer of the goroutine, so it cannot leak.
		<-found
		return false
	}
	return <-found
}

// nameOrNothing gives a name for a log line, or a sentence when there is none.
func nameOrNothing(name string) string {
	if name == "" {
		return "no name at all"
	}
	return name
}

// closerFunc turns the Shutdown method of the server into an io.Closer, so that
// this package holds one small interface and not a type of another module.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// parseIPs turns the text addresses into net.IP values and drops what is not an
// address.
func parseIPs(list []string) []net.IP {
	out := make([]net.IP, 0, len(list))
	for _, text := range list {
		if ip := net.ParseIP(text); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

func (a *Announcer) log(event, details string) {
	if a.opt.Log != nil {
		a.opt.Log.Log(event, details)
	}
}
