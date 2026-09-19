// Package updategate_test proves the A/B update path of plan section 15 with the
// real binary, a throwaway minisign key pair and the real health gate script.
//
// It is the smoke-update gate of plan section 17 item 6. It needs no previous
// published release: it builds one. The first release of the project therefore
// gets the same proof as every later release.
//
// What it proves:
//   - a release that a key signs is installed, and current points at it;
//   - the daemon writes health/<VERSION>.ok, and the gate then passes;
//   - a release that writes no marker is rolled back, marked bad and restarted;
//   - an unsigned release, a release with another key, a legacy signature and a
//     changed binary are all refused BEFORE the symlink flip (D47, plan 18).
//
// The test is off by default. It builds binaries and runs shell scripts, so it
// must not slow a normal "go test ./...". Set PP_CI_UPDATE_GATE=1 to run it.
package updategate_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"aead.dev/minisign"

	"github.com/ethanpil/portapixel/internal/sigverify"
	"github.com/ethanpil/portapixel/internal/updater"
)

// The two releases of the test. The old one runs, the new one is the update.
const (
	oldVersion = "0.0.1"
	newVersion = "0.0.2"
)

// gateScript is where health-gate.sh looks for its helper. The path is absolute
// in the script, because on a device the script is at that path. The CI job puts
// the overlay there before it runs this test.
const oplogPath = "/usr/libexec/portapixel/oplog.sh"

func skipUnlessEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("PP_CI_UPDATE_GATE") == "" {
		t.Skip("set PP_CI_UPDATE_GATE=1 to run the update gate")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the health gate is a POSIX shell script; it runs on Linux")
	}
}

// ---------------------------------------------------------------- the key pair

// signer is a throwaway minisign key pair. The release key of the project is a
// repository secret that CI must not hold, so the gate makes its own key, builds
// a binary that carries the public half, and signs with the secret half. The
// proof is the same: a binary accepts only what its own key signed.
type signer struct {
	public minisign.PublicKey
	secret minisign.PrivateKey
}

func newSigner(t *testing.T) signer {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("make a key pair: %v", err)
	}
	return signer{public: pub, secret: priv}
}

// publicText is the key in the one line form that -X writes into the binary.
func (s signer) publicText(t *testing.T) string {
	t.Helper()
	text, err := s.public.MarshalText()
	if err != nil {
		t.Fatalf("write the public key: %v", err)
	}
	return string(text)
}

// sign makes a PREHASHED signature, which is the only kind that
// internal/sigverify accepts.
func (s signer) sign(t *testing.T, body []byte) []byte {
	t.Helper()
	r := minisign.NewReader(bytes.NewReader(body))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatalf("hash the file: %v", err)
	}
	return r.Sign(s.secret)
}

// signLegacy makes a signature of the file itself. The verifier must refuse it,
// because a check would have to hold the whole binary in memory.
func (s signer) signLegacy(t *testing.T, body []byte) []byte {
	t.Helper()
	return minisign.Sign(s.secret, body)
}

// ------------------------------------------------------------------ the builds

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("cannot find the module root at %s: %v", dir, err)
	}
	return dir
}

// build makes a portapixeld with a version and a public key in it. This is the
// same command line that the release workflow runs.
func build(t *testing.T, repo, version, publicKey, out string) {
	t.Helper()
	ldflags := fmt.Sprintf("-s -w -X github.com/ethanpil/portapixel/internal/version.Version=%s"+
		" -X github.com/ethanpil/portapixel/internal/version.PublicKey=%s", version, publicKey)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", out, "./cmd/portapixeld")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if outText, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, outText)
	}
}

// ------------------------------------------------------------------ the release

