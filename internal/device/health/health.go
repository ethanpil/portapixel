package health

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/device/netcfg"
	"github.com/ethanpil/portapixel/internal/manifest"
	"github.com/ethanpil/portapixel/internal/version"
)

// DefaultWebPassword and DefaultRootPassword are the passwords of the image. The
// report gives a warning until the person changes both of them (D22, D23).
const (
	DefaultWebPassword = "portapixel"
	RootHashFile       = ".root-default-hash"
)

// lowRAMBytes is the memory under which a device is low tier: transitions
// degrade and zram swap is on (plan section 4).
const lowRAMBytes = 1 << 30 // 1 GiB

// Sources are the file roots that the report reads. A test gives directories
// that it made; the daemon gives the true roots.
type Sources struct {
	ProcRoot  string // /proc
	SysRoot   string // /sys
	EtcRoot   string // /etc
	ShareRoot string // /usr/share/portapixel
	MediaRoot string
	StateDir  string
}

// Inputs are the facts that the other packages hold. The report never asks them
// for anything: the daemon collects them and hands them over, so that a slow
// package can never make /api/status slow.
type Inputs struct {
	Config           config.Config
	ConfigFromShadow bool
	ConfigWarning    string
	// ConfigWarningCode names the fault of ConfigWarning:
	// manifest.WarnConfigRepaired for a value that Load put back to its default,
	// manifest.WarnConfigBadEdit for a hand edit that the daemon refused. An
	// empty value with a warning text gets the repaired code.
	ConfigWarningCode string
	DeviceID          string
	// HardwareChanged is the repair flag of the state file (D21).
	HardwareChanged bool
	// Codecs is the report of the player, or nil before the first heartbeat.
	Codecs manifest.CodecReport

	BrowserState     string
	NavigationRung   string
	DisplayConnected bool
	ScreenOn         bool
	NowPlaying       *manifest.NowPlaying

	Paired         bool
	ServerURL      string
	PairingCode    string
	LastSync       time.Time
	LastSyncResult string
	SyncError      string
	// ServerInsecure is true when the server address is http:// on a network that
	// is not local. The device token then goes over the internet in clear text.
	ServerInsecure bool

	ClockSynced bool
	// Problems are the playlist faults that the library found. Each one is a
	// sentence for a person.
	Problems []string
	Update   manifest.UpdateState

	// MDNSNameTaken is the name that another device on the network already answers
	// for, or "". internal/device/mdns finds it with a probe before it announces
	// (D20).
	MDNSNameTaken string

	// RebootLoops is how many times the watchdog ladder rebooted this device in the
	// last hour, and 0 when that is under the limit. The device then stops every
	// automatic action and shows the state (plan 3.3, rung 4).
	RebootLoops int

	// Trusted says that the caller has a right to the whole report: the device
	// itself, an admin with a session, or the fleet server on a heartbeat.
	//
	// /api/status needs no session, because the fallback screen and the login page
	// read it (D46). A few of its fields are therefore a gift to anybody on the
	// LAN. The two change-me warnings name a box whose password is in the manual.
	// The URL of the item that plays now is very often a dashboard link with a share
	// token in it. redact takes those out for an untrusted caller.
	Trusted bool
}

// The codec names and the picture heights that a codec report may hold. A report
// comes from the player, which is a web page, so the daemon keeps the values that
// it knows and drops the rest.
var (
	codecNames  = []string{"h264", "hevc", "vp9", "av1"}
	codecHeight = []string{"1080", "2160"}
)

