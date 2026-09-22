package browser

import (
	"context"
	"os"
	"path/filepath"
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
		// Without this flag Chromium 111 and later refuse the DevTools socket of
		// our own client, and rung 1 fails at the first navigation.
		"--remote-allow-origins=http://127.0.0.1:9222",
		"--autoplay-policy=no-user-gesture-required",
		"--user-data-dir=/var/cache/kiosk/profile",
		"--disk-cache-dir=/var/cache/kiosk/cache",
		"--no-first-run",
		"--noerrdialogs",
		"--disable-infobars",
		"--disable-session-crashed-bubble",
		// BackForwardCache is in the list because the browser kept the page of
		// each URL item in memory. Without the name, three URL items made three
		// more renderer processes (QEMU, 2026-09-22).
		"--disable-features=Translate,OptimizationHints,NetworkTimeServiceQuerying,BackForwardCache",
		"--password-store=basic",
		// An unattended appliance must not talk to a server that its owner did
		// not name, and must not do the background work of a desktop browser.
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-domain-reliability",
		"--metrics-recording-only",
		"--disable-sync",
		"--disable-default-apps",
		"--no-default-browser-check",
		"--disable-breakpad",
		"--gcm-checkin-url=http://127.0.0.1:1/",
		"--gcm-registration-url=http://127.0.0.1:1/",
		"--gcm-mcs-endpoint=http://127.0.0.1:1/",
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

	// Chromium keeps the LAST --disable-features and drops the others, so a
	// second one would silently turn Translate back on.
	if n := strings.Count(line, "--disable-features="); n != 1 {
		t.Errorf("the command line has %d --disable-features flags, want 1:\n%s", n, line)
	}

	// The sandbox is the reason that the browser runs as the kiosk user. No flag
	// may take it away.
	for _, never := range []string{"--no-sandbox", "--disable-gpu-sandbox", "--disable-setuid-sandbox"} {
		if strings.Contains(line, never) {
			t.Errorf("the command line holds %q:\n%s", never, line)
		}
	}

	env := strings.Join(cmd.Env, " ")
	for _, want := range []string{
		"XDG_RUNTIME_DIR=/run/user/1300",
		"WLR_LIBINPUT_NO_DEVICES=1",
		"HOME=/var/cache/kiosk/home",
		// The transparent cursor theme. Without it, cage draws a pointer in the
		// middle of the screen and the picture is not clean.
		"XCURSOR_THEME=portapixel-blank",
		"XCURSOR_PATH=/usr/share/icons",
		"XCURSOR_SIZE=24",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("the environment has no %q: %v", want, cmd.Env)
		}
	}
	// cage MUST NOT see WAYLAND_DISPLAY. wlroots then looks for a parent
	// compositor, finds none, and the screen stays black.
	if strings.Contains(env, "WAYLAND_DISPLAY=") {
		t.Errorf("the environment of cage holds WAYLAND_DISPLAY: %v", cmd.Env)
	}
}

// A program that joins the session from outside must find the socket.
func TestToolEnvironmentHasTheWaylandDisplay(t *testing.T) {
	cfg := CommandConfig{CacheDir: "/var/cache/kiosk", RuntimeDir: "/run/user/1300"}
	cmd, err := cfg.Tool(context.Background(), "wlr-randr", "--output", "HDMI-A-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(cmd.Env, " "), "WAYLAND_DISPLAY="+WaylandDisplay) {
		t.Errorf("a tool has no WAYLAND_DISPLAY: %v", cmd.Env)
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

// The browser log must stay on the capped tmpfs. A development machine has no
// cache directory and gets no file at all.
func TestLogPath(t *testing.T) {
	if got := (CommandConfig{CacheDir: "/var/cache/kiosk"}).LogPath(); got != "/var/cache/kiosk/browser.log" {
		t.Errorf("LogPath = %q", got)
	}
	if got := (CommandConfig{}).LogPath(); got != "" {
		t.Errorf("LogPath with no cache directory = %q", got)
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

// firstOutput reads the text of another program, so it must never stop the
// daemon. A line of whitespace that is neither a space nor a tab passes the
// first-character guard and gives no fields at all.
func TestFirstOutput(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
		ok   bool
	}{
		{"the first output wins", "HDMI-A-1 \"Acme 27\"\n  Make: Acme\nDP-1 \"Other\"\n", "HDMI-A-1", true},
		{"indented lines are skipped", "  Make: Acme\n\tMode: 1920x1080\nHDMI-A-2\n", "HDMI-A-2", true},
		{"a carriage return alone is not an output", "\r\nHDMI-A-3\n", "HDMI-A-3", true},
		{"a vertical tab alone is not an output", "\v\nHDMI-A-4\n", "HDMI-A-4", true},
		{"a no-break space alone is not an output", " \nHDMI-A-5\n", "HDMI-A-5", true},
		{"no output at all", "\v\n\f\n", "", false},
		{"nothing", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := firstOutput(tt.text)
			if (err == nil) != tt.ok {
				t.Fatalf("firstOutput(%q) error = %v, want ok=%v", tt.text, err, tt.ok)
			}
			if got != tt.want {
				t.Errorf("firstOutput(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"http://127.0.0.1:8099/player?k=abc123", "http://127.0.0.1:8099/player?k=REDACTED"},
		{"http://127.0.0.1:8099/player?k=abc123&resume=3", "http://127.0.0.1:8099/player?k=REDACTED&resume=3"},
		{"http://127.0.0.1:8099/player?resume=3&k=abc123", "http://127.0.0.1:8099/player?resume=3&k=REDACTED"},
		{"https://dash.example.com/board", "https://dash.example.com/board"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := RedactURL(tt.in); got != tt.want {
			t.Errorf("RedactURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if strings.Contains(RedactURL(tt.in), "abc123") {
			t.Errorf("RedactURL(%q) still holds the secret", tt.in)
		}
	}
}

// waitForCompositor must not run wlr-randr before cage made its socket: the
// rotation would be lost until the next launch.
func TestWaitForCompositor(t *testing.T) {
	t.Run("no runtime directory means no wait", func(t *testing.T) {
		if err := waitForCompositor(context.Background(), ""); err != nil {
			t.Fatalf("waitForCompositor with no runtime directory = %v", err)
		}
	})
	t.Run("the socket is there", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, WaylandDisplay), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := waitForCompositor(context.Background(), dir); err != nil {
			t.Fatalf("waitForCompositor = %v", err)
		}
	})
	t.Run("a context that ends stops the wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := waitForCompositor(ctx, t.TempDir()); err == nil {
			t.Fatal("waitForCompositor waited for a socket that will never come")
		}
	})
}
