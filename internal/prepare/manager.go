package prepare

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
)

const staleHeartbeatAfter = 30 * time.Second

// Manager provides read/control operations for persisted prepare state.
type Manager struct {
	Config       *config.Config
	ManifestPath string
	DataDir      string
	Now          func() time.Time
}

// Plan is the effective prepare configuration for a bay.
type Plan struct {
	BayPath string
	Steps   []config.BayPrepareConfig
}

// StepStatus is the user-visible status of one prepare step.
type StepStatus struct {
	Name           string
	Status         manifest.PrepareStatus
	PID            int
	StartedAt      int64
	FinishedAt     int64
	HeartbeatAt    int64
	RunLogOffset   int64
	DefinitionHash string
}

// Plan returns the effective prepare plan for opts.
func (m Manager) Plan(opts Options) (Plan, error) {
	return m.loadPlan(opts)
}

// Status refreshes derived stale state and returns the current step statuses.
func (m Manager) Status(opts Options) ([]StepStatus, error) {
	if err := m.validateOpts(opts); err != nil {
		return nil, err
	}

	now := m.now().Unix()
	var statuses []StepStatus
	err := manifest.LockedUpdateMaybe(m.ManifestPath, func(mf *manifest.Manifest) (bool, error) {
		bay, err := findBay(mf, opts)
		if err != nil {
			return false, err
		}
		plan, err := m.planForBay(opts, bay)
		if err != nil {
			return false, err
		}
		changed := false
		ensurePrepareEntries(bay, plan.Steps)
		for _, step := range plan.Steps {
			entry := ensurePrepareEntry(bay, step.Name)
			if entry.Status == "" {
				entry.Status = manifest.PrepareStatusPending
				changed = true
			}

			defHash, err := DefinitionHash(step)
			if err != nil {
				return false, err
			}
			if entry.Status == manifest.PrepareStatusReady && entry.DefinitionHash != defHash {
				markEntryStale(entry, now)
				changed = true
			}
			if entry.Status == manifest.PrepareStatusRunning {
				held, err := m.stepLockHeld(opts, step.Name)
				if err != nil {
					return false, err
				}
				heartbeatExpired := entry.HeartbeatAt > 0 && now-entry.HeartbeatAt > int64(staleHeartbeatAfter/time.Second)
				if !held || heartbeatExpired {
					markEntryStale(entry, now)
					changed = true
				}
			}
			statuses = append(statuses, stepStatusFromEntry(*entry))
		}
		return changed, nil
	})
	return statuses, err
}

// Retry resets failed, stale, and never-started steps to pending. The returned
// slice is empty when nothing needed retrying — callers should dispatch a
// worker only when at least one step was reset.
func (m Manager) Retry(opts Options) ([]string, error) {
	statuses, err := m.Status(opts)
	if err != nil {
		return nil, err
	}
	retry := map[string]bool{}
	for _, status := range statuses {
		if status.Status == manifest.PrepareStatusFailed ||
			status.Status == manifest.PrepareStatusStale ||
			status.Status == manifest.PrepareStatusPending {
			retry[status.Name] = true
		}
	}
	if len(retry) == 0 {
		return nil, nil
	}

	var names []string
	err = manifest.LockedUpdateMaybe(m.ManifestPath, func(mf *manifest.Manifest) (bool, error) {
		bay, err := findBay(mf, opts)
		if err != nil {
			return false, err
		}
		changed := false
		for i := range bay.Prepare {
			entry := &bay.Prepare[i]
			if !retry[entry.Name] {
				continue
			}
			names = append(names, entry.Name)
			resetEntryPending(entry)
			changed = true
		}
		return changed, nil
	})
	return names, err
}

// Kill sends SIGTERM to running prepare workers and marks any non-terminal
// running steps stale if the worker does not exit within wait. Returns early
// if ctx is canceled — the affected step names are returned alongside the
// cancel error so callers can surface what was already signaled.
func (m Manager) Kill(ctx context.Context, opts Options, wait time.Duration) ([]string, error) {
	statuses, err := m.Status(opts)
	if err != nil {
		return nil, err
	}
	running := runningStatuses(statuses)
	if len(running) == 0 {
		return nil, nil
	}

	pids := map[int]bool{}
	var names []string
	for _, status := range running {
		names = append(names, status.Name)
		if status.PID != 0 {
			pids[status.PID] = true
		}
	}
	for pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		_ = proc.Signal(syscall.SIGTERM)
	}

	if wait <= 0 {
		return names, m.markStepsStale(opts, names)
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(wait)

	for {
		statuses, err = m.Status(opts)
		if err != nil {
			return names, err
		}
		if len(runningStatuses(statuses)) == 0 {
			return names, nil
		}
		select {
		case <-ctx.Done():
			return names, ctx.Err()
		case <-deadline:
			return names, m.markStepsStale(opts, names)
		case <-ticker.C:
		}
	}
}

// StepNames returns step names in declaration order.
func StepNames(steps []config.BayPrepareConfig) []string {
	names := make([]string, 0, len(steps))
	for _, step := range steps {
		names = append(names, step.Name)
	}
	return names
}

