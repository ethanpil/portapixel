package store

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "a plain name stays", in: "welcome.jpg", want: "welcome.jpg"},
		{name: "spaces become hyphens", in: "my holiday photo.png", want: "my-holiday-photo.png"},
		{name: "runs of bad characters join", in: "a   b???c.mp4", want: "a-b-c.mp4"},
		{name: "a path keeps the last element", in: "media/sub/clip.mp4", want: "clip.mp4"},
		{name: "a windows path keeps the last element", in: `C:\media\clip.mp4`, want: "clip.mp4"},
		{name: "a parent reference cannot survive", in: "../../etc/passwd", want: "passwd"},
		{name: "a name that is only an extension gets a stem", in: ".profile", want: "object.profile"},
		{name: "letters of other alphabets go away", in: "café.jpg", want: "caf.jpg"},
		{name: "an empty name gets a default", in: "", want: "object"},
		{name: "a name of bad characters only gets a default", in: "???", want: "object"},
		{name: "an extension with no stem gets a default", in: ".jpg", want: "object.jpg"},
		{name: "underscores stay", in: "logo_2026.svg", want: "logo_2026.svg"},
		{name: "a long extension is part of the name", in: "file.verylongextension", want: "file.verylongextension"},
		{
			name: "a long name is cut and keeps the extension",
			in:   strings.Repeat("a", 200) + ".mp4",
			want: strings.Repeat("a", maxNameLen-4) + ".mp4",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SafeName(tt.in)
			if got != tt.want {
				t.Fatalf("SafeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if len(got) > maxNameLen {
				t.Fatalf("SafeName(%q) is %d characters long, want at most %d", tt.in, len(got), maxNameLen)
			}
		})
	}
}

func TestObjectName(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	tests := []struct {
		name     string
		sha      string
		origName string
		want     string
		wantErr  bool
	}{
		{name: "full hash", sha: sha, origName: "welcome.jpg", want: "abababab-welcome.jpg"},
		{name: "bad name", sha: sha, origName: "../a b.mp4", want: "abababab-a-b.mp4"},

		// The sha comes from the fleet manifest. Every one of these would put
		// the object somewhere else, or would name it after nothing.
		{name: "a parent step", sha: "../../..", origName: "a.jpg", wantErr: true},
		{name: "a parent step in a full-length value", sha: strings.Repeat("../", 21) + "x", origName: "a.jpg", wantErr: true},
		{name: "a path separator", sha: strings.Repeat("a", 32) + "/" + strings.Repeat("b", 31), origName: "a.jpg", wantErr: true},
		{name: "upper case", sha: strings.ToUpper(sha), origName: "a.png", wantErr: true},
		{name: "too short", sha: "abcd", origName: "a.png", wantErr: true},
		{name: "one character short", sha: sha[:63], origName: "a.png", wantErr: true},
		{name: "one character too long", sha: sha + "a", origName: "a.png", wantErr: true},
		{name: "empty", sha: "", origName: "a.png", wantErr: true},
		{name: "a letter that is not hex", sha: strings.Repeat("ag", 32), origName: "a.png", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ObjectName(tt.sha, tt.origName)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ObjectName(%q, %q) = %q, want an error", tt.sha, tt.origName, got)
				}
				if got != "" {
					t.Fatalf("a refused sha gave the name %q, want an empty name", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ObjectName(%q, %q): %v", tt.sha, tt.origName, err)
			}
			if got != tt.want {
				t.Fatalf("ObjectName(%q, %q) = %q, want %q", tt.sha, tt.origName, got, tt.want)
			}
		})
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		content string
	}{
		{name: "empty file", content: ""},
		{name: "small file", content: "hello"},
		{name: "binary file", content: "\x00\x01\x02\xff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, "f")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := HashFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(tt.content))
			if want := hex.EncodeToString(sum[:]); got != want {
				t.Fatalf("HashFile = %q, want %q", got, want)
			}
		})
	}

	if _, err := HashFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("want an error for a missing file")
	}
}