// CleanCodecs keeps the codec names and the sizes that this product knows.
//
// The report arrives in an HTTP body. Without this step a page could make the
// daemon hold a map of any size and serve it again to every caller of
// /api/status.
func CleanCodecs(in manifest.CodecReport) manifest.CodecReport {
	if in == nil {
		return nil
	}
	out := make(manifest.CodecReport, len(codecNames))
	for _, name := range codecNames {
		bands, ok := in[name]
		if !ok {
			continue
		}
		kept := make(map[string]manifest.CodecSupport, len(codecHeight))
		for _, height := range codecHeight {
			if support, ok := bands[height]; ok {
				kept[height] = support
			}
		}
		if len(kept) > 0 {
			out[name] = kept
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Reporter builds the status. It holds the facts that cannot change while the
// daemon runs: the image version and the hash of the package manifest (D50).
// Reading and hashing that file at every call would cost milliseconds for an
// answer that is always the same.
type Reporter struct {
	src Sources

	once         sync.Once
	imageVersion string
	manifestHash string

	// The total memory is read once: it is a fact of the machine, and Tier asks
	// for it at every call of Status.
	totalOnce sync.Once
	ramTotal  uint64
}

// New makes a Reporter. An empty root takes the true path of a device.
func New(src Sources) *Reporter {
	if src.ProcRoot == "" {
		src.ProcRoot = "/proc"
	}
	if src.SysRoot == "" {
		src.SysRoot = "/sys"
	}
	if src.EtcRoot == "" {
		src.EtcRoot = "/etc"
	}
	if src.ShareRoot == "" {
		src.ShareRoot = "/usr/share/portapixel"
	}
	return &Reporter{src: src}
}

// Status builds the report.
func (r *Reporter) Status(in Inputs) manifest.Status {
	r.once.Do(r.readRelease)

	free, total := space(r.src.MediaRoot)
	// One read of /proc/meminfo for the whole report. Three reads of one file for
	// one answer is three chances to give numbers that do not agree.
	ramTotal, ramFree := r.memory()
	out := manifest.Status{
		DeviceID: in.DeviceID,
		Name:     in.Config.Device.Name,
		MDNSName: netcfg.MDNSName(in.Config, in.DeviceID),
		IPs:      LocalIPs(),

		Version:             version.Version,
		ImageVersion:        r.imageVersion,
		PackageManifestHash: r.manifestHash,
		Arch:                version.Arch(),
		Tier:                r.Tier(in.Config),

		UptimeSeconds:   r.uptime(),
		Load:            r.load(),
		TempC:           r.temperature(),
		RAMTotalBytes:   ramTotal,
		RAMFreeBytes:    ramFree,
		MediaTotalBytes: total,
		MediaFreeBytes:  free,

		BrowserState:     in.BrowserState,
		NavigationRung:   in.NavigationRung,
		DisplayConnected: in.DisplayConnected,
		ScreenOn:         in.ScreenOn,
		NowPlaying:       in.NowPlaying,

		Paired:         in.Paired,
		ServerURL:      in.ServerURL,
		LastSync:       in.LastSync,
		LastSyncResult: in.LastSyncResult,
		SyncError:      in.SyncError,

		ClockSynced:      in.ClockSynced,
		Timezone:         in.Config.Device.Timezone,
		ConfigFromShadow: in.ConfigFromShadow,
		HardwareChanged:  in.HardwareChanged,
		Codecs:           in.Codecs,
		PairingCode:      in.PairingCode,
		Update:           in.Update,
	}
	if out.LastSyncResult == "" {
		out.LastSyncResult = "never"
	}
	out.Warnings = r.warnings(in)
	if !in.Trusted {
		redact(&out)
	}
	return out
}

// redact takes the fields that only a trusted caller may read out of the report.
//
// What stays: the name, the device ID, the addresses, the health numbers, the
// state of the browser and of the screen, the playlist that plays and the
// warnings that name no secret. That is what the login page and a monitor need.
//
// What goes:
//   - the two change-me warnings. They tell a port sweep exactly which box still
//     answers to the password that the manual prints (D22, D23).
//   - the address of the fleet server and the text of a sync fault. The two draw
//     the map of the site and name its internal hosts.
//   - the URL of a url item. A signage dashboard link very often carries a share
//     token in its query, and the whole value would go to any caller while the
//     item is on the screen. The scheme and the host stay, so a person can still
//     see which site is up.
func redact(out *manifest.Status) {
	kept := out.Warnings[:0]
	for _, w := range out.Warnings {
		if w.Code == manifest.WarnWebPassword || w.Code == manifest.WarnRootPassword {
			continue
		}
		kept = append(kept, w)
	}
	out.Warnings = kept

	out.ServerURL = ""
	out.SyncError = ""

	if out.NowPlaying != nil && out.NowPlaying.Kind == "url" {
		short := *out.NowPlaying
		short.Item = urlOrigin(short.Item)
		out.NowPlaying = &short
	}
}

// urlOrigin gives the scheme and the host of an address and drops the path and
// the query, which is where a share token lives. A value that is not an address
// becomes the one word "url", because a name that we cannot read may hold
// anything.
func urlOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "url"
	}
	return u.Scheme + "://" + u.Host
}

// warnings gives the loud messages of the dashboard and the fallback screen.
//
// Each message carries a code. The UI matches the code, never the words, so a
// better sentence cannot break a banner.
func (r *Reporter) warnings(in Inputs) []manifest.Warning {
	out := make([]manifest.Warning, 0, 4)
	add := func(code, message string) {
		out = append(out, manifest.Warning{Code: code, Message: message})
	}
	if in.Config.Web.Password == DefaultWebPassword {
		add(manifest.WarnWebPassword, "The web password is still the default one. Change it on the Settings page.")
	}
	if r.rootPasswordIsDefault() {
		add(manifest.WarnRootPassword, "The root password is still the default one. Change it on the Settings page.")
	}
	if in.Config.Device.Timezone == "UTC" {
		add(manifest.WarnTimezoneUTC, "The time zone is UTC. Set your time zone, or the schedules use the wrong hours.")
	}
	if !in.ClockSynced {
		add(manifest.WarnClockUnsynced, "The clock is not synchronised yet. The default playlist plays until it is.")
	}
	if in.ConfigFromShadow {
		add(manifest.WarnConfigShadow, "portapixel.toml on the media partition is missing or bad. The device runs from the last good copy.")
	}
	if in.ConfigWarning != "" && !in.ConfigFromShadow {
		code := in.ConfigWarningCode
		if code == "" {
			code = manifest.WarnConfigRepaired
		}
		add(code, "The configuration file has a fault: "+in.ConfigWarning)
	}
	if in.HardwareChanged {
		add(manifest.WarnHardwareChanged, "The hardware of this device changed. The device kept its name and its pairing, and it reports the new identity.")
	}
	if in.ServerInsecure {
		add(manifest.WarnServerInsecure, "The server address starts with http:// and it is not on this network. The device token goes over the internet in clear text. Use https://.")
	}
	if in.RebootLoops > 0 {
		add(manifest.WarnRebootLoop, "This device rebooted "+strconv.Itoa(in.RebootLoops)+
			" times in the last hour and did not come up. Automatic updates are off until it does. Look at the event log.")
	}
	if in.MDNSNameTaken != "" {
		add(manifest.WarnMDNSNameTaken, "Another device on this network already answers for "+
			in.MDNSNameTaken+". This device announces its factory name instead. Give the two devices different names.")
	}
	// A low tier device needs zram swap. Chromium uses more memory than the engine of
	// the first design, and a low tier device with no swap restarts the browser again
	// and again. The OS layer switches zram on; this is the report of it.
	if r.Tier(in.Config) == "low" && !r.zramActive() {
		add(manifest.WarnZramOff, "This device has less than 1 GB of memory and no zram swap. "+
			"The browser may restart again and again. Switch zram on in the operating system.")
	}
	if in.Update.State == manifest.UpdateRolledBack {
		add(manifest.WarnUpdateRolledBack, "An update did not come up and the device went back to "+in.Update.Current+". It never tries that release again.")
	}
	for _, p := range in.Problems {
		add(manifest.WarnPlaylistProblem, p)
	}
	return out
}

// zramActive reports if a zram device gives this machine swap space.
//
// /proc/swaps names every swap area that the kernel uses. A device that is not
// there gives no swap, whatever /sys/block holds: the zram-init package can be
// installed and its service switched off. A machine with no /proc/swaps at all is a
// development machine, and that answers false, so the warning appears only where the
// tier is low.
func (r *Reporter) zramActive() bool {
	for _, line := range strings.Split(readFile(filepath.Join(r.src.ProcRoot, "swaps")), "\n") {
		if strings.HasPrefix(line, "/dev/zram") {
			return true
		}
	}
	return false
}

// rootPasswordIsDefault compares the hash in /etc/shadow with the hash that the
// first boot saved. The same hash means that nobody changed the password (D23).
//
// A missing file on either side gives false: we never nag on a guess.
func (r *Reporter) rootPasswordIsDefault() bool {
	saved := trimValue(readFile(filepath.Join(r.src.StateDir, RootHashFile)))
	if saved == "" {
		return false
	}
	shadow := readFile(filepath.Join(r.src.EtcRoot, "shadow"))
	for _, line := range strings.Split(shadow, "\n") {
		if strings.HasPrefix(line, "root:") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				return parts[1] == saved
			}
		}
	}
	return false
}

