// Package netcfg makes the system network files from portapixel.toml.
//
// Why this package exists: the network settings live in one TOML file on a
// partition that any laptop can edit, but the boot needs them as
// /etc/network/interfaces and /etc/wpa_supplicant/wpa_supplicant.conf. No shell
// script parses TOML (ARCHITECTURE section 4), so the daemon renders the files
// in the "render-net" subcommand before the network service starts.
//
// The render functions are pure: configuration in, bytes out. Write puts the
// bytes on the disk under a root directory, which the tests set to a temporary
// directory. Golden files hold the expected output, because these files are
// read by other programs and a small change in the format is a field failure.
//
// One rule to know: the static address goes to the interface that carries the
// traffic. With a WiFi SSID set, that is wlan0 and eth0 falls back to DHCP.
// Without an SSID it is eth0. Two interfaces with one static address is a
// configuration that cannot work, so we never render it.
package netcfg
