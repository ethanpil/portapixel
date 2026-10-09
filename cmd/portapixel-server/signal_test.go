package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// signalChildEnv marks the test binary that runs as the program under test.
const signalChildEnv = "PORTAPIXEL_TEST_SIGNAL_CHILD"

// TestSignalContextChild is not a test. The test below starts the test binary
// again with the variable set, and this function is the program that it signals.
// It ends its context on the first signal and then hangs, as a shutdown does when a
// transfer does not finish.
func TestSignalContextChild(t *testing.T) {
	if os.Getenv(signalChildEnv) == "" {
		t.Skip("this is the helper program of TestASecondSignalEndsTheProcess")
	}
	ctx, stop := signalContext(os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Println("ready")
	<-ctx.Done()
	fmt.Println("stopping")
	time.Sleep(time.Minute)
}

// TestASecondSignalEndsTheProcess covers the comment in runCommand. The first signal
// asks the server to stop, and a second one must end the process at once. With
// signal.NotifyContext alone, the second signal went nowhere.
func TestASecondSignalEndsTheProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot send SIGTERM to a process")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalContextChild$")
	cmd.Env = append(os.Environ(), signalChildEnv+"=1")
	// An own pipe, because Wait closes the pipe of StdoutPipe while a read may run.
	out, in, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	cmd.Stdout = in
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	in.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer cmd.Process.Kill()

	lines := bufio.NewScanner(out)
	waitFor := func(want string) {
		t.Helper()
		for lines.Scan() {
			if strings.TrimSpace(lines.Text()) == want {
				return
			}
		}
		t.Fatalf("the helper program never printed %q", want)
	}

	waitFor("ready")
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitFor("stopping")
	select {
	case <-exited:
		t.Fatal("the first signal ended the process; it must only end the context")
	case <-time.After(200 * time.Millisecond):
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("a second signal did not end the process")
	}
}
