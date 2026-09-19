package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"golang.org/x/crypto/bcrypt"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// ConfigName is the name of the configuration file in the data directory.
const ConfigName = "server.toml"

// DatabaseName is the name of the database file in the data directory.
const DatabaseName = "portapixel.db"

// The defaults of a new server.toml.
const (
	defaultListen = ":8080"
	defaultRepo   = "ethanpil/portapixel"
	defaultPoll   = 60
)

// Config is server.toml. It holds the values that a restart applies: the listen
// address, the certificate, the public URL and the password hash. Everything that
// the admin UI changes while the server runs is in the settings table of the
// database, except the four values that the UI writes back here.
type Config struct {
	Listen             string `toml:"listen"`
	PublicURL          string `toml:"public_url"`
	AdminPasswordHash  string `toml:"admin_password_hash"`
	TLSCert            string `toml:"tls_cert"`
	TLSKey             string `toml:"tls_key"`
	GitHubRepo         string `toml:"github_repo"`
	DefaultPollSeconds int    `toml:"default_poll_seconds"`
}

// defaults gives a configuration with nothing set.
func defaults() Config {
	return Config{
		Listen:             defaultListen,
		GitHubRepo:         defaultRepo,
		DefaultPollSeconds: defaultPoll,
	}
}

// ConfigPath gives the name of the configuration file in a data directory.
func ConfigPath(dataDir string) string { return filepath.Join(dataDir, ConfigName) }

// LoadConfig reads server.toml. It makes the file with the defaults when it is
// not there, so a first run needs no hand-written file.
//
// A bad value gets its default back and a warning. The device configuration works
// the same way and for the same reason: one wrong line must never stop the whole
// server (D38 in spirit).
func LoadConfig(dataDir string) (Config, []string, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return Config{}, nil, fmt.Errorf("make %s: %w", dataDir, err)
	}
	path := ConfigPath(dataDir)

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := defaults()
		if err := SaveConfig(dataDir, cfg); err != nil {
			return Config{}, nil, err
		}
		return cfg, nil, nil
	}
	if err != nil {
		return Config{}, nil, fmt.Errorf("read %s: %w", path, err)
	}

	cfg := defaults()
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, nil, fmt.Errorf("read %s: %w", path, err)
	}

	var warnings []string
	if strings.TrimSpace(cfg.Listen) == "" {
		cfg.Listen = defaultListen
		warnings = append(warnings, "listen was empty, so the server uses "+defaultListen)
	}
	if strings.TrimSpace(cfg.GitHubRepo) == "" {
		cfg.GitHubRepo = defaultRepo
		warnings = append(warnings, "github_repo was empty, so the server uses "+defaultRepo)
	}
	if cfg.DefaultPollSeconds < 5 || cfg.DefaultPollSeconds > 86400 {
		warnings = append(warnings, fmt.Sprintf(
			"default_poll_seconds was %d, so the server uses %d", cfg.DefaultPollSeconds, defaultPoll))
		cfg.DefaultPollSeconds = defaultPoll
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		warnings = append(warnings, "tls_cert and tls_key need each other, so the server answers plain HTTP")
		cfg.TLSCert, cfg.TLSKey = "", ""
	}
	return cfg, warnings, nil
}

// SaveConfig writes server.toml.
//
// The template is written by hand. The contract permits the TOML package for
// reading only, and a file that a person edits needs its comments back after
// every save.
func SaveConfig(dataDir string, cfg Config) error {
	var b strings.Builder
	b.WriteString(`# PortaPixel fleet server configuration.
# The server reads this file when it starts. The admin UI writes it again when
# somebody changes a setting or the password: these comments come back, your own
# comments do not.

# The address that the server listens on. ":8080" answers on every interface.
`)
	fmt.Fprintf(&b, "listen = %s\n\n", quote(cfg.Listen))
	b.WriteString(`# The address that a browser and a device use to reach this server. The Host
# header allowlist and the pairing block for a card come from it. Leave it empty
# on a closed network.
`)
	fmt.Fprintf(&b, "public_url = %s\n\n", quote(cfg.PublicURL))
	b.WriteString(`# The bcrypt hash of the admin password. The first run makes a random password,
# prints it one time, and stores its hash here. Change it in the admin UI or with
# "portapixel-server set-password".
`)
	fmt.Fprintf(&b, "admin_password_hash = %s\n\n", quote(cfg.AdminPasswordHash))
	b.WriteString(`# The certificate and the private key of HTTPS. Set both, or neither for plain
# HTTP behind a reverse proxy. There is no automatic certificate in this release.
`)
	fmt.Fprintf(&b, "tls_cert = %s\n", quote(cfg.TLSCert))
	fmt.Fprintf(&b, "tls_key = %s\n\n", quote(cfg.TLSKey))
	b.WriteString("# The repository that the release list comes from.\n")
	fmt.Fprintf(&b, "github_repo = %s\n\n", quote(cfg.GitHubRepo))
	b.WriteString("# How often a device calls, in seconds, when it has no value of its own.\n")
	fmt.Fprintf(&b, "default_poll_seconds = %d\n", cfg.DefaultPollSeconds)

	// The file holds a password hash, so only the owner may read it.
	return fsutil.WriteFileAtomic(ConfigPath(dataDir), []byte(b.String()), 0o600)
}

