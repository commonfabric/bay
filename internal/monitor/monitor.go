// Package monitor provides background detection of agent panes waiting for user input.
// It reads the manifest to find windows with agent/cmd panes, captures their content,
// checks for configured patterns, and sets tmux window styles/options to highlight waiting windows.
//
// This is the legacy approach to waiting detection. Agents that send a terminal bell
// (codex natively, claude via a PermissionRequest hook) are detected directly by tmux's
// window_bell_flag without needing the monitor. The monitor remains useful as a fallback
// for agents that don't send bells. It may be removed if all supported agents adopt
// bell-based signaling.
package monitor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/git"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/tmux"
)

// ansiRE matches ANSI escape sequences: CSI sequences, OSC sequences, and simple escapes.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\].*?\x07|\x1b[^[\]]`)

const (
	// waitingOption is the tmux user option set on windows where an agent is waiting.
	waitingOption = "@bay-waiting"

	// highlightStyle is the tmux window-status-style applied to waiting windows.
	highlightStyle = "fg=red,bg=colour238,bold"

	// captureLinesCount is how many lines to capture from each pane.
	captureLinesCount = 20

	// MergeCheckCycles is how many check cycles between merge detection runs.
	// With a default 3-second interval, 100 cycles = ~5 minutes.
	MergeCheckCycles = 100

	// DescribeCheckCycles is how many check cycles between auto-description
	// dispatch rounds (~5 minutes at the default interval). See
	// docs/design/auto-descriptions.md.
	DescribeCheckCycles = 100

	// describeMinInterval is the base cadence for re-describing a bay: the
	// floor when the user has touched it since the last run, and the
	// starting interval for probing agent-only activity the monitor can't
	// see. It bounds the cheap transcript reads each dispatched worker does.
	describeMinInterval = 10 * 60

	// describeMaxInterval caps the backoff: a bay that keeps coming back
	// unchanged is re-probed at most this often (until it falls out of the
	// activity window), instead of every describeMinInterval forever.
	describeMaxInterval = 60 * 60

	// describeMaxPerCycle caps describe workers dispatched per round, so a
	// monitor restart with many eligible bays doesn't spawn a herd.
	describeMaxPerCycle = 4

	// activityWindow is how long a bay must have been active to
	// trigger fetch + merge checks (2 hours in seconds).
	activityWindow = 2 * 60 * 60

	prepareLogRetention     = 14 * 24 * time.Hour
	prepareLogPruneInterval = 23 * time.Hour
)

// Monitor watches agent panes for input prompts and highlights their tmux windows.
// It also periodically detects PR numbers for bays with branches.
type Monitor struct {
	tmux         tmux.Interface
	git          git.Interface
	manifestPath string
	patternsPath string
	pidPath      string
	intervalSecs int

	runtimeStatusPath string
	runtimeVersion    string
	runtimeStatus     RuntimeStatus

	// engine, if set, has its SyncAll() called at the start of every
	// CheckOnce cycle so that branch changes (and the resulting tmux
	// window renames) propagate without waiting for a CLI command.
	// Optional — when nil, the monitor still does prompt detection
	// and PR/merge probing on its own cadence.
	engine *engine.Engine

	// tracked records which windows currently carry the monitor's red
	// window-status-style, so we skip re-applying the style and know which
	// windows to clear when their prompt goes away. It does NOT gate the
	// @bay-waiting flag: that is re-asserted every cycle because the
	// after-select-window hook clears it behind our back (see setHighlight).
	tracked map[string]bool

	// cycle counts check cycles for cadence-gated operations.
	cycle int

	// binaryMTime is the mtime of the bay executable at monitor start.
	// If a later tick sees a different mtime, the binary has been
	// replaced (e.g., by `go install` or a package upgrade) and the
	// monitor exec's itself in place to load the new code. Zero means
	// "not yet recorded."
	binaryMTime time.Time
}

