package engine

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A long-lived parent (the monitor) must reap the workers it dispatches.
// An exited-but-unreaped child still answers signal 0; only a reaped one
// returns ESRCH.
func TestSpawnWorker_ReapsExitedChild(t *testing.T) {
	exe, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("no true binary: %v", err)
	}
	pid, err := spawnWorker(exe, nil)
	if err != nil {
		t.Fatalf("spawnWorker: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker pid %d exited but was never reaped (zombie)", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The monitor holds its in-place re-exec while workers are in flight;
// the count must cover a running worker and drop once it is reaped.
func TestWorkersInFlight_TracksRunningWorker(t *testing.T) {
	exe, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	before := WorkersInFlight()
	if _, err := spawnWorker(exe, []string{"0.5"}); err != nil {
		t.Fatalf("spawnWorker: %v", err)
	}
	if got := WorkersInFlight(); got != before+1 {
		t.Fatalf("WorkersInFlight while running = %d, want %d", got, before+1)
	}
	deadline := time.Now().Add(10 * time.Second)
	for WorkersInFlight() != before {
		if time.Now().After(deadline) {
			t.Fatalf("WorkersInFlight = %d after worker exit, want %d", WorkersInFlight(), before)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