type bundle struct {
	asset  string
	binary []byte
	sig    []byte
	sums   []byte
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// newBundle makes the three release files in the shape that the release workflow
// writes and internal/updater reads.
func newBundle(t *testing.T, asset string, binary, sig []byte) bundle {
	t.Helper()
	return bundle{
		asset:  asset,
		binary: binary,
		sig:    sig,
		sums:   []byte(sha256Hex(binary) + "  " + asset + "\n"),
	}
}

// serve answers the three files of a release. A nil signature answers 404, which
// is what an unsigned release looks like to a device.
func (b bundle) serve(t *testing.T) updater.Release {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+b.asset, func(w http.ResponseWriter, r *http.Request) { w.Write(b.binary) })
	mux.HandleFunc("/"+b.asset+updater.SigSuffix, func(w http.ResponseWriter, r *http.Request) {
		if b.sig == nil {
			http.NotFound(w, r)
			return
		}
		w.Write(b.sig)
	})
	mux.HandleFunc("/"+updater.SumsName, func(w http.ResponseWriter, r *http.Request) { w.Write(b.sums) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return updater.Release{
		Version:   newVersion,
		Source:    "github",
		BinaryURL: server.URL + "/" + b.asset,
		SigURL:    server.URL + "/" + b.asset + updater.SigSuffix,
		SumsURL:   server.URL + "/" + updater.SumsName,
	}
}

// ------------------------------------------------------------- the release root

// releaseRoot builds the layout of a device that runs oldVersion:
//
//	<root>/releases/0.0.1/portapixeld
//	<root>/current -> releases/0.0.1
//	<root>/health/
func releaseRoot(t *testing.T, oldBinary string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, updater.ReleasesDir, oldVersion)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, updater.HealthDir), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(oldBinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "portapixeld"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(updater.ReleasesDir, oldVersion), filepath.Join(root, updater.CurrentLink)); err != nil {
		t.Fatal(err)
	}
	return root
}

func currentTarget(t *testing.T, root string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(root, updater.CurrentLink))
	if err != nil {
		t.Fatalf("read the current link: %v", err)
	}
	return target
}

// manager makes a Manager for a release root. binaryVersion nil keeps the
// PRODUCTION path, which runs the staged binary and reads what it prints.
func manager(t *testing.T, root, publicKey string, binaryVersion func(string) (string, error)) *updater.Manager {
	t.Helper()
	return updater.New(updater.Options{
		Root:          root,
		BinaryName:    "portapixeld",
		Arch:          runtime.GOARCH,
		Running:       oldVersion,
		PublicKey:     publicKey,
		BinaryVersion: binaryVersion,
		Restart:       func() error { return nil },
		Log:           func(event, details string) { t.Logf("oplog %s %s", event, details) },
	})
}

// ------------------------------------------------------------------- the tests