// New creates a new Monitor without git support (PR detection disabled).
func New(t tmux.Interface, manifestPath, patternsPath, pidPath string, intervalSecs int) *Monitor {
	return &Monitor{
		tmux:         t,
		manifestPath: manifestPath,
		patternsPath: patternsPath,
		pidPath:      pidPath,
		intervalSecs: intervalSecs,
		tracked:      make(map[string]bool),
	}
}

// NewWithGit creates a Monitor with git support for background PR detection.
func NewWithGit(t tmux.Interface, g git.Interface, manifestPath, patternsPath, pidPath string, intervalSecs int) *Monitor {
	m := New(t, manifestPath, patternsPath, pidPath, intervalSecs)
	m.git = g
	return m
}

// SetEngine attaches an engine whose SyncAll() will run at the start of
// every CheckOnce cycle. Pass nil to disable. Used by the CLI to wire
// background branch detection into the same process that already does
// prompt-waiting detection.
func (m *Monitor) SetEngine(e *engine.Engine) {
	m.engine = e
}

// SetRuntimeStatus enables monitor-owned runtime metadata.
func (m *Monitor) SetRuntimeStatus(path, version string) {
	m.runtimeStatusPath = path
	m.runtimeVersion = version
}

// Run is the main loop. It periodically checks all windows and exits when ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) error {
	if m.runtimeStatusPath != "" {
		defer func() {
			_ = os.Remove(m.runtimeStatusPath)
		}()
	}
	// Resolve and stash the executable path once. Record the mtime
	// baseline before the loop so a replacement landing during the
	// first interval isn't silently adopted as the new baseline.
	exe, _ := os.Executable()
	if exe != "" {
		m.recordBinaryMTime(exe)
		m.initializeRuntimeStatus(exe, time.Now())
	}
	interval := time.Duration(m.intervalSecs) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// If the bay binary has been replaced, exec the new one
			// in place. syscall.Exec preserves the PID and falls
			// through on failure (ENOENT mid-swap, etc.) so the next
			// tick retries. Workers still running would keep this PID
			// as their parent but lose the goroutines waiting on them,
			// and stay defunct once they exit, so hold the exec until
			// they have been reaped.
			if exe != "" && m.hasBinaryChanged(exe) && engine.WorkersInFlight() == 0 {
				_ = syscall.Exec(exe, os.Args, os.Environ())
			}
			// Errors during a check cycle are non-fatal; we log and continue.
			_ = m.CheckOnce()
			m.refreshRuntimeStatusIfDue(exe, time.Now())
		}
	}
}

// recordBinaryMTime stats path and stores its mtime as the baseline.
// Stat errors leave the baseline zeroed, and hasBinaryChanged then
// returns false until a later recordBinaryMTime call succeeds.
func (m *Monitor) recordBinaryMTime(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	m.binaryMTime = info.ModTime()
}

