package prepare

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
)

func TestWorker_RunTrueMarksStepReadyAndWritesLog(t *testing.T) {
	trueCmd := commandPath(t, "true")
	h := newWorkerHarness(t, []config.BayPrepareConfig{
		{Name: "vendors", Command: []string{trueCmd}},
	})

	if err := h.worker.Run(t.Context(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	step := h.prepareStep(t, "vendors")
	if step.Status != manifest.PrepareStatusReady {
		t.Fatalf("status = %q, want ready\nlog:\n%s", step.Status, h.readLog(t))
	}
	if step.StartedAt == 0 || step.FinishedAt == 0 {
		t.Fatalf("timestamps not recorded: %#v", step)
	}
	if step.PID != 0 || step.HeartbeatAt != 0 || step.RunLogOffset != 0 {
		t.Fatalf("running fields not cleared: %#v", step)
	}
	wantHash, err := DefinitionHash(config.BayPrepareConfig{Name: "vendors", Command: []string{trueCmd}})
	if err != nil {
		t.Fatalf("DefinitionHash: %v", err)
	}
	if step.DefinitionHash != wantHash {
		t.Fatalf("definition hash = %q, want %q", step.DefinitionHash, wantHash)
	}

	log := h.readLog(t)
	if !strings.Contains(log, "==== run bay=b1 step=vendors 2026-05-07T10:00:00Z ====") {
		t.Fatalf("log missing run separator:\n%s", log)
	}
	if !strings.Contains(log, "==== finished bay=b1 step=vendors 2026-05-07T10:00:00Z status=ready ====") {
		t.Fatalf("log missing ready separator:\n%s", log)
	}
}

func TestWorker_RunFalseMarksFailedAndStops(t *testing.T) {
	h := newWorkerHarness(t, []config.BayPrepareConfig{
		{Name: "fail", Command: []string{commandPath(t, "false")}},
		{Name: "later", Command: []string{commandPath(t, "true")}},
	})

	if err := h.worker.Run(t.Context(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	failed := h.prepareStep(t, "fail")
	if failed.Status != manifest.PrepareStatusFailed {
		t.Fatalf("fail status = %q, want failed", failed.Status)
	}
	if failed.DefinitionHash != "" {
		t.Fatalf("failed step definition hash = %q, want empty", failed.DefinitionHash)
	}
	later := h.prepareStep(t, "later")
	if later.Status != manifest.PrepareStatusPending {
		t.Fatalf("later status = %q, want pending", later.Status)
	}
	if later.StartedAt != 0 {
		t.Fatalf("later step was started: %#v", later)
	}
}

func TestWorker_LockContentionExitsWithoutManifestChange(t *testing.T) {
	h := newWorkerHarness(t, []config.BayPrepareConfig{
		{Name: "vendors", Command: []string{commandPath(t, "true")}},
	})
	lock, acquired, err := tryStepLock(stepLockPath(h.dataDir, "labs", "b1", "vendors"))
	if err != nil {
		t.Fatalf("tryStepLock: %v", err)
	}
	if !acquired {
		t.Fatal("could not acquire test lock")
	}
	defer lock.Unlock()

	if err := h.worker.Run(t.Context(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	m := h.loadManifest(t)
	bay := m.FindDock("labs").FindBayByID("b1")
	if len(bay.Prepare) != 0 {
		t.Fatalf("prepare state changed under lock contention: %#v", bay.Prepare)
	}
}

func TestWorker_SkipsReadyStepWithMatchingDefinitionHash(t *testing.T) {
	step := config.BayPrepareConfig{Name: "vendors", Command: []string{commandPath(t, "true")}}
	hash, err := DefinitionHash(step)
	if err != nil {
		t.Fatalf("DefinitionHash: %v", err)
	}
	h := newWorkerHarness(t, []config.BayPrepareConfig{step})
	if err := manifest.LockedUpdate(h.manifestPath, func(m *manifest.Manifest) error {
		bay := m.FindDock("labs").FindBayByID("b1")
		bay.Prepare = []manifest.PrepareStep{{
			Name:           "vendors",
			Status:         manifest.PrepareStatusReady,
			StartedAt:      100,
			FinishedAt:     200,
			DefinitionHash: hash,
		}}
		return nil
	}); err != nil {
		t.Fatalf("seed ready state: %v", err)
	}

	if err := h.worker.Run(t.Context(), Options{Dock: "labs", Bay: "b1"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := h.prepareStep(t, "vendors")
	if got.StartedAt != 100 || got.FinishedAt != 200 {
		t.Fatalf("ready step was rerun: %#v", got)
	}
	if _, err := os.Stat(h.logPath()); !os.IsNotExist(err) {
		t.Fatalf("log stat error = %v, want not-exist", err)
	}
}

func TestWorker_RunLogOffsetsPointAtRunSeparators(t *testing.T) {
	h := newWorkerHarness(t, nil)
	ts := time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)

	first, firstOffset, err := h.worker.openLogAndWriteStart("labs", ts, "b1", "vendors")
	if err != nil {
		t.Fatalf("first openLogAndWriteStart: %v", err)
	}
	if _, err := first.Write([]byte("first output\n")); err != nil {
		t.Fatalf("write first output: %v", err)
	}
	first.Close()
	second, secondOffset, err := h.worker.openLogAndWriteStart("labs", ts, "b2", "vendors")
	if err != nil {
		t.Fatalf("second openLogAndWriteStart: %v", err)
	}
	second.Close()

	if firstOffset != 0 {
		t.Fatalf("first offset = %d, want 0", firstOffset)
	}
	if secondOffset <= firstOffset {
		t.Fatalf("second offset = %d, want after first offset %d", secondOffset, firstOffset)
	}

	log := h.readLog(t)
	wantSecond := "==== run bay=b2 step=vendors 2026-05-07T10:00:00Z ===="
	gotSecond := strings.Index(log, wantSecond)
	if gotSecond == -1 {
		t.Fatalf("second run separator missing:\n%s", log)
	}
	if int64(gotSecond) != secondOffset {
		t.Fatalf("second offset = %d, want separator index %d\n%s", secondOffset, gotSecond, log)
	}
}

func TestManager_StatusReportsFiveStates(t *testing.T) {
	steps := []config.BayPrepareConfig{
		{Name: "pending", Command: []string{commandPath(t, "true")}},
		{Name: "running", Command: []string{commandPath(t, "true")}},
		{Name: "ready", Command: []string{commandPath(t, "true")}},
		{Name: "failed", Command: []string{commandPath(t, "true")}},
		{Name: "stale", Command: []string{commandPath(t, "true")}},
	}
	h := newWorkerHarness(t, steps)
	readyHash, err := DefinitionHash(steps[2])
	if err != nil {
		t.Fatalf("DefinitionHash: %v", err)
	}
	if err := manifest.LockedUpdate(h.manifestPath, func(m *manifest.Manifest) error {
		bay := m.FindDock("labs").FindBayByID("b1")
		bay.Prepare = []manifest.PrepareStep{
			{Name: "pending", Status: manifest.PrepareStatusPending},
			{Name: "running", Status: manifest.PrepareStatusRunning, PID: 1234, HeartbeatAt: h.worker.Now().Unix()},
			{Name: "ready", Status: manifest.PrepareStatusReady, DefinitionHash: readyHash},
			{Name: "failed", Status: manifest.PrepareStatusFailed},
			{Name: "stale", Status: manifest.PrepareStatusStale},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	lock, acquired, err := tryStepLock(stepLockPath(h.dataDir, "labs", "b1", "running"))
	if err != nil {
		t.Fatalf("tryStepLock: %v", err)
	}
	if !acquired {
		t.Fatal("could not acquire running lock")
	}
	defer lock.Unlock()

	statuses, err := h.manager().Status(Options{Dock: "labs", Bay: "b1"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := map[string]manifest.PrepareStatus{}
	for _, status := range statuses {
		got[status.Name] = status.Status
	}
	for name, want := range map[string]manifest.PrepareStatus{
		"pending": manifest.PrepareStatusPending,
		"running": manifest.PrepareStatusRunning,
		"ready":   manifest.PrepareStatusReady,
		"failed":  manifest.PrepareStatusFailed,
		"stale":   manifest.PrepareStatusStale,
	} {
		if got[name] != want {
			t.Fatalf("%s status = %q, want %q (all statuses: %#v)", name, got[name], want, got)
		}
	}
}

func TestManager_RetryResetsFailedStaleAndPending(t *testing.T) {
	steps := []config.BayPrepareConfig{
		{Name: "pending", Command: []string{commandPath(t, "true")}},
		{Name: "failed", Command: []string{commandPath(t, "true")}},
		{Name: "stale", Command: []string{commandPath(t, "true")}},
		{Name: "ready", Command: []string{commandPath(t, "true")}},
	}
	h := newWorkerHarness(t, steps)
	readyHash, err := DefinitionHash(steps[3])
	if err != nil {
		t.Fatalf("DefinitionHash: %v", err)
	}
	if err := manifest.LockedUpdate(h.manifestPath, func(m *manifest.Manifest) error {
		bay := m.FindDock("labs").FindBayByID("b1")
		bay.Prepare = []manifest.PrepareStep{
			{Name: "pending", Status: manifest.PrepareStatusPending, StartedAt: 1},
			{Name: "failed", Status: manifest.PrepareStatusFailed, FinishedAt: 2},
			{Name: "stale", Status: manifest.PrepareStatusStale, PID: 1234, RunLogOffset: 9},
			{Name: "ready", Status: manifest.PrepareStatusReady, DefinitionHash: readyHash},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	names, err := h.manager().Retry(Options{Dock: "labs", Bay: "b1"})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if strings.Join(names, ",") != "pending,failed,stale" {
		t.Fatalf("retry names = %v, want pending,failed,stale", names)
	}
	for _, name := range []string{"pending", "failed", "stale"} {
		step := h.prepareStep(t, name)
		if step.Status != manifest.PrepareStatusPending || step.PID != 0 || step.RunLogOffset != 0 || step.FinishedAt != 0 {
			t.Fatalf("%s not reset to clean pending: %#v", name, step)
		}
	}
	if ready := h.prepareStep(t, "ready"); ready.Status != manifest.PrepareStatusReady {
		t.Fatalf("ready step should stay ready, got %#v", ready)
	}
}

func TestManager_KillSignalsRunningStepAndMarksStaleOnTimeout(t *testing.T) {
	h := newWorkerHarness(t, []config.BayPrepareConfig{
		{Name: "vendors", Command: []string{commandPath(t, "true")}},
	})
	sleep := exec.Command(commandPath(t, "sleep"), "10")
	if err := sleep.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer func() {
		_ = sleep.Process.Kill()
		_ = sleep.Wait()
	}()

	lock, acquired, err := tryStepLock(stepLockPath(h.dataDir, "labs", "b1", "vendors"))
	if err != nil {
		t.Fatalf("tryStepLock: %v", err)
	}
	if !acquired {
		t.Fatal("could not acquire running lock")
	}
	defer lock.Unlock()

	if err := manifest.LockedUpdate(h.manifestPath, func(m *manifest.Manifest) error {
		bay := m.FindDock("labs").FindBayByID("b1")
		bay.Prepare = []manifest.PrepareStep{{
			Name:        "vendors",
			Status:      manifest.PrepareStatusRunning,
			PID:         sleep.Process.Pid,
			HeartbeatAt: h.worker.Now().Unix(),
		}}
		return nil
	}); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	names, err := h.manager().Kill(context.Background(), Options{Dock: "labs", Bay: "b1"}, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if strings.Join(names, ",") != "vendors" {
		t.Fatalf("kill names = %v, want vendors", names)
	}
	step := h.prepareStep(t, "vendors")
	if step.Status != manifest.PrepareStatusStale || step.PID != 0 {
		t.Fatalf("killed step = %#v, want stale with pid cleared", step)
	}
}

func TestManager_KillWaitZeroMarksStaleImmediately(t *testing.T) {
	h := newWorkerHarness(t, []config.BayPrepareConfig{
		{Name: "vendors", Command: []string{commandPath(t, "true")}},
	})
	sleep := exec.Command(commandPath(t, "sleep"), "10")
	if err := sleep.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer func() {
		_ = sleep.Process.Kill()
		_ = sleep.Wait()
	}()
	lock, _, err := tryStepLock(stepLockPath(h.dataDir, "labs", "b1", "vendors"))
	if err != nil {
		t.Fatalf("tryStepLock: %v", err)
	}
	defer lock.Unlock()

	if err := manifest.LockedUpdate(h.manifestPath, func(m *manifest.Manifest) error {
		bay := m.FindDock("labs").FindBayByID("b1")
		bay.Prepare = []manifest.PrepareStep{{
			Name:        "vendors",
			Status:      manifest.PrepareStatusRunning,
			PID:         sleep.Process.Pid,
			HeartbeatAt: h.worker.Now().Unix(),
		}}
		return nil
	}); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	start := time.Now()
	names, err := h.manager().Kill(context.Background(), Options{Dock: "labs", Bay: "b1"}, 0)
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("wait=0 Kill took %s, want immediate return", elapsed)
	}
	if strings.Join(names, ",") != "vendors" {
		t.Fatalf("kill names = %v, want vendors", names)
	}
	if step := h.prepareStep(t, "vendors"); step.Status != manifest.PrepareStatusStale {
		t.Fatalf("step.Status = %s, want stale", step.Status)
	}
}

func TestManager_KillReturnsCtxErrWhenCanceled(t *testing.T) {
	h := newWorkerHarness(t, []config.BayPrepareConfig{
		{Name: "vendors", Command: []string{commandPath(t, "true")}},
	})
	sleep := exec.Command(commandPath(t, "sleep"), "10")
	if err := sleep.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer func() {
		_ = sleep.Process.Kill()
		_ = sleep.Wait()
	}()

	lock, _, err := tryStepLock(stepLockPath(h.dataDir, "labs", "b1", "vendors"))
	if err != nil {
		t.Fatalf("tryStepLock: %v", err)
	}
	defer lock.Unlock()

	if err := manifest.LockedUpdate(h.manifestPath, func(m *manifest.Manifest) error {
		bay := m.FindDock("labs").FindBayByID("b1")
		bay.Prepare = []manifest.PrepareStep{{
			Name:        "vendors",
			Status:      manifest.PrepareStatusRunning,
			PID:         sleep.Process.Pid,
			HeartbeatAt: h.worker.Now().Unix(),
		}}
		return nil
	}); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	names, err := h.manager().Kill(ctx, Options{Dock: "labs", Bay: "b1"}, time.Hour)
	if err != context.Canceled {
		t.Fatalf("Kill err = %v, want context.Canceled", err)
	}
	if strings.Join(names, ",") != "vendors" {
		t.Fatalf("kill names = %v, want vendors (signaled before cancel returned)", names)
	}
	if step := h.prepareStep(t, "vendors"); step.Status != manifest.PrepareStatusRunning {
		t.Fatalf("step.Status = %s, want running (cancel skipped markStepsStale)", step.Status)
	}
}

func TestManager_PlanGatesRepoLocalConfigByTrust(t *testing.T) {
	h := newWorkerHarness(t, nil)
	bayPath := h.loadManifest(t).FindDock("labs").FindBayByID("b1").Path
	if err := os.WriteFile(filepath.Join(bayPath, config.RepoLocalFilename), []byte(`
[[bay_prepare]]
name = "vendors"
command = ["true"]
`), 0o644); err != nil {
		t.Fatalf("write repo-local config: %v", err)
	}

	plan, err := h.manager().Plan(Options{Dock: "labs", Bay: "b1"})
	if err != nil {
		t.Fatalf("Plan untrusted: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Fatalf("untrusted repo-local steps = %#v, want none", plan.Steps)
	}

	trust := true
	h.worker.Config.TrustRepoBayToml = &trust
	plan, err = h.manager().Plan(Options{Dock: "labs", Bay: "b1"})
	if err != nil {
		t.Fatalf("Plan trusted: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Name != "vendors" {
		t.Fatalf("trusted repo-local steps = %#v, want vendors", plan.Steps)
	}
}

type workerHarness struct {
	t            *testing.T
	dataDir      string
	manifestPath string
	worker       Worker
}

func newWorkerHarness(t *testing.T, steps []config.BayPrepareConfig) workerHarness {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	manifestPath := filepath.Join(dataDir, "manifest.json")
	bayPath := filepath.Join(root, "checkout")
	if err := os.MkdirAll(bayPath, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}

	m := manifest.New()
	if err := m.AddDock(manifest.Dock{Name: "labs", Path: bayPath}); err != nil {
		t.Fatalf("AddDock: %v", err)
	}
	if err := m.FindDock("labs").AddBay(manifest.Bay{
		ID:       "b1",
		Type:     manifest.BayTypeWorktree,
		Path:     bayPath,
		Surfaces: []manifest.Surface{},
	}); err != nil {
		t.Fatalf("AddBay: %v", err)
	}
	if err := manifest.Save(manifestPath, m); err != nil {
		t.Fatalf("Save manifest: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Docks["labs"] = config.DockConfig{BayPrepare: steps}
	now := time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
	return workerHarness{
		t:            t,
		dataDir:      dataDir,
		manifestPath: manifestPath,
		worker: Worker{
			Config:            cfg,
			ManifestPath:      manifestPath,
			DataDir:           dataDir,
			HeartbeatInterval: -1,
			Now:               func() time.Time { return now },
			PID:               4242,
		},
	}
}

func (h workerHarness) loadManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Load(h.manifestPath)
	if err != nil {
		t.Fatalf("Load manifest: %v", err)
	}
	return m
}

func (h workerHarness) prepareStep(t *testing.T, name string) manifest.PrepareStep {
	t.Helper()
	m := h.loadManifest(t)
	bay := m.FindDock("labs").FindBayByID("b1")
	for _, step := range bay.Prepare {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("prepare step %q not found in %#v", name, bay.Prepare)
	return manifest.PrepareStep{}
}

func (h workerHarness) readLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(h.logPath())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return string(data)
}

func (h workerHarness) logPath() string {
	return filepath.Join(h.dataDir, "logs", "labs", "2026-05-07", "prepare.log")
}

func (h workerHarness) manager() Manager {
	return Manager{
		Config:       h.worker.Config,
		ManifestPath: h.manifestPath,
		DataDir:      h.dataDir,
		Now:          h.worker.Now,
	}
}

func commandPath(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("looking up %s: %v", name, err)
	}
	return path
}
