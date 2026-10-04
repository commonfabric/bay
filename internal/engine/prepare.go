package engine

import (
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"

	"github.com/commonfabric/bay/internal/prepare"
)

// startWorkerProcess is the default Engine.startWorker. Detaches the
// child so the worker survives the parent (e.g., the CLI process)
// exiting. Shared by the prepare and describe dispatchers.
func startWorkerProcess(exe string, args []string) error {
	_, err := spawnWorker(exe, args)
	return err
}

// spawnWorker starts the detached worker and returns its PID. The child
// is waited on in a goroutine rather than released: Process.Release does
// not reap, so a parent that outlives the worker (the monitor, which
// dispatches describe workers for weeks) would otherwise keep every
// exited worker as a zombie. A short-lived parent exits before the worker
// does and the worker is reparented to init, as before.
//
// The waiter lives only in this process image; see WorkersInFlight.
func spawnWorker(exe string, args []string) (int, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting worker: %w", err)
	}
	pid := cmd.Process.Pid
	workersInFlight.Add(1)
	go func() {
		_ = cmd.Wait()
		workersInFlight.Add(-1)
	}()
	return pid, nil
}

// workersInFlight counts workers this process started and has not yet
// reaped.
var workersInFlight atomic.Int64

// WorkersInFlight reports how many dispatched workers are still running
// under this process. A process that replaces its own image with
// syscall.Exec keeps its children but loses the goroutines waiting on
// them, so it must not exec while this is non-zero.
func WorkersInFlight() int {
	return int(workersInFlight.Load())
}

// PreparePlan returns the effective prepare plan for a bay.
func (e *Engine) PreparePlan(dockName, bayID string) (prepare.Plan, error) {
	manager := prepare.Manager{
		Config:       e.Config,
		ManifestPath: e.manifestPath,
	}
	return manager.Plan(prepare.Options{Dock: dockName, Bay: bayID})
}

// DispatchPrepareWorker starts a detached same-binary prepare worker for a bay.
func (e *Engine) DispatchPrepareWorker(dockName, bayID string) error {
	if dockName == "" {
		return fmt.Errorf("dock is required")
	}
	if bayID == "" {
		return fmt.Errorf("bay is required")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable: %w", err)
	}

	args := []string{}
	if e.configPath != "" {
		args = append(args, "--config", e.configPath)
	}
	args = append(args, "prepare-worker", "--dock", dockName, "--bay", bayID)
	startWorker := e.startWorker
	if startWorker == nil {
		startWorker = startWorkerProcess
	}
	return startWorker(exe, args)
}