// hasBinaryChanged reports whether path's current mtime differs from
// the baseline set by recordBinaryMTime. Returns false before the
// baseline is recorded or on transient stat errors, so a flaky moment
// can't trigger a spurious restart.
func (m *Monitor) hasBinaryChanged(path string) bool {
	if m.binaryMTime.IsZero() {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.ModTime().Equal(m.binaryMTime)
}

func (m *Monitor) initializeRuntimeStatus(exe string, now time.Time) {
	if m.runtimeStatusPath == "" || m.runtimeVersion == "" {
		return
	}
	pid := os.Getpid()
	startedAt := now
	generation := 1
	if previous, err := ReadRuntimeStatus(m.runtimeStatusPath); err == nil && previous.PID == pid {
		if !previous.StartedAt.IsZero() {
			startedAt = previous.StartedAt
		}
		if previous.Generation > 0 {
			generation = previous.Generation + 1
		}
	}
	m.runtimeStatus = RuntimeStatus{
		Schema:     runtimeStatusSchema,
		PID:        pid,
		Version:    m.runtimeVersion,
		StartedAt:  startedAt,
		LastSeenAt: now,
		Generation: generation,
	}
	_ = m.writeRuntimeStatus(exe, now)
}

func (m *Monitor) refreshRuntimeStatusIfDue(exe string, now time.Time) {
	if m.runtimeStatusPath == "" || exe == "" || m.runtimeStatus.PID == 0 {
		return
	}
	if now.Sub(m.runtimeStatus.LastSeenAt) < RuntimeStatusHeartbeat {
		return
	}
	_ = m.writeRuntimeStatus(exe, now)
}

func (m *Monitor) writeRuntimeStatus(exe string, now time.Time) error {
	identity, err := CurrentBinaryIdentity(exe)
	if err != nil {
		return err
	}
	m.runtimeStatus.LastSeenAt = now
	m.runtimeStatus.BinaryIdentity = identity
	return WriteRuntimeStatusAtomic(m.runtimeStatusPath, m.runtimeStatus)
}

// CheckOnce runs a single check cycle: reload patterns, read manifest, check all windows.
func (m *Monitor) CheckOnce() error {
	// Branch sync first, so the manifest we load below reflects any
	// branch changes the user just made (and so the tmux windows have
	// already been renamed by the time we iterate them for prompt
	// detection). Cheap — engine.SyncAll's per-bay probe is one
	// local git call.
	if m.engine != nil {
		m.engine.SyncAll()
	}
	_ = m.prunePrepareLogsIfDue(time.Now())

	patterns, err := LoadPatterns(m.patternsPath)
	if err != nil {
		return fmt.Errorf("loading patterns: %w", err)
	}

	mf, err := manifest.Load(m.manifestPath)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}

	// Collect the set of windows we visit this cycle, so we can clear stale entries.
	visited := make(map[string]bool)

	for i := range mf.Docks {
		dock := &mf.Docks[i]
		for j := range dock.Bays {
			bay := &dock.Bays[j]
			for _, s := range bay.Surfaces {
				if s.Tmux == nil || s.Tmux.WindowID == "" {
					continue
				}
				if !hasMonitorableSurface(s) {
					continue
				}

				visited[s.Tmux.WindowID] = true
				waiting := m.checkWindow(s.Tmux.WindowID, patterns)

				if waiting {
					if err := m.setHighlight(s.Tmux.WindowID); err != nil {
						continue
					}
				} else {
					if err := m.clearHighlight(s.Tmux.WindowID); err != nil {
						continue
					}
				}
			}
		}
	}

	// Clear highlight on windows we previously tracked but no longer appear in manifest.
	for wid := range m.tracked {
		if !visited[wid] {
			_ = m.clearHighlight(wid)
		}
	}

	// Periodic background tasks (slower cadence than prompt checking).
	m.cycle++
	manifestDirty := false

	if m.git != nil && m.cycle%MergeCheckCycles == 0 {
		if m.detectMerges(mf) {
			manifestDirty = true
		}
	}

	if manifestDirty {
		_ = manifest.Save(m.manifestPath, mf)
	}

	// Auto-description dispatch (detached workers; reads mf, never
	// mutates it here — the workers update the manifest themselves).
	// Reload config first so enabling the backstop (e.g. via `bay setup`)
	// takes effect within one gate interval, without a monitor restart.
	if m.engine != nil && m.cycle%DescribeCheckCycles == 0 {
		_ = m.engine.ReloadConfig()
		m.dispatchDescribeWorkers(mf, time.Now().Unix())
	}

	return nil
}

// dispatchDescribeWorkers fires detached describe workers for eligible
// bays on the slow cadence. The selection is coarse and reads no
// transcripts; each worker decides precisely whether to (re)summarize.
func (m *Monitor) dispatchDescribeWorkers(mf *manifest.Manifest, now int64) {
	if m.engine == nil || m.engine.Config == nil || !m.engine.Config.Describe.EffectiveEnabled() {
		return
	}
	for _, c := range selectDescribeCandidates(mf, now, describeMaxPerCycle) {
		_ = m.engine.DispatchDescribeWorker(c.dock, c.bay)
	}
}

type describeCandidate struct {
	dock string
	bay  string
}