// Tier says if this device is low tier or high tier (plan section 4). The value
// "auto" in the configuration asks the device to decide, and the memory is the
// whole test. Every Raspberry Pi model that is too slow for a crossfade has 1 GiB
// or less. A list of model names said the same thing a second time.
//
// The memory of a machine does not change while it runs, so the answer comes from
// the value that the first report read.
func (r *Reporter) Tier(cfg config.Config) string {
	switch cfg.Device.Tier {
	case "low", "high":
		return cfg.Device.Tier
	}
	if total, _ := r.memory(); total > 0 && total < lowRAMBytes {
		return "low"
	}
	return "high"
}

// memory gives MemTotal and MemAvailable from one read of /proc/meminfo.
//
// MemTotal is read once for the life of the daemon: the memory of a machine does
// not change, and Tier asks for it at every call of Status.
func (r *Reporter) memory() (total, free uint64) {
	values := r.meminfo("MemTotal", "MemAvailable")
	r.totalOnce.Do(func() { r.ramTotal = values["MemTotal"] })
	return r.ramTotal, values["MemAvailable"]
}

// readRelease reads the two files that say which image this is (D50).
func (r *Reporter) readRelease() {
	for _, line := range strings.Split(readFile(filepath.Join(r.src.EtcRoot, "portapixel-release")), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key == "PORTAPIXEL_VERSION" {
			r.imageVersion = strings.Trim(value, `"`)
		}
	}
	if data, err := os.ReadFile(filepath.Join(r.src.ShareRoot, "packages.manifest")); err == nil {
		sum := sha256.Sum256(data)
		r.manifestHash = hex.EncodeToString(sum[:])
	}
}

