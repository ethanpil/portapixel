package version

import (
	"runtime"
	"testing"
)

func TestVersionDefault(t *testing.T) {
	if Version == "" {
		t.Fatal("Version is empty")
	}
}

func TestArch(t *testing.T) {
	got := Arch()
	if got != runtime.GOARCH {
		t.Fatalf("Arch() = %q, want %q", got, runtime.GOARCH)
	}
	// The release builds make only these two architectures.
	switch got {
	case "amd64", "arm64":
	default:
		t.Logf("this test machine is %q, which is not a release architecture", got)
	}
}
