// Package prepare runs configured bay prepare steps.
package prepare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
)

const defaultHeartbeatInterval = 5 * time.Second

// Options identifies the bay a worker should prepare.
type Options struct {
	Dock string
	Bay  string
}

// Worker owns one prepare run for one bay.
type Worker struct {
	Config            *config.Config
	ManifestPath      string
	DataDir           string
	HeartbeatInterval time.Duration
	Now               func() time.Time
	PID               int
}

// Run executes configured prepare steps serially for opts.
func (w *Worker) Run(ctx context.Context, opts Options) error {
	if err := w.manager().validateOpts(opts); err != nil {
		return err
	}
	if w.DataDir == "" {
		return fmt.Errorf("data dir is required")
	}

	plan, err := w.loadPlan(opts)
	if err != nil {
		return err
	}
	if len(plan.Steps) == 0 {
		return nil
	}

	for _, step := range plan.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		defHash, err := DefinitionHash(step)
		if err != nil {
			return err
		}

		lock, acquired, err := tryStepLock(stepLockPath(w.DataDir, opts.Dock, opts.Bay, step.Name))
		if err != nil {
			return err
		}
		if !acquired {
			return nil
		}

		shouldRun, err := w.markRunning(opts, plan.Steps, step, defHash)
		if err != nil {
			lock.Unlock()
			return err
		}
		if !shouldRun {
			lock.Unlock()
			continue
		}

		failed, err := w.runStep(ctx, opts, plan.BayPath, step, defHash)
		lock.Unlock()
		if err != nil {
			return err
		}
		if failed {
			return nil
		}
	}
	return nil
}

func (w *Worker) manager() Manager {
	return Manager{
		Config:       w.Config,
		ManifestPath: w.ManifestPath,
		DataDir:      w.DataDir,
		Now:          w.Now,
	}
}

func (w *Worker) loadPlan(opts Options) (Plan, error) {
	return w.manager().loadPlan(opts)
}

func (w *Worker) markRunning(opts Options, allSteps []config.BayPrepareConfig, step config.BayPrepareConfig, defHash string) (bool, error) {
	pid := w.pid()
	now := w.now().Unix()
	shouldRun := true
	err := manifest.LockedUpdateMaybe(w.ManifestPath, func(m *manifest.Manifest) (bool, error) {
		bay, err := findBay(m, opts)
		if err != nil {
			return false, err
		}
		entry := findPrepareEntry(bay, step.Name)
		if entry != nil && entry.Status == manifest.PrepareStatusReady && entry.DefinitionHash == defHash {
			shouldRun = false
			return false, nil
		}
		ensurePrepareEntries(bay, allSteps)
		entry = ensurePrepareEntry(bay, step.Name)
		entry.Status = manifest.PrepareStatusRunning
		entry.StartedAt = now
		entry.FinishedAt = 0
		entry.HeartbeatAt = now
		entry.PID = pid
		entry.RunLogOffset = 0
		return true, nil
	})
	return shouldRun, err
}

func (w *Worker) runStep(ctx context.Context, opts Options, bayPath string, step config.BayPrepareConfig, defHash string) (bool, error) {
	started := w.now()
	logFile, offset, err := w.openLogAndWriteStart(opts.Dock, started, opts.Bay, step.Name)
	if err != nil {
		_ = w.markFinished(opts, step.Name, manifest.PrepareStatusFailed, "")
		return true, err
	}
	defer logFile.Close()

	if err := w.setRunLogOffset(opts, step.Name, offset); err != nil {
		return true, err
	}

	stopHeartbeat := w.startHeartbeat(ctx, opts, step.Name)
	cmdErr := runCommand(ctx, bayPath, step, logFile)
	stopHeartbeat()

	status := manifest.PrepareStatusReady
	hash := defHash
	failed := false
	if cmdErr != nil {
		status = manifest.PrepareStatusFailed
		hash = ""
		failed = true
		fmt.Fprintf(logFile, "prepare command failed: %v\n", cmdErr)
	}
	if _, err := fmt.Fprintf(logFile, "==== finished bay=%s step=%s %s status=%s ====\n", opts.Bay, step.Name, w.now().Format(time.RFC3339), status); err != nil {
		_ = w.markFinished(opts, step.Name, manifest.PrepareStatusFailed, "")
		return true, fmt.Errorf("writing prepare log: %w", err)
	}
	if err := w.markFinished(opts, step.Name, status, hash); err != nil {
		return true, err
	}
	return failed, nil
}

func (w *Worker) openLogAndWriteStart(dock string, ts time.Time, bayID, stepName string) (*runLog, int64, error) {
	dir := filepath.Join(w.DataDir, "logs", dock, ts.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, 0, fmt.Errorf("creating prepare log dir: %w", err)
	}
	path := filepath.Join(dir, "prepare.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, 0, fmt.Errorf("opening prepare log: %w", err)
	}
	log := &runLog{file: f, lockPath: path + ".lock"}
	offset, err := log.writeStart(bayID, stepName, ts)
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return log, offset, nil
}

type runLog struct {
	file     *os.File
	lockPath string
}

func (l *runLog) Write(p []byte) (int, error) {
	unlock, err := lockExclusive(l.lockPath)
	if err != nil {
		return 0, fmt.Errorf("locking prepare log: %w", err)
	}
	defer unlock()
	return l.file.Write(p)
}

func (l *runLog) Close() error {
	return l.file.Close()
}

