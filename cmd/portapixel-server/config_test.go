package main

import (
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestLoadConfigMakesTheFileOnTheFirstRun(t *testing.T) {
	dir := t.TempDir() + "/data"

	cfg, warnings, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("a new file gave warnings: %v", warnings)
	}
	if cfg.Listen != defaultListen || cfg.GitHubRepo != defaultRepo {
		t.Fatalf("the defaults are %+v", cfg)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Fatalf("a new file trusts the proxies %v", cfg.TrustedProxies)
	}
	if cfg.AdminPasswordHash != "" {
		t.Fatal("a new file holds a password hash")
	}
	if _, err := os.Stat(ConfigPath(dir)); err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Config{
		Listen:            "127.0.0.1:9000",
		PublicURL:         "https://signage.example.com",
		AdminPasswordHash: "$2a$10$abcdefghijklmnopqrstuv",
		TLSCert:           "/etc/ssl/cert.pem",
		TLSKey:            "/etc/ssl/key.pem",
		GitHubRepo:        "someone/else",
		TrustedProxies:    []string{"127.0.0.1", "172.16.0.0/12"},
	}
	if err := SaveConfig(dir, want); err != nil {
		t.Fatal(err)
	}
	got, warnings, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("the round trip gave warnings: %v", warnings)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the file read back as\n%+v\nwant\n%+v", got, want)
	}
}

func TestConfigRepairsBadValues(t *testing.T) {
	dir := t.TempDir()
	// Only the certificate is set, the repository is empty, and the listen address
	// is empty. Each one gets its default back with a warning, and the good values
	// stay.
	content := "listen = \"\"\ngithub_repo = \"\"\ntls_cert = \"/a/cert\"\n" +
		"public_url = \"https://keep.example.com\"\n"
	if err := os.WriteFile(ConfigPath(dir), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, warnings, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 3 {
		t.Fatalf("the load gave %d warnings, want 3: %v", len(warnings), warnings)
	}
	if cfg.Listen != defaultListen || cfg.GitHubRepo != defaultRepo {
		t.Fatalf("the repair gave %+v", cfg)
	}
	if cfg.TLSCert != "" || cfg.TLSKey != "" {
		t.Fatal("a certificate with no key was kept")
	}
	if cfg.PublicURL != "https://keep.example.com" {
		t.Fatal("a bad value cost the user a good one")
	}
}

func TestConfigFileIsNotReadableByEverybody(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, defaults()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ConfigPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX modes, so the check runs where it means something. The
	// old form also let 0666 through, which is the worst mode that a POSIX system
	// could give this file.
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX file modes")
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the mode of the file is %v; it holds a password hash", info.Mode().Perm())
	}
}

func TestEnvironmentReplacesTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{
		Listen: ":8080", GitHubRepo: defaultRepo,
		PublicURL: "https://from-the-file.example",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envPublicURL, "https://from-the-environment.example")
	t.Setenv(envListen, "127.0.0.1:9999")
	t.Setenv(envTrustedProxies, "127.0.0.1, 172.16.0.0/12")

	cfg, warnings, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("the load gave warnings: %v", warnings)
	}
	if cfg.PublicURL != "https://from-the-environment.example" {
		t.Fatalf("the public URL is %q", cfg.PublicURL)
	}
	if cfg.Listen != "127.0.0.1:9999" {
		t.Fatalf("the listen address is %q", cfg.Listen)
	}
	if len(cfg.TrustedProxies) != 2 {
		t.Fatalf("the trusted proxies are %v", cfg.TrustedProxies)
	}

	// A public URL with no scheme is refused, and the value of the file stays.
	t.Setenv(envPublicURL, "from-the-environment.example")
	cfg, warnings, err = LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatalf("a bad %s gave %d warnings", envPublicURL, len(warnings))
	}
	if cfg.PublicURL != "https://from-the-file.example" {
		t.Fatalf("a bad environment value cost the file value: %q", cfg.PublicURL)
	}
}

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
		"127.0.0.1:8097": "http://127.0.0.1:8097",
		"not-an-address": "http://127.0.0.1:8080",
	} {
		if got := healthURL(Config{Listen: in}); got != want {
			t.Errorf("the health URL of %q is %q, want %q", in, got, want)
		}
	}
	if got := healthURL(Config{Listen: ":8443", TLSCert: "/a/cert"}); got != "https://127.0.0.1:8443" {
		t.Errorf("a server with a certificate gives %q", got)
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, "a long enough password") {
		t.Fatal("the hash holds the password")
	}
	if !CheckPassword("a long enough password", hash) {
		t.Fatal("the right password was refused")
	}
	if CheckPassword("another password", hash) {
		t.Fatal("a wrong password was accepted")
	}
	if CheckPassword("", hash) {
		t.Fatal("an empty password was accepted")
	}
	if CheckPassword("a long enough password", "") {
		t.Fatal("an empty hash accepted a password")
	}
}

func TestPasswordLengthRules(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("a short password was accepted")
	}
	if _, err := HashPassword(strings.Repeat("x", 73)); err == nil {
		// bcrypt drops everything after 72 bytes, so a longer value must be an
		// error and not a password that is quietly cut.
		t.Fatal("a password of 73 bytes was accepted")
	}
}

func TestGeneratePassword(t *testing.T) {
	first := GeneratePassword()
	if len(first) != generatedPasswordLength {
		t.Fatalf("the password is %d characters long", len(first))
	}
	if first == GeneratePassword() {
		t.Fatal("two generated passwords are the same")
	}
	for _, c := range first {
		if strings.ContainsRune("0Ol1I", c) {
			t.Fatalf("the password %q holds the character %q, which a person reads wrongly", first, c)
		}
	}
	// The generated password must pass the rules of the server itself.
	if _, err := HashPassword(first); err != nil {
		t.Fatalf("the generated password is not acceptable: %v", err)
	}
}

func TestAllowedHosts(t *testing.T) {
	hosts := AllowedHosts(Config{Listen: ":8097", PublicURL: "https://signage.example.com:8443"})
	want := []string{"localhost", "127.0.0.1", "localhost:8097", "signage.example.com:8443", "signage.example.com"}
	for _, name := range want {
		if !has(hosts, name) {
			t.Fatalf("%q is not in the allowlist %v", name, hosts)
		}
	}
	// A name that nobody configured must not be there.
	if has(hosts, "evil.example.com") {
		t.Fatal("the allowlist holds a name that nobody configured")
	}

	// With no public URL the list still holds the loopback names, so a fresh
	// server is reachable from the host that runs it and from nowhere else.
	bare := AllowedHosts(Config{Listen: ":8080"})
	if !has(bare, "localhost:8080") || has(bare, "signage.example.com") {
		t.Fatalf("the bare allowlist is %v", bare)
	}
}

func has(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestListenPort(t *testing.T) {
	for in, want := range map[string]string{
		":8080":           "8080",
		"127.0.0.1:8097":  "8097",
		"[::1]:8080":      "8080",
		"":                "",
		"not-an-address":  "",
		"127.0.0.1:https": "",
	} {
		if got := listenPort(in); got != want {
			t.Errorf("the port of %q is %q, want %q", in, got, want)
		}
	}
}
