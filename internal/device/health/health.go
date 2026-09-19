package health

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
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
	DeviceID         string

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

	ClockSynced bool
	// Problems are the playlist faults that the library found. Each one is a
	// sentence for a person.
	Problems []string
	Update   manifest.UpdateState
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
		PairingCode:      in.PairingCode,
		Update:           in.Update,
	}
	if out.LastSyncResult == "" {
		out.LastSyncResult = "never"
	}
	out.Warnings = r.warnings(in)
	return out
}

// warnings gives the loud messages of the dashboard and the fallback screen.
func (r *Reporter) warnings(in Inputs) []string {
	var out []string
	if in.Config.Web.Password == DefaultWebPassword {
		out = append(out, "The web password is still the default one. Change it on the Settings page.")
	}
	if r.rootPasswordIsDefault() {
		out = append(out, "The root password is still the default one. Change it on the Settings page.")
	}
	if in.Config.Device.Timezone == "UTC" {
		out = append(out, "The time zone is UTC. Set your time zone, or the schedules use the wrong hours.")
	}
	if !in.ClockSynced {
		out = append(out, "The clock is not synchronised yet. The default playlist plays until it is.")
	}
	if in.ConfigFromShadow {
		out = append(out, "portapixel.toml on the media partition is missing or bad. The device runs from the last good copy.")
	}
	if in.ConfigWarning != "" && !in.ConfigFromShadow {
		out = append(out, "The configuration file has a fault: "+in.ConfigWarning)
	}
	out = append(out, in.Problems...)
	return out
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
