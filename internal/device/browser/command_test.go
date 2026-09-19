package browser

import (
	"strings"
	"testing"
)

func TestBuildDefaultCommand(t *testing.T) {
	cfg := CommandConfig{
		CacheDir:   "/var/cache/kiosk",
		RuntimeDir: "/run/user/1300",
		DebugPort:  9222,
	}
	cmd, err := cfg.Build("http://127.0.0.1/player?k=abc")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(cmd.Args, " ")

	// The flags of ARCHITECTURE section 7. A missing flag here is a black screen
	// or a browser that writes to the flash, so each one is checked.
	for _, want := range []string{
		"cage", "-s", "--", "chromium",
		"--kiosk",
		"--ozone-platform=wayland",
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=9222",
		"--autoplay-policy=no-user-gesture-required",
		"--user-data-dir=/var/cache/kiosk/profile",
		"--disk-cache-dir=/var/cache/kiosk/cache",
		"--no-first-run",
		"--noerrdialogs",
		"--disable-infobars",
		"--disable-session-crashed-bubble",
		"--disable-features=Translate",
		"--password-store=basic",
		"http://127.0.0.1/player?k=abc",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the command line has no %q:\n%s", want, line)
		}
	}
	// The URL must be the last argument: cage gives everything after it to
	// Chromium, and Chromium takes the last argument as the page.
	if cmd.Args[len(cmd.Args)-1] != "http://127.0.0.1/player?k=abc" {
		t.Errorf("the URL is not the last argument: %v", cmd.Args)
	}

	env := strings.Join(cmd.Env, " ")
	for _, want := range []string{
		"XDG_RUNTIME_DIR=/run/user/1300",
		"WLR_LIBINPUT_NO_DEVICES=1",
		"HOME=/var/cache/kiosk/home",
		"WAYLAND_DISPLAY=" + WaylandDisplay,
	} {
		if !strings.Contains(env, want) {
			t.Errorf("the environment has no %q: %v", want, cmd.Env)
		}
	}
}

func TestBuildOverride(t *testing.T) {
	tests := []struct {
		name     string
		override string
		want     []string
	}{
		{"the URL goes at the end", "echo hello", []string{"echo", "hello", "http://x/"}},
		{"the placeholder takes the URL", "echo a %u b", []string{"echo", "a", "http://x/", "b"}},
		{"quotation marks keep a space", `"my browser" --flag`, []string{"my browser", "--flag", "http://x/"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := CommandConfig{Override: tt.override}.Build("http://x/")
			if err != nil {
				t.Fatal(err)
			}
			if len(cmd.Args) != len(tt.want) {
				t.Fatalf("args = %v, want %v", cmd.Args, tt.want)
			}
			for i := range tt.want {
				if cmd.Args[i] != tt.want[i] {
					t.Fatalf("args = %v, want %v", cmd.Args, tt.want)
				}
			}
		})
	}
}

func TestBuildDisabled(t *testing.T) {
	cfg := CommandConfig{Override: DisableCommand}
	if !cfg.Disabled() {
		t.Fatal("Disabled is false for the override none")
	}
	if _, err := cfg.Build("http://x/"); err == nil {
		t.Fatal("Build gave a command for a switched off browser")
	}
}

func TestEndpoint(t *testing.T) {
	if got := (CommandConfig{}).Endpoint(); got != "http://127.0.0.1:9222" {
		t.Errorf("Endpoint = %q", got)
	}
	if got := (CommandConfig{DebugPort: 9333}).Endpoint(); got != "http://127.0.0.1:9333" {
		t.Errorf("Endpoint = %q", got)
	}
}

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"a b c", []string{"a", "b", "c"}},
		{"  a   b  ", []string{"a", "b"}},
		{`"a b" c`, []string{"a b", "c"}},
		{`'a b' c`, []string{"a b", "c"}},
		{`""`, []string{""}},
		{"", nil},
	}
	for _, tt := range tests {
		got := splitArgs(tt.in)
		if len(got) != len(tt.want) {
			t.Fatalf("splitArgs(%q) = %v, want %v", tt.in, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Fatalf("splitArgs(%q) = %v, want %v", tt.in, got, tt.want)
			}
		}
	}
}

func TestTransformName(t *testing.T) {
	for rotation, want := range map[int]string{0: "normal", 90: "90", 180: "180", 270: "270", 45: "normal"} {
		if got := transformName(rotation); got != want {
			t.Errorf("transformName(%d) = %q, want %q", rotation, got, want)
		}
	}
}

func TestClockMinutes(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"03:30", 210, true},
		{"00:00", 0, true},
		{"23:59", 1439, true},
		{"24:00", 0, false},
		{"3:30", 0, false},
		{"", 0, false},
		{"ab:cd", 0, false},
	}
	for _, tt := range tests {
		got, ok := clockMinutes(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("clockMinutes(%q) = %d, %v", tt.in, got, ok)
		}
	}
}