// selectDescribeCandidates returns bays due for an auto-description refresh.
// Never-described bays and failed empty descriptions form a bounded backlog
// even after their activity window expires. Successfully probed quiet bays and
// auto-description refreshes still require recent activity. Candidates are
// oldest-summarized first, capped at max.
func selectDescribeCandidates(mf *manifest.Manifest, now int64, max int) []describeCandidate {
	type scored struct {
		describeCandidate
		summarizedAt int64
	}
	var cands []scored
	for i := range mf.Docks {
		dock := &mf.Docks[i]
		for j := range dock.Bays {
			bay := &dock.Bays[j]
			if bay.ID == manifest.HomeBayID || bay.Type == manifest.BayTypeHome || bay.Path == "" {
				continue
			}
			if bay.Description != "" && bay.DescriptionSource != manifest.DescriptionSourceAuto {
				continue // user-set or legacy — never auto-overwrite
			}
			recentlyActive := bay.LastActive != 0 && now-bay.LastActive <= activityWindow
			needsBackfill := bay.Description == "" &&
				(bay.DescriptionSummarizedAt == 0 || bay.DescriptionStableStreak == 0)
			if !recentlyActive && !needsBackfill {
				continue
			}
			if bay.DescriptionSummarizedAt != 0 {
				// Already described once. Re-dispatch only when due. If the
				// user has touched the bay since (LastActive newer than the
				// last run), refresh at the base cadence; otherwise we're
				// only probing for agent-only activity the monitor can't
				// see without reading logs, so back off a quiet bay.
				interval := int64(describeMinInterval)
				if bay.LastActive < bay.DescriptionSummarizedAt {
					interval = describeBackoff(bay.DescriptionStableStreak)
				}
				if now-bay.DescriptionSummarizedAt < interval {
					continue // described recently enough
				}
			}
			cands = append(cands, scored{describeCandidate{dock.Name, bay.ID}, bay.DescriptionSummarizedAt})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		return cands[i].summarizedAt < cands[j].summarizedAt
	})
	if max > 0 && len(cands) > max {
		cands = cands[:max]
	}
	out := make([]describeCandidate, len(cands))
	for i, c := range cands {
		out[i] = c.describeCandidate
	}
	return out
}

// describeBackoff returns how long to wait before re-probing a bay that
// has come back unchanged for `streak` consecutive worker runs: the base
// min-interval doubled per stable run, capped at describeMaxInterval. A
// streak of 0 (just changed, or never stable) yields the base interval.
func describeBackoff(streak int) int64 {
	iv := int64(describeMinInterval)
	for i := 0; i < streak && iv < describeMaxInterval; i++ {
		iv *= 2
	}
	if iv > describeMaxInterval {
		iv = describeMaxInterval
	}
	return iv
}

func (m *Monitor) prunePrepareLogsIfDue(now time.Time) error {
	if m.manifestPath == "" {
		return nil
	}
	return prunePrepareLogs(filepath.Join(filepath.Dir(m.manifestPath), "logs"), now)
}

func prunePrepareLogs(logsRoot string, now time.Time) error {
	if logsRoot == "" {
		return nil
	}
	sentinel := filepath.Join(logsRoot, ".last-prune")
	if info, err := os.Stat(sentinel); err == nil && now.Sub(info.ModTime()) < prepareLogPruneInterval {
		return nil
	}
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		return err
	}
	docks, err := os.ReadDir(logsRoot)
	if err != nil {
		return err
	}
	for _, dockEntry := range docks {
		if !dockEntry.IsDir() {
			continue
		}
		dockPath := filepath.Join(logsRoot, dockEntry.Name())
		days, err := os.ReadDir(dockPath)
		if err != nil {
			continue
		}
		for _, dayEntry := range days {
			if !dayEntry.IsDir() {
				continue
			}
			day, err := time.ParseInLocation("2006-01-02", dayEntry.Name(), now.Location())
			if err != nil {
				continue
			}
			if now.Sub(day) > prepareLogRetention {
				_ = os.RemoveAll(filepath.Join(dockPath, dayEntry.Name()))
			}
		}
	}
	if err := os.WriteFile(sentinel, []byte(now.Format(time.RFC3339)), 0o644); err != nil {
		return err
	}
	// Stamp mtime from the caller's clock, not the kernel's, so the
	// throttle gate (info.ModTime() comparison above) honors a fake
	// `now` in tests.
	return os.Chtimes(sentinel, now, now)
}

