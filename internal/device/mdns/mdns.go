package mdns

import (
	"errors"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	hashicorp "github.com/hashicorp/mdns"

	"github.com/ethanpil/portapixel/internal/opslog"
)

// Service is the service type that the device announces. A browser is what a
// person opens, so the admin UI is the service (D20).
const Service = "_http._tcp"

// tick is how often the loop looks at the name and the addresses.
const tick = 30 * time.Second

// Publisher starts one announcement and gives the object that ends it. A nil
// Publisher in Options uses the real one.
type Publisher func(host string, port int, ips []net.IP) (io.Closer, error)

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
	Log     *opslog.Log
	// Tick is the period of the loop. 0 uses 30 seconds.
	Tick time.Duration
}

// Announcer keeps one announcement current. Only Run touches the fields, so it
// needs no lock.
type Announcer struct {
	opt Options

	closer io.Closer
	host   string
	ips    []string
	// failed is true while the last announcement did not start. It stops one log
	// line for every tick on a device with no network.
	failed bool
}

// New makes an Announcer. It announces nothing until Run starts.
func New(opt Options) *Announcer {
	if opt.Tick <= 0 {
		opt.Tick = tick
	}
	if opt.Publish == nil {
		opt.Publish = publish
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
func (a *Announcer) refresh() {
	host := strings.TrimSuffix(strings.TrimSpace(a.opt.Name()), ".")
	ips := a.opt.IPs()

	if host == a.host && slices.Equal(ips, a.ips) && !a.failed {
		return
	}
	a.stop()
	a.host, a.ips = host, ips

	addresses := parseIPs(ips)
	if host == "" || len(addresses) == 0 {
		// No name or no address. There is nothing to announce, and that is not a
		// fault: a device with no cable plays what it has.
		a.failed = false
		return
	}

	closer, err := a.opt.Publish(host, a.opt.Port, addresses)
	if err != nil {
		if !a.failed {
			a.failed = true
			a.log("mdns.fail", err.Error()+"; the device tries again every "+a.opt.Tick.String())
		}
		return
	}
	a.closer = closer
	a.failed = false
	a.log("mdns.announce", host+" on port "+strconv.Itoa(a.opt.Port)+" at "+strings.Join(ips, " "))
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
