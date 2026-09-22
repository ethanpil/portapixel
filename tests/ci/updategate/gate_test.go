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
//   - twenty starts of the gate and the daemon together all pass, so the order of
//     the two cannot decide the answer;
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
	"encoding/json"
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

// publicText is the key in the ONE LINE form, which is the base64 line of a
// minisign.pub file with no comment line. -ldflags -X cannot carry a newline, so
// the repository variable MINISIGN_PUBLIC_KEY holds this form too.
func (s signer) publicText(t *testing.T) string {
	t.Helper()
	return s.public.String()
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
func manager(t *testing.T, root, publicKey string, binaryVersion func(string) (updater.BinaryInfo, error)) *updater.Manager {
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
	// the updater runs the staged binary and reads what it says about itself.
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
				"The binary answers \"version --json\" with {name, version, arch}, and\n"+
				"internal/updater readBinaryInfo reads that form. A reader that takes a\n"+
				"field of the human line by its position compares the processor name\n"+
				"with the release name.", err)
			return
		}
		if got, want := currentTarget(t, root), updater.ReleasesDir+"/"+newVersion; got != want {
			t.Errorf("current points at %q, want %q", got, want)
		}
	})

	// From here the test gives a version reader of its own, so that one fault in
	// the production reader cannot hide the rest of the path. It reads the same
	// JSON, with its own decoder.
	readVersion := func(path string) (updater.BinaryInfo, error) {
		out, err := exec.Command(path, "version", "--json").Output()
		if err != nil {
			return updater.BinaryInfo{}, err
		}
		var info struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Arch    string `json:"arch"`
		}
		if err := json.Unmarshal(out, &info); err != nil {
			return updater.BinaryInfo{}, fmt.Errorf("the binary answered %q: %w", out, err)
		}
		if info.Version == "" || info.Arch == "" {
			return updater.BinaryInfo{}, errors.New("the binary named no version and no processor")
		}
		return updater.BinaryInfo{Name: info.Name, Version: info.Version, Arch: info.Arch}, nil
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

		// The order of the OpenRC service: start_pre clears the tmpfs health
		// directory and starts the gate in the background, then the service starts
		// the daemon. prepareHealthGate does the clearing.
		gate, gateLog := prepareHealthGate(t, root, 90)
		if err := gate.Start(); err != nil {
			t.Fatalf("start the health gate: %v", err)
		}
		time.Sleep(3 * time.Second)
		stop := startDaemon(t, filepath.Join(root, updater.ReleasesDir, newVersion, "portapixeld"), root)
		defer stop()

		rc := waitGate(t, gate)
		log := gateLog()
		t.Logf("the gate answered %d:\n%s", rc, log)
		if rc != 0 {
			t.Fatalf("the health gate answered %d while the new daemon was up:\n%s", rc, log)
		}
		// The NAME and the PLACE of the marker are the point. A name that does not
		// match rolls a good release back for ever (final review item 19), and the
		// place is the tmpfs run directory and never the flash.
		marker := updater.MarkerPath(runDir(root), newVersion)
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("the daemon wrote no %s%s in the run directory: %v", newVersion, updater.OKSuffix, err)
		}
		t.Logf("the health marker is %s", filepath.Base(marker))
		// Nothing of the daemon reaches the flash. The gate writes only .bad and the
		// boot counter there, and this release passed.
		flash, err := os.ReadDir(filepath.Join(root, updater.HealthDir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range flash {
			t.Errorf("%s is under the release root, which is the flash", e.Name())
		}
		if _, err := os.Stat(filepath.Join(root, updater.PendingFile)); !os.IsNotExist(err) {
			t.Fatal("the gate left the pending marker in place")
		}
		if got, want := currentTarget(t, root), updater.ReleasesDir+"/"+newVersion; got != want {
			t.Fatalf("the gate moved current to %q", got)
		}
	})

	// The race that rolled a good release back. The gate and the daemon start at
	// the same moment, twenty times, in both orders. The marker is in tmpfs now, so
	// a marker of an earlier boot cannot exist and no step removes one. The test
	// stays: it is the proof that the order of the two processes decides nothing.
	//
	// Each round also puts a STALE marker in the tmpfs health directory, which is
	// what a restart of the SERVICE with no restart of the machine leaves behind.
	// prepareHealthGate clears the directory, the way the init script does in
	// start_pre, and the marker the gate passes on must be the one THIS daemon wrote.
	t.Run("the gate and the daemon start together", func(t *testing.T) {
		root := releaseRoot(t, oldBinary)
		m := manager(t, root, publicKey, readVersion)
		rel := good.serve(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.Apply(ctx, rel); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		binary := filepath.Join(root, updater.ReleasesDir, newVersion, "portapixeld")
		pending := filepath.Join(root, updater.PendingFile)
		marker := updater.MarkerPath(runDir(root), newVersion)

		const rounds = 20
		for i := 1; i <= rounds; i++ {
			// One round in a function of its own, so that every defer runs at the end
			// of the round and a t.Fatalf leaves nothing behind.
			func(i int) {
				// The state after an Apply: the pending marker names the new release.
				// The first round already has it, and a round that passed removed it.
				if err := os.WriteFile(pending, []byte(newVersion+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				// A marker of an earlier start of the service. It proves nothing, so
				// start_pre has to take it away.
				if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, []byte("a stale marker of an earlier start\n"), 0o644); err != nil {
					t.Fatal(err)
				}

				// prepareHealthGate clears the tmpfs health directory, the way
				// start_pre does before the service starts the daemon.
				gate, gateLog := prepareHealthGate(t, root, 30)
				if i%2 == 0 {
					// The gate looks first and the daemon comes up while it waits.
					if err := gate.Start(); err != nil {
						t.Fatalf("round %d: start the health gate: %v", i, err)
					}
					startDaemon(t, binary, root)
				} else {
					// The daemon WINS the race: its marker is there before the gate
					// looks at all. This is the order that rolled a good release back
					// when the gate removed the marker after it started.
					startDaemon(t, binary, root)
					waitForMarker(t, marker)
					if err := gate.Start(); err != nil {
						t.Fatalf("round %d: start the health gate: %v", i, err)
					}
				}
				rc := waitGate(t, gate)
				body, readErr := os.ReadFile(marker)
				if rc != 0 {
					t.Fatalf("round %d: the health gate answered %d while the new daemon was up:\n%s",
						i, rc, gateLog())
				}
				if readErr != nil {
					t.Fatalf("round %d: the daemon wrote no health marker: %v", i, readErr)
				}
				// The daemon writes its own version, its pid and its start time. The
				// stale line holds none of that, so a gate that passed on the stale
				// marker fails here.
				if !strings.Contains(string(body), "pid=") {
					t.Fatalf("round %d: the gate passed on the marker %q, which this daemon did not write",
						i, strings.TrimSpace(string(body)))
				}
				if _, err := os.Stat(pending); !os.IsNotExist(err) {
					t.Fatalf("round %d: the gate left the pending marker in place", i)
				}
				if got, want := currentTarget(t, root), updater.ReleasesDir+"/"+newVersion; got != want {
					t.Fatalf("round %d: the gate moved current to %q:\n%s", i, got, gateLog())
				}
			}(i)
		}
		t.Logf("%d starts of the gate and the daemon together, no rollback", rounds)
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
		gate, gateLog := prepareHealthGate(t, root, 4)
		if err := gate.Start(); err != nil {
			t.Fatalf("start the health gate: %v", err)
		}
		rc := waitGate(t, gate)
		log := gateLog()
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

// runDir is the tmpfs run directory that the daemon and the gate SHARE. The
// daemon writes <run>/health/<version>.ok there and the gate looks for it there,
// so the two must be given the same directory. On a device that is PP_RUN of
// /etc/conf.d/portapixeld, which the init script passes to --run.
func runDir(root string) string { return filepath.Join(root, "run") }

// startDaemon starts the real daemon of a release with the browser off. It stops
// the daemon and prints its log through t.Cleanup, so a round that fails leaves no
// live process behind and still shows the log that explains the failure.
//
// The daemon writes <run>/health/<its own version>.ok when it is up. That is the
// file that the health gate waits for, so the daemon and the gate must run at the
// same time, the way the OpenRC service starts them.
func startDaemon(t *testing.T, binary, root string) func() {
	t.Helper()
	work := t.TempDir()
	for _, dir := range []string{"media", "state"} {
		if err := os.MkdirAll(filepath.Join(work, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(binary, "run",
		"--browser-cmd", "none",
		"--kiosk-user", "",
		// Port 0: the race test starts the daemon twenty times, and a fixed port
		// can still be in the TIME_WAIT state of the round before.
		"--listen", "127.0.0.1:0",
		"--media", filepath.Join(work, "media"),
		"--state", filepath.Join(work, "state"),
		"--run", runDir(root),
		"--releases", root,
	)
	var log bytes.Buffer
	cmd.Stdout = &log
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the daemon: %v", err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Kill()
		// cmd.Wait and not Process.Wait: Wait joins the two goroutines that copy the
		// output of the child into the buffer. Reading the buffer after Process.Wait
		// is a data race and it can lose the end of the log.
		_ = cmd.Wait()
		t.Logf("daemon log:\n%s", log.String())
	}
	// A t.Fatalf in the caller must not leave the daemon running.
	t.Cleanup(stop)
	return stop
}

// prepareHealthGate builds the command of the real os/overlay health-gate.sh
// against a release root. The caller starts it, because the gate and the daemon
// run at the same time on a device.
//
// There is no step before it any more. The marker lives in the tmpfs run
// directory, so a marker of an earlier boot cannot exist and no step has to remove
// one. The init script clears <run>/health in start_pre for the case of a service
// restart with no machine restart, and this function does the same.
//
// The script calls "rc-service portapixeld restart" after a rollback. The test
// puts a stand-in for that command first in PATH, so the restart is recorded and
// not attempted. The second value reads the ops log that the gate wrote.
func prepareHealthGate(t *testing.T, root string, timeout int) (*exec.Cmd, func() string) {
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
	// start_pre clears the tmpfs health directory and makes it again. This covers a
	// restart of the service with no restart of the machine, which is the one case
	// in which a marker of an earlier start can still be there.
	runHealth := filepath.Join(runDir(root), updater.HealthDir)
	if err := os.RemoveAll(runHealth); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runHealth, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PP_RELEASES="+root,
		"PP_STATE="+state,
		"PP_RUN="+runDir(root),
		fmt.Sprintf("PP_HEALTH_TIMEOUT=%d", timeout),
		// One second, not the default two. The race test runs the whole gate twenty
		// times, and the step decides how long each round takes.
		"PP_HEALTH_POLL=1",
	)

	cmd := exec.Command("sh", script, "wait")
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	return cmd, func() string {
		log, _ := os.ReadFile(filepath.Join(state, "ops.log"))
		return out.String() + string(log)
	}
}

// waitForMarker waits until the daemon has written its health marker. The content
// names the run that wrote it, so "pid=" tells the marker of this daemon from the
// stale line that the test left behind.
func waitForMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := os.ReadFile(path); err == nil && strings.Contains(string(body), "pid=") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the daemon wrote no health marker of its own")
}

// waitGate waits for the health gate and gives its exit code.
func waitGate(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	err := cmd.Wait()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	t.Fatalf("run the health gate: %v", err)
	return -1
}