// TestSwapAndHealthGate is the main path: sign, install, swap, health marker,
// rollback.
func TestSwapAndHealthGate(t *testing.T) {
	skipUnlessEnabled(t)
	repo := repoRoot(t)
	key := newSigner(t)
	publicKey := key.publicText(t)

	work := t.TempDir()
	oldBinary := filepath.Join(work, "portapixeld-old")
	newBinary := filepath.Join(work, "portapixeld-new")
	build(t, repo, oldVersion, publicKey, oldBinary)
	build(t, repo, newVersion, publicKey, newBinary)

	body, err := os.ReadFile(newBinary)
	if err != nil {
		t.Fatal(err)
	}
	asset := "portapixeld-" + runtime.GOARCH
	good := newBundle(t, asset, body, key.sign(t, body))

	// ---------------------------------------------------------------- 1. the flip
	// The PRODUCTION version reader runs here: Options.BinaryVersion is nil, so
	// the updater runs the staged binary and reads the version that it prints.
	// This is the cross-check of final-review item 19 and it must work with the
	// real binary, not only with a test double.
	t.Run("production version reader", func(t *testing.T) {
		root := releaseRoot(t, oldBinary)
		m := manager(t, root, publicKey, nil)
		rel := good.serve(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.Apply(ctx, rel); err != nil {
			t.Errorf("Apply with the production version reader failed: %v\n"+
				"The binary prints \"portapixeld <version> <arch>\" and internal/updater\n"+
				"readVersion takes the LAST field, so it reads the architecture.", err)
			return
		}
		if got, want := currentTarget(t, root), updater.ReleasesDir+"/"+newVersion; got != want {
			t.Errorf("current points at %q, want %q", got, want)
		}
	})

	// From here the test gives the version reader that the release workflow
	// proves separately (the second field of "portapixeld <version> <arch>"), so
	// that one fault cannot hide the rest of the path.
	readVersion := func(path string) (string, error) {
		out, err := exec.Command(path, "version").Output()
		if err != nil {
			return "", err
		}
		fields := strings.Fields(string(out))
		if len(fields) < 2 {
			return "", errors.New("the binary printed no version")
		}
		return fields[1], nil
	}

	t.Run("swap then health marker", func(t *testing.T) {
		root := releaseRoot(t, oldBinary)
		m := manager(t, root, publicKey, readVersion)
		rel := good.serve(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.Apply(ctx, rel); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if got, want := currentTarget(t, root), updater.ReleasesDir+"/"+newVersion; got != want {
			t.Fatalf("current points at %q, want %q", got, want)
		}
		previous, err := os.Readlink(filepath.Join(root, updater.PreviousLink))
		if err != nil || previous != updater.ReleasesDir+"/"+oldVersion {
			t.Fatalf("previous is %q (%v), want %s/%s", previous, err, updater.ReleasesDir, oldVersion)
		}
		pending, err := os.ReadFile(filepath.Join(root, updater.PendingFile))
		if err != nil {
			t.Fatalf("read the pending marker: %v", err)
		}
		if strings.TrimSpace(string(pending)) != newVersion {
			t.Fatalf("the pending marker holds %q, want %q", pending, newVersion)
		}

		// The daemon of the new release writes the marker. Run the real daemon
		// with the browser off and prove the NAME of the marker, because a name
		// that does not match rolls a good release back for ever (item 19).
		marker := runDaemonUntilMarker(t, filepath.Join(root, updater.ReleasesDir, newVersion, "portapixeld"), root)
		if got, want := filepath.Base(marker), newVersion+updater.OKSuffix; got != want {
			t.Fatalf("the daemon wrote %q, and the gate waits for %q", got, want)
		}

		// The gate must pass now.
		rc, log := runHealthGate(t, root, 20)
		if rc != 0 {
			t.Fatalf("the health gate answered %d with a marker in place:\n%s", rc, log)
		}
		if _, err := os.Stat(filepath.Join(root, updater.PendingFile)); !os.IsNotExist(err) {
			t.Fatal("the gate left the pending marker in place")
		}
		if got, want := currentTarget(t, root), updater.ReleasesDir+"/"+newVersion; got != want {
			t.Fatalf("the gate moved current to %q", got)
		}
	})

	t.Run("no marker rolls back", func(t *testing.T) {
		root := releaseRoot(t, oldBinary)
		m := manager(t, root, publicKey, readVersion)
		rel := good.serve(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.Apply(ctx, rel); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		// Nothing writes a health marker: this is a release that does not come up.
		rc, log := runHealthGate(t, root, 4)
		t.Logf("the gate answered %d:\n%s", rc, log)
		if got, want := currentTarget(t, root), updater.ReleasesDir+"/"+oldVersion; got != want {
			t.Fatalf("current is %q after the rollback, want %q", got, want)
		}
		if _, err := os.Stat(filepath.Join(root, updater.HealthDir, newVersion+updater.BadSuffix)); err != nil {
			t.Fatalf("the gate wrote no %s%s marker: %v", newVersion, updater.BadSuffix, err)
		}
		if _, err := os.Stat(filepath.Join(root, updater.PendingFile)); !os.IsNotExist(err) {
			t.Fatal("the gate left the pending marker in place")
		}
		if _, err := os.Stat(filepath.Join(root, "rc-service.called")); err != nil {
			t.Fatalf("the gate did not restart the service: %v", err)
		}
	})
}

// TestRefusals proves that every bad release is refused before the flip (D47).
func TestRefusals(t *testing.T) {
	skipUnlessEnabled(t)
	repo := repoRoot(t)
	key := newSigner(t)
	other := newSigner(t)
	publicKey := key.publicText(t)

	work := t.TempDir()
	oldBinary := filepath.Join(work, "portapixeld-old")
	newBinary := filepath.Join(work, "portapixeld-new")
	build(t, repo, oldVersion, publicKey, oldBinary)
	build(t, repo, newVersion, publicKey, newBinary)
	body, err := os.ReadFile(newBinary)
	if err != nil {
		t.Fatal(err)
	}
	asset := "portapixeld-" + runtime.GOARCH

	changed := append([]byte(nil), body...)
	changed[len(changed)-1] ^= 0xff

	cases := []struct {
		name   string
		bundle bundle
	}{
		{"no signature", newBundle(t, asset, body, nil)},
		{"another key", newBundle(t, asset, body, other.sign(t, body))},
		{"a legacy signature", newBundle(t, asset, body, key.signLegacy(t, body))},
		{"a changed binary", bundle{
			asset:  asset,
			binary: changed,
			sig:    key.sign(t, body),
			sums:   []byte(sha256Hex(body) + "  " + asset + "\n"),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := releaseRoot(t, oldBinary)
			m := manager(t, root, publicKey, nil)
			rel := c.bundle.serve(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			err := m.Apply(ctx, rel)
			if err == nil {
				t.Fatal("Apply took a release that it must refuse")
			}
			t.Logf("refused: %v", err)
			if got, want := currentTarget(t, root), updater.ReleasesDir+"/"+oldVersion; got != want {
				t.Fatalf("current is %q, so the flip happened before the refusal", got)
			}
		})
	}

	// The same rule one level down, so the reason is in the report: a legacy
	// signature is refused by name.
	t.Run("the verifier names the legacy signature", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "binary")
		if err := os.WriteFile(file, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file+updater.SigSuffix, key.signLegacy(t, body), 0o644); err != nil {
			t.Fatal(err)
		}
		err := sigverify.VerifyFile(file, file+updater.SigSuffix, publicKey)
		if !errors.Is(err, sigverify.ErrLegacySignature) {
			t.Fatalf("VerifyFile gave %v, want ErrLegacySignature", err)
		}
	})
}

// --------------------------------------------------------------- the two runners

// runDaemonUntilMarker starts the real daemon with the browser off and waits for
// the health marker. It gives the path of the marker that appeared.
func runDaemonUntilMarker(t *testing.T, binary, root string) string {
	t.Helper()
	work := t.TempDir()
	for _, dir := range []string{"media", "state", "run"} {
		if err := os.MkdirAll(filepath.Join(work, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(binary, "run",
		"--browser-cmd", "none",
		"--kiosk-user", "",
		"--listen", "127.0.0.1:18099",
		"--media", filepath.Join(work, "media"),
		"--state", filepath.Join(work, "state"),
		"--run", filepath.Join(work, "run"),
		"--releases", root,
	)
	var log bytes.Buffer
	cmd.Stdout = &log
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the daemon: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Logf("daemon log:\n%s", log.String())
	}()

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(filepath.Join(root, updater.HealthDir))
		if err == nil {
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), updater.OKSuffix) {
					return filepath.Join(root, updater.HealthDir, e.Name())
				}
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("the daemon wrote no health marker in 90 s:\n%s", log.String())
	return ""
}

// runHealthGate runs the real os/overlay health-gate.sh against a release root.
// It gives the exit code and the ops log.
//
// The script calls "rc-service portapixeld restart" after a rollback. The test
// puts a stand-in for that command first in PATH, so the restart is recorded and
// not attempted.
func runHealthGate(t *testing.T, root string, timeout int) (int, string) {
	t.Helper()
	repo := repoRoot(t)
	script := filepath.Join(repo, "os", "overlay", "usr", "libexec", "portapixel", "health-gate.sh")
	if _, err := os.Stat(oplogPath); err != nil {
		t.Fatalf("%s is missing. The CI job copies os/overlay/usr/libexec/portapixel "+
			"to /usr/libexec before it runs this gate: %v", oplogPath, err)
	}

	bin := t.TempDir()
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >>" + filepath.Join(root, "rc-service.called") + "\n"
	if err := os.WriteFile(filepath.Join(bin, "rc-service"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PP_RELEASES="+root,
		"PP_STATE="+state,
		"PP_RUN="+filepath.Join(root, "run"),
		fmt.Sprintf("PP_HEALTH_TIMEOUT=%d", timeout),
	)
	out, err := cmd.CombinedOutput()
	rc := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		rc = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run the health gate: %v", err)
	}
	log, _ := os.ReadFile(filepath.Join(state, "ops.log"))
	return rc, string(out) + string(log)
}
