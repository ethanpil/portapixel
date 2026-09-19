package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The names of the hardware sources. The ops log and the admin UI show them.
const (
	SourcePiSerial = "pi-serial"
	SourceDMIUUID  = "dmi-uuid"
	SourceMAC      = "mac"
	SourceHostname = "hostname"
)

// Prefix is the start of every device ID.
const Prefix = "px-"

// Identity is what the hardware says about itself.
type Identity struct {
	// DeviceID is the short public name: "px-" and 8 hex characters.
	DeviceID string
	// HardwareID is the full SHA-256 of the hardware value, as hex. The fleet
	// server compares it to find a repair or a clone (D21).
	HardwareID string
	// Source names the file that gave the value.
	Source string
}

// Paths of the hardware sources, relative to the root.
const (
	piSerialPath = "proc/device-tree/serial-number"
	dmiUUIDPath  = "sys/class/dmi/id/product_uuid"
	netDir       = "sys/class/net"
)

// Derive reads the hardware identity. root is the root of the file tree: "/" on
// a device, a test directory in a test.
//
// Derive always gives an identity. When no hardware source answers, it uses the
// host name and says so in Source. A development machine has no sysfs, and a
// daemon that cannot start is worse than a daemon with a weak identity. A device
// always has one of the three true sources.
func Derive(root string) Identity {
	if v := piSerial(root); v != "" {
		return fromValue(v, SourcePiSerial)
	}
	if v := dmiUUID(root); v != "" {
		return fromValue(v, SourceDMIUUID)
	}
	if v := firstMAC(root); v != "" {
		return fromValue(v, SourceMAC)
	}
	host, _ := os.Hostname()
	return fromValue(strings.ToLower(strings.TrimSpace(host)), SourceHostname)
}

// fromValue hashes the hardware value and cuts the device ID out of the hash.
func fromValue(value, source string) Identity {
	sum := sha256.Sum256([]byte(value))
	full := hex.EncodeToString(sum[:])
	return Identity{DeviceID: Prefix + full[:8], HardwareID: full, Source: source}
}

// piSerial reads the serial number of a Raspberry Pi. The device tree gives the
// value with a NUL character at the end.
func piSerial(root string) string {
	v := readTrimmed(filepath.Join(root, piSerialPath))
	if v == "" || isAllSame(v, '0') {
		return ""
	}
	return strings.ToLower(v)
}

// dmiUUID reads the product UUID of a PC. Many mainboards give a junk value
// here, so the value must look like a UUID and must hold real entropy.
func dmiUUID(root string) string {
	v := strings.ToLower(readTrimmed(filepath.Join(root, dmiUUIDPath)))
	if v == "" {
		return ""
	}
	// Words that mainboards write in place of a UUID.
	for _, junk := range []string{"not settable", "not present", "not applicable", "default string", "unknown", "to be filled"} {
		if strings.Contains(v, junk) {
			return ""
		}
	}
	hexOnly := strings.ReplaceAll(v, "-", "")
	if len(hexOnly) != 32 {
		return ""
	}
	for _, r := range hexOnly {
		if !isHex(r) {
			return ""
		}
	}
	// All zeros and all f are the two values that a mainboard writes when it has
	// nothing to say.
	if isAllSame(hexOnly, '0') || isAllSame(hexOnly, 'f') {
		return ""
	}
	return v
}

// firstMAC gives the MAC address of the first physical network interface, in
// name order. The order must be stable: an identity that changes with the
// order in which the kernel found the interfaces is not an identity.
func firstMAC(root string) string {
	entries, err := os.ReadDir(filepath.Join(root, netDir))
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		if virtualName(name) {
			continue
		}
		dir := filepath.Join(root, netDir, name)
		// A physical interface has a device link to the bus that carries it. A
		// bridge, a tunnel and a container interface do not.
		if _, err := os.Stat(filepath.Join(dir, "device")); err != nil {
			continue
		}
		// addr_assign_type 0 means that the address comes from the hardware. Any
		// other value is a random or a set address, which changes at a boot.
		if t := readTrimmed(filepath.Join(dir, "addr_assign_type")); t != "" && t != "0" {
			continue
		}
		mac := strings.ToLower(readTrimmed(filepath.Join(dir, "address")))
		if mac == "" || strings.Trim(mac, "0:") == "" {
			continue
		}
		return mac
	}
	return ""
}

// virtualName reports if an interface name belongs to an interface that the
// software made. The name is the cheapest test and it catches the interfaces
// that a container runtime adds after the boot.
func virtualName(name string) bool {
	if name == "lo" {
		return true
	}
	for _, prefix := range []string{"docker", "veth", "virbr", "br-", "vmnet", "tap", "tun", "wg", "dummy", "bond", "sit", "ip6tnl"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// readTrimmed reads a small sysfs or procfs file. It removes the white space
// and the NUL characters that the device tree adds.
func readTrimmed(p string) string {
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.Trim(string(data), " \t\r\n\x00")
}

// isAllSame reports if every character of s is c.
func isAllSame(s string, c byte) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != c {
			return false
		}
	}
	return true
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
}
