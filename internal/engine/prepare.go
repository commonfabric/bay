package engine

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/commonfabric/bay/internal/prepare"
)

// startWorkerProcess is the default Engine.startWorker. Detaches the
// child so the worker survives the parent (e.g., the CLI process)
// exiting. Shared by the prepare and describe dispatchers.
func startWorkerProcess(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting worker: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("releasing worker: %w", err)
	}
	return nil
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
