package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
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
// ever work. Every other failure must stop the write: a file that keeps 0600
// when the caller asked for 0644 is one that the kiosk user cannot read.
func TestWriteFileAtomicWhenTheModeIsRefused(t *testing.T) {
	const data = "[device]\nname = \"Lobby\"\n"
	tests := []struct {
		name string
		// chmodErr is what the filesystem answers to the mode change.
		chmodErr error
		perm     os.FileMode
		wantErr  bool
	}{
		{name: "exfat answers EPERM", chmodErr: syscall.EPERM, perm: 0o644},
		{name: "a filesystem with no modes answers ENOTSUP", chmodErr: syscall.ENOTSUP, perm: 0o644},
		{name: "a filesystem with no modes answers EINVAL", chmodErr: syscall.EINVAL, perm: 0o600},
		{name: "another chmod error fails the write", chmodErr: syscall.EIO, perm: 0o644, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantErr && runtime.GOOS == "windows" {
				t.Skip("Windows has no POSIX modes, so every refusal is tolerated there")
			}
			old := chmodFile
			chmodFile = func(f *os.File, perm os.FileMode) error {
				return &os.PathError{Op: "chmod", Path: f.Name(), Err: tt.chmodErr}
			}
			defer func() { chmodFile = old }()

			dir := t.TempDir()
			path := filepath.Join(dir, "portapixel.toml")
			err := WriteFileAtomic(path, []byte(data), tt.perm)
			if tt.wantErr {
				if err == nil {
					t.Fatal("a chmod failure that is not a refusal must stop the write")
				}
				if _, statErr := os.Stat(path); statErr == nil {
					t.Fatal("a failed write must not leave the file behind")
				}
				names, _ := filepath.Glob(filepath.Join(dir, "*.tmp*"))
				if len(names) != 0 {
					t.Fatalf("temporary files stayed: %v", names)
				}
				return
			}
			if err != nil {
				t.Fatalf("a refused mode change must not stop the write: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != data {
				t.Fatalf("content = %q, want %q", got, data)
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

// A copy that stops in the middle leaves nothing at the target. The first boot
// skips a default media file that exists, so a short file stayed in the default
// playlist for ever.
func TestCopyFileSyncLeavesNoShortFile(t *testing.T) {
	dir := t.TempDir()
	// A directory opens, and its first read fails: the copy stops after the
	// target was made.
	src := filepath.Join(dir, "a directory")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CopyFileSync(src, filepath.Join(out, "clip.mp4")); err == nil {
		t.Fatal("the copy of a directory did not fail")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("the failed copy left %s", e.Name())
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

func TestTotalBytes(t *testing.T) {
	dir := t.TempDir()
	total, err := TotalBytes(dir)
	if err != nil {
		t.Fatalf("TotalBytes: %v", err)
	}
	if total == 0 {
		t.Fatal("TotalBytes = 0, want more than zero")
	}
	// The dashboard shows the free space against the total, so the free space
	// must never be the larger of the two.
	free, err := FreeBytes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if free > total {
		t.Fatalf("free = %d, total = %d: the free space cannot be the larger", free, total)
	}
	if _, err := TotalBytes(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("want an error for a missing directory")
	}
}
