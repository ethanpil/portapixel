package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	tests := []struct {
		name   string
		before string // content to put in the file first; "" writes no file
		data   string
	}{
		{name: "new file", data: "hello"},
		{name: "replace a file", before: "old content", data: "new content"},
		{name: "empty data", before: "old content", data: ""},
		{name: "binary data", data: "\x00\x01\x02\xff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "portapixel.toml")
			if tt.before != "" {
				if err := os.WriteFile(path, []byte(tt.before), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := WriteFileAtomic(path, []byte(tt.data), 0o644); err != nil {
				t.Fatalf("WriteFileAtomic: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.data {
				t.Fatalf("content = %q, want %q", got, tt.data)
			}
			// The temporary file must be gone.
			names, err := filepath.Glob(filepath.Join(dir, "*.tmp*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(names) != 0 {
				t.Fatalf("temporary files stayed: %v", names)
			}
		})
	}
}

func TestWriteFileAtomicKeepsTheOldFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "portapixel.toml")
	if err := WriteFileAtomic(path, []byte("x"), 0o644); err == nil {
		t.Fatal("want an error when the directory is missing")
	}
}

func TestWriteFileAtomicMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX file modes")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := WriteFileAtomic(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

// TestWriteFileAtomicWhenTheModeIsRefused covers the filesystem that has no file
// modes. PPMEDIA is exFAT: it answers "operation not permitted" to a mode
// change. The write must still finish, or no save on the media partition can
// ever work.
func TestWriteFileAtomicWhenTheModeIsRefused(t *testing.T) {
	old := chmodFile
	chmodFile = func(f *os.File, perm os.FileMode) error { return os.ErrPermission }
	defer func() { chmodFile = old }()

	tests := []struct {
		name string
		perm os.FileMode
		data string
	}{
		{name: "the configuration file", perm: 0o644, data: "[device]\nname = \"Lobby\"\n"},
		{name: "a file that holds a secret", perm: 0o600, data: "psk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "portapixel.toml")
			if err := WriteFileAtomic(path, []byte(tt.data), tt.perm); err != nil {
				t.Fatalf("a refused mode change must not stop the write: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.data {
				t.Fatalf("content = %q, want %q", got, tt.data)
			}
			if runtime.GOOS == "windows" {
				return
			}
			// The file that stays keeps the mode of the temporary file, which is
			// the strict one, never a wider one.
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
			}
		})
	}
}

func TestCopyFileSync(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	want := "some media bytes"
	if err := os.WriteFile(src, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CopyFileSync(src, dst); err != nil {
		t.Fatalf("CopyFileSync: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if err := CopyFileSync(filepath.Join(dir, "missing"), dst); err == nil {
		t.Fatal("want an error for a missing source")
	}
}

func TestFreeBytes(t *testing.T) {
	got, err := FreeBytes(t.TempDir())
	if err != nil {
		t.Fatalf("FreeBytes: %v", err)
	}
	if got == 0 {
		t.Fatal("FreeBytes = 0, want more than zero")
	}
	if _, err := FreeBytes(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("want an error for a missing directory")
	}
}