func (l *runLog) writeStart(bayID, stepName string, ts time.Time) (int64, error) {
	unlock, err := lockExclusive(l.lockPath)
	if err != nil {
		return 0, fmt.Errorf("locking prepare log: %w", err)
	}
	defer unlock()

	offset, err := l.file.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("seeking prepare log: %w", err)
	}
	if err := writeRunHeader(l.file, bayID, stepName, ts); err != nil {
		return 0, fmt.Errorf("writing prepare log: %w", err)
	}
	return offset, nil
}

func runCommand(ctx context.Context, bayPath string, step config.BayPrepareConfig, logFile io.Writer) error {
	runCtx := ctx
	cancel := func() {}
	if timeout, err := step.ParsedTimeout(); err != nil {
		return err
	} else if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, step.Command[0], step.Command[1:]...)
	if bayPath != "" {
		cmd.Dir = bayPath
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Run(); err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s: %w", step.Timeout, err)
		}
		return err
	}
	return nil
}

func (w *Worker) setRunLogOffset(opts Options, stepName string, offset int64) error {
	return w.mutateEntry(opts, stepName, func(entry *manifest.PrepareStep) {
		entry.RunLogOffset = offset
	})
}

func (w *Worker) mutateEntry(opts Options, stepName string, fn func(*manifest.PrepareStep)) error {
	return manifest.LockedUpdate(w.ManifestPath, func(m *manifest.Manifest) error {
		bay, err := findBay(m, opts)
		if err != nil {
			return err
		}
		fn(ensurePrepareEntry(bay, stepName))
		return nil
	})
}

func (w *Worker) startHeartbeat(ctx context.Context, opts Options, stepName string) func() {
	interval := w.heartbeatInterval()
	if interval <= 0 {
		return func() {}
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = w.setHeartbeat(opts, stepName)
			case <-heartbeatCtx.Done():
				return
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (w *Worker) setHeartbeat(opts Options, stepName string) error {
	now := w.now().Unix()
	return w.mutateEntry(opts, stepName, func(entry *manifest.PrepareStep) {
		if entry.Status == manifest.PrepareStatusRunning {
			entry.HeartbeatAt = now
		}
	})
}

func (w *Worker) markFinished(opts Options, stepName string, status manifest.PrepareStatus, defHash string) error {
	finishedAt := w.now().Unix()
	return w.mutateEntry(opts, stepName, func(entry *manifest.PrepareStep) {
		entry.Status = status
		entry.FinishedAt = finishedAt
		entry.HeartbeatAt = 0
		entry.PID = 0
		entry.RunLogOffset = 0
		entry.DefinitionHash = defHash
	})
}

func findBay(m *manifest.Manifest, opts Options) (*manifest.Bay, error) {
	dock := m.FindDock(opts.Dock)
	if dock == nil {
		return nil, fmt.Errorf("dock %q not found", opts.Dock)
	}
	bay := dock.FindBayByID(opts.Bay)
	if bay == nil {
		return nil, fmt.Errorf("bay %q not found in dock %q", opts.Bay, opts.Dock)
	}
	return bay, nil
}

func ensurePrepareEntries(bay *manifest.Bay, steps []config.BayPrepareConfig) {
	for _, step := range steps {
		ensurePrepareEntry(bay, step.Name)
	}
}

func ensurePrepareEntry(bay *manifest.Bay, name string) *manifest.PrepareStep {
	if entry := findPrepareEntry(bay, name); entry != nil {
		return entry
	}
	bay.Prepare = append(bay.Prepare, manifest.PrepareStep{
		Name:   name,
		Status: manifest.PrepareStatusPending,
	})
	return &bay.Prepare[len(bay.Prepare)-1]
}

func findPrepareEntry(bay *manifest.Bay, name string) *manifest.PrepareStep {
	for i := range bay.Prepare {
		if bay.Prepare[i].Name == name {
			return &bay.Prepare[i]
		}
	}
	return nil
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) pid() int {
	if w.PID != 0 {
		return w.PID
	}
	return os.Getpid()
}

func (w *Worker) heartbeatInterval() time.Duration {
	if w.HeartbeatInterval != 0 {
		return w.HeartbeatInterval
	}
	return defaultHeartbeatInterval
}

// stepDefinition is the BayPrepareConfig subset whose changes invalidate a
// cached "ready" status. Name is the lookup key, and Run only controls
// dispatch policy, so neither belongs in the definition hash.
type stepDefinition struct {
	Command      []string `json:"command"`
	ReadyCommand []string `json:"ready_command"`
	Blocks       []string `json:"blocks"`
	Timeout      string   `json:"timeout"`
}

// DefinitionHash returns the persisted hash for a prepare step definition.
func DefinitionHash(step config.BayPrepareConfig) (string, error) {
	def := stepDefinition{
		Command:      nonNilStrings(step.Command),
		ReadyCommand: nonNilStrings(step.ReadyCommand),
		Blocks:       nonNilStrings(step.Blocks),
		Timeout:      step.Timeout,
	}
	data, err := json.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("encoding prepare definition: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

type stepLock struct {
	file *os.File
}

func tryStepLock(path string) (*stepLock, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, fmt.Errorf("creating prepare lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("opening prepare lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("acquiring prepare lock: %w", err)
	}
	return &stepLock{file: f}, true, nil
}

func lockExclusive(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func (l *stepLock) Unlock() {
	if l == nil || l.file == nil {
		return
	}
	syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	l.file.Close()
}

func stepLockPath(dataDir, dockName, bayID, stepName string) string {
	return filepath.Join(dataDir, "locks", "prepare", dockName, bayID, safeSegment(stepName)+".lock")
}

func safeSegment(s string) string {
	replacer := strings.NewReplacer(
		"/", "%2F",
		"\\", "%5C",
	)
	return replacer.Replace(s)
}