// detectPRs checks bays with a branch but no PR and tries to find one.
// Uses the PRCheckedAt timestamp + manifest.PRCheckTTL to avoid re-hammering
// bays that have already been checked. The TTL ensures that PRs opened
// after the first check are eventually picked up.
//
// Returns true if any bay's PR state changed.
func (m *Monitor) detectPRs(mf *manifest.Manifest) bool {
	now := time.Now().Unix()
	changed := false
	for i := range mf.Docks {
		dock := &mf.Docks[i]
		for j := range dock.Bays {
			bay := &dock.Bays[j]
			if bay.Path == "" || !bay.Worktree.NeedsPRCheck(now) {
				continue
			}
			pr, err := m.git.PRForBranch(bay.Path, bay.Worktree.Branch)
			if err != nil {
				// gh unavailable or transient — leave PRCheckedAt
				// unchanged so we retry next cycle.
				continue
			}
			// Definitive answer: either a PR number, or confirmed no PR.
			bay.Worktree.PR = pr
			bay.Worktree.PRCheckedAt = time.Now().Unix()
			changed = true
		}
	}
	return changed
}

// detectMerges checks active bays for branches merged into default.
// Only checks bays with recent activity (within activityWindow).
// Performs git fetch before merge check. Returns true if any status changed.
func (m *Monitor) detectMerges(mf *manifest.Manifest) bool {
	now := time.Now().Unix()
	changed := false

	// Collect repos that need fetching (deduplicate).
	fetched := map[string]bool{}

	for i := range mf.Docks {
		dock := &mf.Docks[i]
		for j := range dock.Bays {
			bay := &dock.Bays[j]
			if bay.Worktree == nil || bay.Worktree.Branch == "" || bay.Path == "" {
				continue
			}
			if bay.Worktree.Merged {
				continue
			}
			// Activity gate: skip bays not active recently.
			if bay.LastActive == 0 || (now-bay.LastActive) > activityWindow {
				continue
			}

			// Fetch once per repo path.
			if !fetched[bay.Path] {
				_ = m.git.Fetch(bay.Path)
				fetched[bay.Path] = true
			}

			merged, err := m.git.IsMergedIntoDefault(bay.Path, bay.Worktree.Branch)
			if err != nil || !merged {
				continue
			}

			bay.Worktree.Merged = true
			changed = true
		}
	}
	return changed
}

// hasMonitorableSurface returns true if the surface is an agent or cmd that should be monitored.
func hasMonitorableSurface(s manifest.Surface) bool {
	return s.Type == manifest.SurfaceTypeAgent || s.Type == manifest.SurfaceTypeCmd
}

// checkWindow captures all panes in a tmux window and returns true if any match a pattern.
func (m *Monitor) checkWindow(windowID string, patterns []*regexp.Regexp) bool {
	if len(patterns) == 0 {
		return false
	}

	panes, err := m.tmux.ListPanes(windowID)
	if err != nil {
		return false
	}

	for _, pane := range panes {
		content, err := m.tmux.CapturePane(pane.ID, captureLinesCount)
		if err != nil {
			continue
		}
		cleaned := StripANSI(content)
		if CheckPane(cleaned, patterns) {
			return true
		}
	}
	return false
}

