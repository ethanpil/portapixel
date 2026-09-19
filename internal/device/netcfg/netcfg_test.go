package netcfg

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethanpil/portapixel/internal/config"
)

// update writes the golden files again. Run "go test ./internal/device/netcfg
// -update" after a deliberate change of the output, and read the diff.
var update = flag.Bool("update", false, "write the golden files again")

// golden compares data with the file testdata/<name>.
func golden(t *testing.T, name string, data []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the golden file: %v (run the test with -update)", err)
	}
	if string(data) != string(want) {
		t.Errorf("%s is different.\n--- got ---\n%s\n--- want ---\n%s", name, data, want)
	}
}

// base gives a default configuration with a name that makes a good slug.
func base() config.Config {
	cfg := config.Default()
	cfg.Device.Name = "Lobby Screen"
	return cfg
}

func TestInterfaces(t *testing.T) {
	dhcp := base()

	static := base()
	static.Network.Mode = "static"
	static.Network.Address = "192.168.1.50/24"
	static.Network.Gateway = "192.168.1.1"
	static.Network.DNS = []string{"192.168.1.1", "1.1.1.1"}

	wifi := base()
	wifi.Network.WifiSSID = "Office WiFi"
	wifi.Network.WifiPSK = "a secret"

	wifiStatic := static
	wifiStatic.Network.WifiSSID = "Office WiFi"
	wifiStatic.Network.WifiPSK = "a secret"

	staticNoGateway := base()
	staticNoGateway.Network.Mode = "static"
	staticNoGateway.Network.Address = "10.0.0.9/8"

	tests := []struct {
		name string
		cfg  config.Config
	}{
		{"interfaces-dhcp", dhcp},
		{"interfaces-static", static},
		{"interfaces-wifi-dhcp", wifi},
		{"interfaces-wifi-static", wifiStatic},
		{"interfaces-static-no-gateway", staticNoGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			golden(t, tt.name, Interfaces(tt.cfg))
		})
	}
}

func TestWPASupplicant(t *testing.T) {
	none := base()

	pass := base()
	pass.Network.WifiSSID = "Office WiFi"
	pass.Network.WifiPSK = "correct horse"

	hexKey := base()
	hexKey.Network.WifiSSID = "Office WiFi"
	hexKey.Network.WifiPSK = "0123456789ABCDEF0123456789abcdef0123456789ABCDEF0123456789abcdef"

	open := base()
	open.Network.WifiSSID = "Guest"

	quoted := base()
	quoted.Network.WifiSSID = `He said "hi"\`
	quoted.Network.WifiPSK = `back\slash"quote`

	// The regulatory domain (network.wifi_country). Without it some radios permit
	// fewer channels, and a 5 GHz network can be invisible.
	country := base()
	country.Network.WifiSSID = "Office WiFi"
	country.Network.WifiPSK = "correct horse"
	country.Network.WifiCountry = "de" // it comes out in capital letters

	countryOnly := base()
	countryOnly.Network.WifiCountry = "US"

	tests := []struct {
		name string
		cfg  config.Config
	}{
		{"wpa-none", none},
		{"wpa-passphrase", pass},
		{"wpa-hex-key", hexKey},
		{"wpa-open", open},
		{"wpa-escapes", quoted},
		{"wpa-country", country},
		{"wpa-country-no-ssid", countryOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			golden(t, tt.name, WPASupplicant(tt.cfg))
		})
	}
}

func TestResolvConf(t *testing.T) {
	dhcp := base()
	dhcp.Network.DNS = []string{"1.1.1.1"} // ignored: DHCP owns the file
	if got := ResolvConf(dhcp); got != nil {
		t.Errorf("a DHCP device wrote resolv.conf: %q", got)
	}

	static := base()
	static.Network.Mode = "static"
	static.Network.Address = "192.168.1.50/24"
	static.Network.DNS = []string{"192.168.1.1", " 1.1.1.1 ", ""}
	golden(t, "resolv-static", ResolvConf(static))
}

func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"PortaPixel", "portapixel"},
		{"Lobby Screen", "lobby-screen"},
		{"  Front  Desk  ", "front-desk"},
		{"Café #2", "caf-2"},
		{"---", "portapixel"},
		{"", "portapixel"},
		{"Screen_01", "screen-01"},
	}
	for _, tt := range tests {
		if got := Slug(tt.in); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMDNSName(t *testing.T) {
	factory := config.Default() // name is still "PortaPixel"
	if got := MDNSName(factory, "px-1a2b3c4d"); got != "portapixel-3c4d.local" {
		t.Errorf("MDNSName = %q, want portapixel-3c4d.local", got)
	}
	named := base()
	if got := MDNSName(named, "px-1a2b3c4d"); got != "lobby-screen.local" {
		t.Errorf("MDNSName = %q, want lobby-screen.local", got)
	}
}

func TestWrite(t *testing.T) {
	root := t.TempDir()
	cfg := base()
	cfg.Network.Mode = "static"
	cfg.Network.Address = "192.168.1.50/24"
	cfg.Network.DNS = []string{"192.168.1.1"}
	cfg.Network.WifiSSID = "Office WiFi"
	cfg.Network.WifiPSK = "a secret"

	if err := Write(root, cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{InterfacesPath, WPAPath, HostnamePath, ResolvPath} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s is missing: %v", name, err)
		}
	}
	host, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(HostnamePath)))
	if err != nil || string(host) != "lobby-screen\n" {
		t.Errorf("hostname = %q, %v", host, err)
	}
	// A second run must give the same result.
	if err := Write(root, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestWriteSkipsResolvConfForDHCP(t *testing.T) {
	root := t.TempDir()
	if err := Write(root, base()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ResolvPath))); !os.IsNotExist(err) {
		t.Errorf("Write made resolv.conf for a DHCP device")
	}
}