// quote puts a value in the TOML basic form.
func quote(s string) string {
	out := make([]rune, 0, len(s)+2)
	out = append(out, '"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			out = append(out, '\\', r)
		case '\n', '\r', '\t':
			out = append(out, ' ')
		default:
			out = append(out, r)
		}
	}
	return string(append(out, '"'))
}

// minPasswordLength is the shortest admin password that the server takes.
const minPasswordLength = 8

// HashPassword checks a password and gives its bcrypt hash.
func HashPassword(password string) (string, error) {
	if len([]rune(password)) < minPasswordLength {
		return "", fmt.Errorf("the password needs %d characters or more", minPasswordLength)
	}
	// bcrypt takes 72 bytes and silently drops the rest, so a long passphrase
	// must be refused instead of cut.
	if len(password) > 72 {
		return "", errors.New("the password must be 72 bytes or fewer")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword compares a password with a bcrypt hash. bcrypt takes the same
// time for a wrong password as for a right one.
func CheckPassword(password, hash string) bool {
	if password == "" || hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// generatedPasswordWords is the alphabet of a generated password. It leaves out
// the characters that a person reads wrongly, because the first run prints this
// password and somebody types it.
const generatedPasswordWords = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// generatedPasswordLength gives about 95 bits of entropy from the alphabet above.
const generatedPasswordLength = 16

// GeneratePassword makes the first admin password.
//
// This is the homelab answer to "how does the admin get in the first time". The
// other ways are a fixed default password, which is a door that nobody closes, or
// a setup page with no password at all, which is the same door. A random password
// that the log prints one time needs no extra step from the admin and leaves
// nothing open.
func GeneratePassword() string {
	out := make([]byte, generatedPasswordLength)
	max := big.NewInt(int64(len(generatedPasswordWords)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic("portapixel-server: the system gave no random bytes: " + err.Error())
		}
		out[i] = generatedPasswordWords[n.Int64()]
	}
	return string(out)
}

// AllowedHosts gives the Host header allowlist of the admin UI (D46). It holds
// the public URL, the listen address and the loopback names.
//
// A device is not in this list and does not need to be: the device API carries a
// bearer token and no cookie, so a rebound name gains an attacker nothing there.
// This list protects the browser side.
func AllowedHosts(cfg Config) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1", "[::1]"}

	_, port, err := net.SplitHostPort(cfg.Listen)
	if err == nil && port != "" {
		hosts = append(hosts, "localhost:"+port, "127.0.0.1:"+port, "[::1]:"+port)
		// A listen address with a host in it is a name that somebody uses.
		if host, _, _ := net.SplitHostPort(cfg.Listen); host != "" && host != "0.0.0.0" && host != "::" {
			hosts = append(hosts, host, net.JoinHostPort(host, port))
		}
	}
	if cfg.PublicURL != "" {
		if u, err := url.Parse(cfg.PublicURL); err == nil && u.Host != "" {
			hosts = append(hosts, u.Host, u.Hostname())
		}
	}
	return hosts
}

// listenPort gives the port of the listen address as a number, for the messages
// that the start prints.
func listenPort(listen string) string {
	if _, port, err := net.SplitHostPort(listen); err == nil {
		if _, err := strconv.Atoi(port); err == nil {
			return port
		}
	}
	return ""
}