// setHighlight marks a window as waiting by setting the tmux user option and style.
//
// The @bay-waiting flag is re-asserted on every call, even for a window we
// already track. We are not its only writer: setup installs an
// after-select-window hook that clears @bay-waiting on focus (see
// bayClearWaitingHook). So a window the agent is still blocking on has its
// flag cleared out from under us the moment the user merely glances at the
// tab — and if we short-circuited on m.tracked we'd never set it back, leaving
// the tab styled red (the style does not self-clear on focus) but invisible to
// `bay go --next-waiting`. Re-asserting each cycle keeps the flag in sync with
// what the pattern still sees. The style is idempotent and gated on tracked
// purely to avoid a redundant tmux call.
func (m *Monitor) setHighlight(windowID string) error {
	if err := m.tmux.SetWindowOption(windowID, waitingOption, "1"); err != nil {
		return err
	}
	if m.tracked[windowID] {
		return nil // style already applied
	}
	if err := m.tmux.SetWindowOption(windowID, "window-status-style", highlightStyle); err != nil {
		return err
	}
	m.tracked[windowID] = true
	return nil
}

// clearHighlight removes the waiting flag and resets the window style.
func (m *Monitor) clearHighlight(windowID string) error {
	if !m.tracked[windowID] {
		return nil // not highlighted
	}
	if err := m.tmux.SetWindowOption(windowID, waitingOption, "0"); err != nil {
		return err
	}
	if err := m.tmux.UnsetWindowOption(windowID, "window-status-style"); err != nil {
		return err
	}
	delete(m.tracked, windowID)
	return nil
}

// Start forks the monitor as a background process and writes the PID file.
func (m *Monitor) Start() error {
	// Find our own executable.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable: %w", err)
	}

	attr := &os.ProcAttr{
		Dir:   "/",
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}
	proc, err := os.StartProcess(exe, []string{exe, "monitor", "run"}, attr)
	if err != nil {
		return fmt.Errorf("starting monitor process: %w", err)
	}
	// Release the process so it continues after we exit.
	// The child process (bay monitor run) writes its own PID file,
	// so we must not write it here to avoid a race condition.
	if err := proc.Release(); err != nil {
		return fmt.Errorf("releasing monitor process: %w", err)
	}

	return nil
}

// Stop reads the PID file, sends SIGTERM, and removes the PID file.
func (m *Monitor) Stop() error {
	pid, err := ReadPIDFile(m.pidPath)
	if err != nil {
		return fmt.Errorf("reading PID file: %w", err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		// Process may have already exited; try to clean up anyway.
		_ = RemovePIDFile(m.pidPath)
		return fmt.Errorf("sending signal to process %d: %w", pid, err)
	}
	if err := RemovePIDFile(m.pidPath); err != nil {
		return err
	}
	return nil
}

// Status checks whether the monitor is running.
func (m *Monitor) Status() (running bool, pid int, err error) {
	pid, err = ReadPIDFile(m.pidPath)
	if err != nil {
		// No PID file means not running.
		return false, 0, nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, pid, nil
	}
	// On Unix, FindProcess always succeeds. Send signal 0 to check if alive.
	err = proc.Signal(syscall.Signal(0))
	if err != nil {
		return false, pid, nil
	}
	return true, pid, nil
}

// StripANSI removes ANSI escape sequences from a string.
func StripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// LoadPatterns reads a patterns file (one regex per line, # comments, blank lines ignored).
// Returns nil (no patterns) if the file does not exist.
func LoadPatterns(path string) ([]*regexp.Regexp, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening patterns file: %w", err)
	}
	defer f.Close()

	var patterns []*regexp.Regexp
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		re, err := regexp.Compile(line)
		if err != nil {
			return nil, fmt.Errorf("invalid regex on line %d (%q): %w", lineNum, line, err)
		}
		patterns = append(patterns, re)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading patterns file: %w", err)
	}
	return patterns, nil
}

// CheckPane returns true if the content matches any of the patterns.
func CheckPane(content string, patterns []*regexp.Regexp) bool {
	for _, p := range patterns {
		if p.MatchString(content) {
			return true
		}
	}
	return false
}

// WritePIDFile writes a PID to a file.
func WritePIDFile(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating PID file directory: %w", err)
	}
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644)
}

// ReadPIDFile reads a PID from a file.
func ReadPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID in %s: %w", path, err)
	}
	return pid, nil
}

// RemovePIDFile removes the PID file.
func RemovePIDFile(path string) error {
	return os.Remove(path)
}
