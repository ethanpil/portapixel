package main

import (
	"reflect"
	"testing"
)

// setEnv sets the three variables of the environment for one test.
func setEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envPublicURL, "https://from-the-environment.example")
	t.Setenv(envListen, "127.0.0.1:9999")
	t.Setenv(envTrustedProxies, "10.9.9.9")
}

// TestTheFirstRunReadsTheEnvironment covers the Docker quick start.
//
// With no server.toml, LoadConfig gave the defaults before it read the environment.
// The first process then answered 421 for the public URL of the compose file, and an
// enrollment token of that run carried the example address.
func TestTheFirstRunReadsTheEnvironment(t *testing.T) {
	setEnv(t)
	dir := t.TempDir() + "/data"

	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://from-the-environment.example" || cfg.Listen != "127.0.0.1:9999" ||
		!reflect.DeepEqual(cfg.TrustedProxies, []string{"10.9.9.9"}) {
		t.Fatalf("the first run ignored the environment: %+v", cfg)
	}
	file, _, err := readFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if file.PublicURL != "" || file.Listen != defaultListen || len(file.TrustedProxies) != 0 {
		t.Fatalf("the first run wrote the environment into the file: %+v", file)
	}
}

// TestASaveKeepsTheEnvironmentOutOfTheFile covers each write of server.toml.
//
// A save wrote the configuration that runs, with the environment and the --listen
// flag in it. A PORTAPIXEL_TRUSTED_PROXIES that a person removed later then stayed in
// the file, and the server still believed that proxy.
func TestASaveKeepsTheEnvironmentOutOfTheFile(t *testing.T) {
	setEnv(t)
	dir := t.TempDir()

	// The first run makes the password and saves its hash.
	srv, err := newServer(dir, ":7777")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if err := srv.setPassword("a new admin password"); err != nil {
		t.Fatal(err)
	}
	if err := srv.saveSettings(srv.settings()); err != nil {
		t.Fatal(err)
	}

	file, _, err := readFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if file.PublicURL != "" || file.Listen != defaultListen || len(file.TrustedProxies) != 0 {
		t.Fatalf("a save wrote the environment or the flag into the file: %+v", file)
	}
	if !CheckPassword("a new admin password", file.AdminPasswordHash) {
		t.Fatal("the file does not hold the new password")
	}
	if !CheckPassword("a new admin password", srv.config().AdminPasswordHash) {
		t.Fatal("the server that runs does not take the new password")
	}
}
