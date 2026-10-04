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
