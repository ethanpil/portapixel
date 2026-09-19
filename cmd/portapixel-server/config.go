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

	"github.com/ethanpil/portapixel/internal/config"
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
)

// The environment variables that replace a value of server.toml. A Docker user
// expects to set an address in the compose file, and the file in the volume is not
// where they would look. The environment wins over the file.
const (
	envPublicURL      = "PORTAPIXEL_PUBLIC_URL"
	envListen         = "PORTAPIXEL_LISTEN"
	envTrustedProxies = "PORTAPIXEL_TRUSTED_PROXIES"
)

// Config is server.toml. It holds the values that the file owns: the listen
// address, the certificate, the public URL, the password hash and the proxies that
// the server believes.
//
// The poll interval is not here. It lives in the settings table, which the admin UI
// writes, and one value in two places needs a precedence rule that nobody
// remembers.
type Config struct {
	Listen            string `toml:"listen"`
	PublicURL         string `toml:"public_url"`
	AdminPasswordHash string `toml:"admin_password_hash"`
	TLSCert           string `toml:"tls_cert"`
	TLSKey            string `toml:"tls_key"`
	GitHubRepo        string `toml:"github_repo"`
	// TrustedProxies names the peer addresses whose X-Forwarded-For and
	// X-Forwarded-Proto headers the server believes. Each entry is an address or a
	// CIDR block. See internal/server/httpjson.Proxies.
	TrustedProxies []string `toml:"trusted_proxies"`
}

// defaults gives a configuration with nothing set.
func defaults() Config {
	return Config{
		Listen:     defaultListen,
		GitHubRepo: defaultRepo,
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
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		warnings = append(warnings, "tls_cert and tls_key need each other, so the server answers plain HTTP")
		cfg.TLSCert, cfg.TLSKey = "", ""
	}
	cfg, envWarnings := applyEnv(cfg)
	return cfg, append(warnings, envWarnings...), nil
}

// applyEnv lets the environment replace three values of the file.
//
// A Docker user sets an address in the compose file and expects the program to read
// it. The value never goes back into server.toml: the file is the record of what a
// person typed there, and a variable of the container is not.
func applyEnv(cfg Config) (Config, []string) {
	var warnings []string
	if v := strings.TrimSpace(os.Getenv(envPublicURL)); v != "" {
		if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			warnings = append(warnings, envPublicURL+" must start with http:// or https://, so the server ignores it")
		} else {
			cfg.PublicURL = v
		}
	}
	if v := strings.TrimSpace(os.Getenv(envListen)); v != "" {
		cfg.Listen = v
	}
	if v := strings.TrimSpace(os.Getenv(envTrustedProxies)); v != "" {
		cfg.TrustedProxies = strings.Split(v, ",")
	}
	return cfg, warnings
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
	fmt.Fprintf(&b, "listen = %s\n\n", config.Quote(cfg.Listen))
	b.WriteString(`# The address that a browser and a device use to reach this server. The Host
# header allowlist and the pairing block for a card come from it. Leave it empty
# on a closed network. PORTAPIXEL_PUBLIC_URL replaces this value.
`)
	fmt.Fprintf(&b, "public_url = %s\n\n", config.Quote(cfg.PublicURL))
	b.WriteString(`# The bcrypt hash of the admin password. The first run makes a random password,
# prints it one time, and stores its hash here. Change it in the admin UI or with
# "portapixel-server set-password".
`)
	fmt.Fprintf(&b, "admin_password_hash = %s\n\n", config.Quote(cfg.AdminPasswordHash))
	b.WriteString(`# The certificate and the private key of HTTPS. Set both, or neither for plain
# HTTP behind a reverse proxy. There is no automatic certificate in this release.
`)
	fmt.Fprintf(&b, "tls_cert = %s\n", config.Quote(cfg.TLSCert))
	fmt.Fprintf(&b, "tls_key = %s\n\n", config.Quote(cfg.TLSKey))
	b.WriteString("# The repository that the release list comes from.\n")
	fmt.Fprintf(&b, "github_repo = %s\n\n", config.Quote(cfg.GitHubRepo))
	b.WriteString(`# The addresses of the reverse proxies that this server believes. Each entry is
# an address or a CIDR block. For a peer inside the list the server reads the
# client address from X-Forwarded-For and the scheme from X-Forwarded-Proto. For
# every other peer it ignores the two headers.
#
# Leave it empty when no proxy is in front. A wrong entry here would let a caller
# choose the address that the rate limiters count.
`)
	fmt.Fprintf(&b, "trusted_proxies = %s\n", quoteList(cfg.TrustedProxies))

	// The file holds a password hash, so only the owner may read it.
	return fsutil.WriteFileAtomic(ConfigPath(dataDir), []byte(b.String()), 0o600)
}

// quoteList makes a TOML array of strings. config.Quote writes each value, so the
// server and the device escape a string the same way.
func quoteList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			quoted = append(quoted, config.Quote(v))
		}
	}
	return "[" + strings.Join(quoted, ", ") + "]"
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
