package netcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethanpil/portapixel/internal/config"
	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/slug"
)

// The names of the two network interfaces that PortaPixel knows. One image runs
// on many machines, and an interface that does not exist costs nothing: ifupdown
// reports it and goes on.
const (
	WiredIface = "eth0"
	WifiIface  = "wlan0"
)

// The paths of the rendered files, relative to the root.
const (
	InterfacesPath = "etc/network/interfaces"
	WPAPath        = "etc/wpa_supplicant/wpa_supplicant.conf"
	ResolvPath     = "etc/resolv.conf"
	HostnamePath   = "etc/hostname"
)

// DefaultName is the factory device name. While the name is this value, the
// device announces itself as portapixel-<last4 of ID> (D20).
const DefaultName = "PortaPixel"

// Interfaces renders /etc/network/interfaces for ifupdown-ng.
func Interfaces(cfg config.Config) []byte {
	var b strings.Builder
	b.WriteString("# PortaPixel writes this file from portapixel.toml.\n")
	b.WriteString("# Your changes are lost at the next boot. Edit portapixel.toml instead.\n")
	b.WriteString("\nauto lo\niface lo inet loopback\n")

	wifi := cfg.Network.WifiSSID != ""
	static := cfg.Network.Mode == "static"

	// The static address goes to the interface that carries the traffic.
	b.WriteString("\nauto " + WiredIface + "\n")
	if static && !wifi {
		writeStatic(&b, WiredIface, cfg)
	} else {
		b.WriteString("iface " + WiredIface + " inet dhcp\n")
	}

	if wifi {
		b.WriteString("\nauto " + WifiIface + "\n")
		if static {
			writeStatic(&b, WifiIface, cfg)
		} else {
			b.WriteString("iface " + WifiIface + " inet dhcp\n")
		}
	}
	return []byte(b.String())
}

// writeStatic writes one static stanza. ifupdown-ng takes the prefix length in
// the address, so "192.168.1.50/24" needs no netmask line.
func writeStatic(b *strings.Builder, iface string, cfg config.Config) {
	b.WriteString("iface " + iface + " inet static\n")
	b.WriteString("\taddress " + cfg.Network.Address + "\n")
	if cfg.Network.Gateway != "" {
		b.WriteString("\tgateway " + cfg.Network.Gateway + "\n")
	}
}

// WPASupplicant renders /etc/wpa_supplicant/wpa_supplicant.conf.
//
// With no SSID the file holds the header only. An empty file is better than an
// old file. A device that no longer has WiFi settings must not join the network
// of last month.
//
// network.wifi_country gives the regulatory domain. An empty value writes no
// country line: a wrong domain is worse than none. With no domain some radios
// permit fewer channels, and a 5 GHz network can be invisible. The code goes in
// capital letters, which is the form that wpa_supplicant documents.
func WPASupplicant(cfg config.Config) []byte {
	var b strings.Builder
	b.WriteString("# PortaPixel writes this file from portapixel.toml.\n")
	b.WriteString("# Your changes are lost at the next boot. Edit portapixel.toml instead.\n")
	b.WriteString("ctrl_interface=/var/run/wpa_supplicant\n")
	b.WriteString("update_config=0\n")
	if country := strings.ToUpper(strings.TrimSpace(cfg.Network.WifiCountry)); country != "" {
		b.WriteString("country=" + country + "\n")
	}

	ssid := cfg.Network.WifiSSID
	if ssid == "" {
		return []byte(b.String())
	}

	b.WriteString("\nnetwork={\n")
	b.WriteString("\tssid=\"" + escapeWPA(ssid) + "\"\n")
	switch psk := cfg.Network.WifiPSK; {
	case psk == "":
		// An open network. Without this line wpa_supplicant looks for a key.
		b.WriteString("\tkey_mgmt=NONE\n")
	case isHexKey(psk):
		// A 64-character hex value is the key itself, not a pass phrase. It goes
		// in without quotation marks, or wpa_supplicant hashes it again.
		b.WriteString("\tpsk=" + strings.ToLower(psk) + "\n")
	default:
		b.WriteString("\tpsk=\"" + escapeWPA(psk) + "\"\n")
	}
	b.WriteString("}\n")
	return []byte(b.String())
}

// ResolvConf renders /etc/resolv.conf. It gives no content for a DHCP device:
// the DHCP client owns the file then, and a file that we write would fight it.
func ResolvConf(cfg config.Config) []byte {
	if cfg.Network.Mode != "static" || len(cfg.Network.DNS) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("# PortaPixel writes this file from portapixel.toml.\n")
	for _, server := range cfg.Network.DNS {
		if s := strings.TrimSpace(server); s != "" {
			b.WriteString("nameserver " + s + "\n")
		}
	}
	return []byte(b.String())
}

// Hostname renders /etc/hostname from the device name.
func Hostname(cfg config.Config) []byte {
	return []byte(Slug(cfg.Device.Name) + "\n")
}

// Slug makes a host name from a display name. mDNS and /etc/hostname both use
// it, so the name that the user types in the web UI is the name that they type in
// a browser. A name with nothing usable in it gives "portapixel": a host with no
// name is a host that nobody can reach.
func Slug(name string) string {
	if out := slug.Make(name); out != "" {
		return out
	}
	return "portapixel"
}

// MDNSName gives the name that the device announces (D20). While the device
// name is still the factory default, the name carries the last 4 characters of
// the device ID, so that two new devices on one network do not fight over one
// name.
func MDNSName(cfg config.Config, deviceID string) string {
	if strings.TrimSpace(cfg.Device.Name) == DefaultName {
		last4 := deviceID
		if len(last4) > 4 {
			last4 = last4[len(last4)-4:]
		}
		return "portapixel-" + strings.ToLower(last4) + ".local"
	}
	return Slug(cfg.Device.Name) + ".local"
}

// Write puts every rendered file under root. root is "" or "/" on a device and a
// temporary directory in a test. Each write is staged and committed with a
// rename, so a power cut in the middle keeps the last good file (D41).
//
// A missing parent directory is made. The wpa_supplicant file gets mode 0600,
// because it holds the WiFi key.
func Write(root string, cfg config.Config) error {
	files := []struct {
		path string
		data []byte
		perm os.FileMode
	}{
		{InterfacesPath, Interfaces(cfg), 0o644},
		{WPAPath, WPASupplicant(cfg), 0o600},
		{HostnamePath, Hostname(cfg), 0o644},
		{ResolvPath, ResolvConf(cfg), 0o644},
	}
	for _, f := range files {
		if f.data == nil {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("make the directory for %s: %w", full, err)
		}
		if err := fsutil.WriteFileAtomic(full, f.data, f.perm); err != nil {
			return err
		}
	}
	return nil
}

// escapeWPA escapes the two characters that would end a wpa_supplicant string.
func escapeWPA(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// isHexKey reports if a PSK is a 64-character hex key.
func isHexKey(psk string) bool {
	if len(psk) != 64 {
		return false
	}
	for _, r := range strings.ToLower(psk) {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
