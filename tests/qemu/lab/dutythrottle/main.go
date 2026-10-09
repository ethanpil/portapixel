//go:build linux

// Command dutythrottle gives one process a CPU duty cycle with SIGSTOP and
// SIGCONT. The lab uses it on the QEMU process of pp-zero, so that the guest
// has about the CPU speed of a Raspberry Pi Zero 2 W (tests/qemu/lab/README.md).
//
// Usage: dutythrottle PID RUN_US STOP_US
//
// The process runs for RUN_US microseconds, then stops for STOP_US
// microseconds, again and again. The times come from a monotonic schedule, so a
// late wake-up does not move the cycle. On TERM, INT or HUP, and when the process
// is gone, dutythrottle sends SIGCONT and stops: it never leaves the process
// stopped.
//
// Build it on the lab host:
//
//	CGO_ENABLED=0 go build -o /root/ppwork/lab/dutythrottle ./tests/qemu/lab/dutythrottle
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: dutythrottle PID RUN_US STOP_US")
		os.Exit(2)
	}
	pid, err1 := strconv.Atoi(os.Args[1])
	run, err2 := strconv.Atoi(os.Args[2])
	stop, err3 := strconv.Atoi(os.Args[3])
	if err1 != nil || err2 != nil || err3 != nil || pid < 2 || run < 100 || stop < 0 {
		fmt.Fprintln(os.Stderr, "dutythrottle: PID must be 2 or more, RUN_US 100 or more, STOP_US 0 or more")
		os.Exit(2)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer syscall.Kill(pid, syscall.SIGCONT)

	runFor := time.Duration(run) * time.Microsecond
	stopFor := time.Duration(stop) * time.Microsecond
	next := time.Now()
	for {
		if syscall.Kill(pid, syscall.SIGCONT) != nil {
			return // the process is gone
		}
		next = next.Add(runFor)
		if !sleepUntil(next, quit) {
			return
		}
		if syscall.Kill(pid, syscall.SIGSTOP) != nil {
			return
		}
		next = next.Add(stopFor)
		if !sleepUntil(next, quit) {
			return
		}
	}
}

// sleepUntil waits until t. It returns false when a stop signal came first.
func sleepUntil(t time.Time, quit <-chan os.Signal) bool {
	timer := time.NewTimer(time.Until(t))
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-quit:
		return false
	}
}
