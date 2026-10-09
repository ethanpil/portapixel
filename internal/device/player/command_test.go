package player

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ethanpil/portapixel/internal/config"
)

func TestArgs(t *testing.T) {
	c := CommandConfig{RunDir: "/run/portapixel"}

	drm := c.Args(Launch{Output: OutputDRM})
	for _, want := range []string{
		"--no-config", "--profile=fast", "--idle=yes", "--force-window=yes", "--keep-open=yes",
		"--loop-playlist=inf", "--prefetch-playlist=yes", "--hwdec=auto-safe", "--ao=alsa", "--vo=drm",
		"--gpu-shader-cache=no",
		"--input-ipc-server=" + filepath.Join("/run/portapixel", "player", "mpv.sock"),
		"--script=" + filepath.Join("/run/portapixel", "transitions.lua"),
	} {
		if !slices.Contains(drm, want) {
			t.Errorf("the drm arguments do not hold %q: %q", want, drm)
		}
	}
	for _, a := range drm {
		if strings.HasPrefix(a, "--video-rotate") || strings.HasPrefix(a, "--drm-mode") || strings.HasPrefix(a, "--gpu-context") {
			t.Errorf("unexpected argument %q", a)
		}
	}
	// --profile=fast comes before the options that it could change.
	if slices.Index(drm, "--profile=fast") != 1 {
		t.Errorf("--profile=fast is not the second argument: %q", drm)
	}

	gpu := c.Args(Launch{Output: OutputGPU, Rotation: 90, VideoMode: "1920x1080@60"})
	for _, want := range []string{"--vo=gpu", "--gpu-context=drm", "--video-rotate=90", "--drm-mode=1920x1080@60"} {
		if !slices.Contains(gpu, want) {
			t.Errorf("the gpu arguments do not hold %q: %q", want, gpu)
		}
	}
}

func TestBuild(t *testing.T) {
	c := CommandConfig{RunDir: "/run/pp"}
	cmd, err := c.Build(Launch{Output: OutputDRM})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cmd.Path) != "mpv" && !strings.HasPrefix(filepath.Base(cmd.Path), "mpv") {
		t.Errorf("program = %q, want mpv", cmd.Path)
	}
	// The device command gets a minimal environment with HOME on the tmpfs.
	if !slices.Contains(cmd.Env, "HOME="+filepath.Join("/run/pp", "player")) ||
		!slices.Contains(cmd.Env, "MESA_SHADER_CACHE_DISABLE=true") || len(cmd.Env) != 3 {
		t.Errorf("env = %q", cmd.Env)
	}

	// An override replaces the program; its own arguments come last, so they win.
	c.Override = `"/opt/my mpv/mpv" --vo=gpu --gpu-context=x11egl`
	cmd, err = c.Build(Launch{Output: OutputDRM})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Args[0] != "/opt/my mpv/mpv" {
		t.Errorf("program = %q", cmd.Args[0])
	}
	if n := len(cmd.Args); cmd.Args[n-2] != "--vo=gpu" || cmd.Args[n-1] != "--gpu-context=x11egl" {
		t.Errorf("the override arguments are not last: %q", cmd.Args)
	}
	if cmd.Env != nil {
		t.Error("an override must keep the environment of the desktop")
	}

	c.Override = DisableCommand
	if _, err := c.Build(Launch{}); err == nil || !c.Disabled() {
		t.Error("the disabled player built a command")
	}
	c.Override = `""`
	if _, err := c.Build(Launch{}); err == nil {
		t.Error("an override with no program built a command")
	}
}

// Prepare writes the script from the binary, makes the socket directory and
// removes a socket that an earlier mpv left.
func TestPrepare(t *testing.T) {
	c := CommandConfig{RunDir: t.TempDir()}
	writeFile(t, c.SocketPath(), "old socket")
	writeFile(t, c.ScriptPath(), "old script")
	if err := c.Prepare(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.SocketPath()); !os.IsNotExist(err) {
		t.Errorf("the old socket is still there: %v", err)
	}
	data, err := os.ReadFile(c.ScriptPath())
	if err != nil || string(data) != string(TransitionScript()) {
		t.Errorf("the script in the run directory is not the embedded one: %v", err)
	}
	if info, err := os.Stat(filepath.Join(c.RunDir, SocketDirName)); err != nil || !info.IsDir() {
		t.Errorf("no socket directory: %v", err)
	}
}