// AllTerminal reports whether none of the steps are waiting or running.
func AllTerminal(statuses []StepStatus) bool {
	for _, status := range statuses {
		if status.Status == manifest.PrepareStatusPending || status.Status == manifest.PrepareStatusRunning {
			return false
		}
	}
	return true
}

// TodayLogPath returns the prepare log path for dock on now's calendar day.
func TodayLogPath(dataDir, dock string, now time.Time) string {
	return filepath.Join(dataDir, "logs", dock, now.Format("2006-01-02"), "prepare.log")
}

func (m Manager) loadPlan(opts Options) (Plan, error) {
	if err := m.validateOpts(opts); err != nil {
		return Plan{}, err
	}
	mf, err := manifest.Load(m.ManifestPath)
	if err != nil {
		return Plan{}, err
	}
	bay, err := findBay(mf, opts)
	if err != nil {
		return Plan{}, err
	}
	return m.planForBay(opts, bay)
}

func (m Manager) validateOpts(opts Options) error {
	if opts.Dock == "" {
		return fmt.Errorf("dock is required")
	}
	if opts.Bay == "" {
		return fmt.Errorf("bay is required")
	}
	if m.ManifestPath == "" {
		return fmt.Errorf("manifest path is required")
	}
	return nil
}

// planForBay derives the effective prepare plan for an already-loaded bay,
// reading the repo-local config when the dock is trusted.
func (m Manager) planForBay(opts Options, bay *manifest.Bay) (Plan, error) {
	cfg := m.config()
	var repoSteps []config.BayPrepareConfig
	if bay.Path != "" && config.ResolveTrust(cfg, opts.Dock) {
		repoCfg, err := config.LoadRepoLocal(bay.Path)
		if err != nil {
			return Plan{}, err
		}
		if repoCfg != nil {
			repoSteps = repoCfg.BayPrepare
		}
	}

	var dockSteps []config.BayPrepareConfig
	if dockCfg, ok := cfg.Docks[opts.Dock]; ok {
		dockSteps = dockCfg.BayPrepare
	}

	if errs := config.ValidatePrepare(repoSteps, dockSteps); len(errs) > 0 {
		return Plan{}, fmt.Errorf("invalid prepare config: %s", strings.Join(errs, "; "))
	}
	return Plan{
		BayPath: bay.Path,
		Steps:   config.MergePrepare(repoSteps, dockSteps),
	}, nil
}

func (m Manager) config() *config.Config {
	if m.Config != nil {
		return m.Config
	}
	return config.DefaultConfig()
}

func (m Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m Manager) stepLockHeld(opts Options, stepName string) (bool, error) {
	if m.DataDir == "" {
		return true, nil
	}
	lock, acquired, err := tryStepLock(stepLockPath(m.DataDir, opts.Dock, opts.Bay, stepName))
	if err != nil {
		return false, err
	}
	if acquired {
		lock.Unlock()
		return false, nil
	}
	return true, nil
}

func (m Manager) markStepsStale(opts Options, names []string) error {
	if len(names) == 0 {
		return nil
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	now := m.now().Unix()
	return manifest.LockedUpdateMaybe(m.ManifestPath, func(mf *manifest.Manifest) (bool, error) {
		bay, err := findBay(mf, opts)
		if err != nil {
			return false, err
		}
		changed := false
		for i := range bay.Prepare {
			entry := &bay.Prepare[i]
			if !wanted[entry.Name] || entry.Status != manifest.PrepareStatusRunning {
				continue
			}
			markEntryStale(entry, now)
			changed = true
		}
		return changed, nil
	})
}

func stepStatusFromEntry(entry manifest.PrepareStep) StepStatus {
	return StepStatus{
		Name:           entry.Name,
		Status:         entry.Status,
		PID:            entry.PID,
		StartedAt:      entry.StartedAt,
		FinishedAt:     entry.FinishedAt,
		HeartbeatAt:    entry.HeartbeatAt,
		RunLogOffset:   entry.RunLogOffset,
		DefinitionHash: entry.DefinitionHash,
	}
}

func runningStatuses(statuses []StepStatus) []StepStatus {
	var running []StepStatus
	for _, status := range statuses {
		if status.Status == manifest.PrepareStatusRunning {
			running = append(running, status)
		}
	}
	return running
}

func markEntryStale(entry *manifest.PrepareStep, now int64) {
	entry.Status = manifest.PrepareStatusStale
	entry.FinishedAt = now
	entry.HeartbeatAt = 0
	entry.PID = 0
	entry.RunLogOffset = 0
	entry.DefinitionHash = ""
}

func resetEntryPending(entry *manifest.PrepareStep) {
	entry.Status = manifest.PrepareStatusPending
	entry.StartedAt = 0
	entry.FinishedAt = 0
	entry.HeartbeatAt = 0
	entry.PID = 0
	entry.RunLogOffset = 0
	entry.DefinitionHash = ""
}