// uptime reads the first number of /proc/uptime.
func (r *Reporter) uptime() int64 {
	fields := strings.Fields(readFile(filepath.Join(r.src.ProcRoot, "uptime")))
	if len(fields) == 0 {
		return 0
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return int64(seconds)
}

// load reads the one minute load average.
func (r *Reporter) load() float64 {
	fields := strings.Fields(readFile(filepath.Join(r.src.ProcRoot, "loadavg")))
	if len(fields) == 0 {
		return 0
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return value
}

// temperature gives the highest thermal zone in degrees Celsius. The zones are
// the processor, and on a Pi also the memory controller; the highest one is the
// number that matters for throttling.
func (r *Reporter) temperature() float64 {
	zones, err := filepath.Glob(filepath.Join(r.src.SysRoot, "class", "thermal", "thermal_zone*", "temp"))
	if err != nil {
		return 0
	}
	sort.Strings(zones)
	highest := 0.0
	for _, zone := range zones {
		milli, err := strconv.ParseFloat(strings.TrimSpace(readFile(zone)), 64)
		if err != nil {
			continue
		}
		if c := milli / 1000; c > highest {
			highest = c
		}
	}
	return highest
}

// meminfo reads /proc/meminfo once and gives the named keys in bytes. The file
// gives kibibytes.
func (r *Reporter) meminfo(keys ...string) map[string]uint64 {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	out := make(map[string]uint64, len(keys))
	for _, line := range strings.Split(readFile(filepath.Join(r.src.ProcRoot, "meminfo")), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !want[name] {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		if kib, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			out[name] = kib * 1024
		}
	}
	return out
}

// LocalIPs gives the addresses of the device, without the loopback address. The
// Host allowlist and the fallback screen both use the list (D46, D18).
func LocalIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || !ipnet.IP.IsGlobalUnicast() {
				continue
			}
			out = append(out, ipnet.IP.String())
		}
	}
	sort.Strings(out)
	return out
}

// Hosts gives the Host header allowlist of the local API (D46, ARCHITECTURE
// section 6): the loopback names, every address of the device, and the two mDNS
// names.
func Hosts(cfg config.Config, deviceID string, port int) []string {
	out := []string{"localhost", "127.0.0.1", "[::1]", "::1"}
	out = append(out, LocalIPs()...)
	out = append(out, netcfg.MDNSName(cfg, deviceID), netcfg.Slug(cfg.Device.Name)+".local")
	if port != 0 && port != 80 {
		// The names with the port. httpguard compares both forms, but a name that
		// holds a port and no name is not a name we know.
		with := make([]string, 0, len(out))
		for _, h := range out {
			with = append(with, fmt.Sprintf("%s:%d", h, port))
		}
		out = append(out, with...)
	}
	return out
}

// trimValue cuts the whitespace and the trailing NUL bytes of a value that came
// from a file. A file that a shell script wrote can end with a NUL, and a hash
// that holds one is not equal to the same hash without one.
func trimValue(s string) string {
	return strings.Trim(s, " \t\r\n\x00")
}

// readFile reads a small file and gives "" when it cannot.
func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