// A Raspberry Pi decodes H.264 with v4l2m2m-copy, which auto-safe does not try.
// Every other board takes auto-safe. A Pi with that decoder also gets a smaller
// buffer count. The model comes from a fake device tree.
func TestHwdecFollowsTheBoard(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name, model, want, buffers string
	}{
		{"zero 2 w", "Raspberry Pi Zero 2 W Rev 1.0\x00", "v4l2m2m-copy", "num_capture_buffers=8"},
		{"pi 4", "Raspberry Pi 4 Model B Rev 1.5\x00", "v4l2m2m-copy", "num_capture_buffers=8"},
		{"pi 5", "Raspberry Pi 5 Model B Rev 1.0\x00", "v4l2m2m-copy", ""},
		{"compute module 5", "Raspberry Pi Compute Module 5 Rev 1.0\x00", "v4l2m2m-copy", ""},
		{"another arm board", "Pine64 RockPro64 v2.1\x00", "auto-safe", ""},
		{"a pc has no file", "", "auto-safe", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, "no-model")
			if tt.model != "" {
				path = filepath.Join(dir, tt.name)
				if err := os.WriteFile(path, []byte(tt.model), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			model := BoardModel(path)
			if strings.ContainsRune(model, 0) {
				t.Errorf("BoardModel kept the NUL byte: %q", model)
			}
			args := CommandConfig{RunDir: "/run/portapixel"}.Args(Launch{Output: OutputDRM, Model: model})
			if !slices.Contains(args, "--hwdec="+tt.want) {
				t.Errorf("model %q gives %q, want --hwdec=%s", model, args, tt.want)
			}
			if got := DecoderOptions(model); got != tt.buffers {
				t.Errorf("model %q gives the decoder options %q, want %q", model, got, tt.buffers)
			}
		})
	}
}

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"mpv", []string{"mpv"}},
		{"  mpv   --vo=x11  ", []string{"mpv", "--vo=x11"}},
		{`"C:\Program Files\mpv\mpv.exe" --fs`, []string{`C:\Program Files\mpv\mpv.exe`, "--fs"}},
		{`mpv '--title=a b'`, []string{"mpv", "--title=a b"}},
		{`""`, []string{""}},
		{"", nil},
	}
	for _, tt := range tests {
		if got := splitArgs(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("splitArgs(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The script is product code: the shipped kinds are there, it reports faults to
// the daemon, and no lab file is left in it.
func TestTransitionScript(t *testing.T) {
	s := string(TransitionScript())
	for _, want := range []string{
		`mp.add_hook("on_unload"`, `"playback-restart"`, "user-data/pptr/fault", "screenshot-raw",
		`["fade"]`, `["crossfade"]`, `["wipe-left"]`, `["push-down"]`, "DEADMAN = 5",
		`mp.add_hook("on_preloaded"`, "video-add", "user-data/pptr/motion", "user-data/pptr/moved",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the script does not hold %q", want)
		}
	}
	for _, bad := range []string{"/root/", "io.open", "pptr.log"} {
		if strings.Contains(s, bad) {
			t.Errorf("the script holds the lab leftover %q", bad)
		}
	}
}

// The script knows each word of config.Transitions except "cut", which needs no
// script work, and no other word.
func TestScriptKnowsEveryTransition(t *testing.T) {
	s := string(TransitionScript())
	block := s[strings.Index(s, "local KINDS = {"):]
	block = block[:strings.Index(block, "}")]
	var kinds []string
	for _, m := range regexp.MustCompile(`\["([a-z-]+)"\] = true`).FindAllStringSubmatch(block, -1) {
		kinds = append(kinds, m[1])
	}
	want := slices.DeleteFunc(slices.Clone(config.Transitions), func(w string) bool { return w == "cut" })
	slices.Sort(kinds)
	slices.Sort(want)
	if !slices.Equal(kinds, want) {
		t.Errorf("the script knows %v, want %v", kinds, want)
	}
}

func TestLadder(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	set := DefaultWatchdog()
	var l ladder
	for i := 1; i <= 3; i++ {
		if n, reboot := l.restarted(at.Add(time.Duration(i)*time.Minute), set); n != i || reboot {
			t.Fatalf("restart %d gave %d, %v", i, n, reboot)
		}
	}
	if n, reboot := l.restarted(at.Add(4*time.Minute), set); n != 4 || !reboot {
		t.Fatalf("the fourth restart gave %d, %v; want a reboot", n, reboot)
	}

	// The window forgets old restarts.
	l = ladder{}
	l.restarted(at, set)
	l.restarted(at.Add(time.Minute), set)
	l.restarted(at.Add(2*time.Minute), set)
	if n, reboot := l.restarted(at.Add(2*time.Hour), set); n != 1 || reboot {
		t.Fatalf("a restart two hours later gave %d, %v", n, reboot)
	}

	// A ladder that is off, and a limit of 0, never reboot.
	for _, s := range []WatchdogSettings{{RestartWindow: time.Hour, RestartsBeforeReboot: 4},
		{Enabled: true, RestartWindow: time.Hour}} {
		l = ladder{}
		for i := range 10 {
			if _, reboot := l.restarted(at.Add(time.Duration(i)*time.Second), s); reboot {
				t.Fatalf("settings %+v rebooted", s)
			}
		}
	}
}
