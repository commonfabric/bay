package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
)

// DockInfo holds summary information about a dock.
type DockInfo struct {
	Name        string        `json:"name"`
	Agent       string        `json:"agent,omitempty"`
	Path        string        `json:"path,omitempty"`
	WorktreeDir string        `json:"worktree_dir,omitempty"`
	Surfaces    []SurfaceInfo `json:"surfaces,omitempty"` // dock-level surfaces
	Bays        []BayInfo     `json:"bays"`
}

// SurfaceInfo holds runtime information about a tracked surface.
type SurfaceInfo struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Backend string `json:"backend"`
	Agent   string `json:"agent,omitempty"`
	Command string `json:"command,omitempty"`
	Status  string `json:"status"`
}

// BayInfo holds summary information about a bay.
type BayInfo struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"`
	Type         string        `json:"type"`
	Path         string        `json:"path,omitempty"`
	Branch       string        `json:"branch,omitempty"`
	PR           string        `json:"pr,omitempty"`
	Dirty        bool          `json:"dirty"`
	Pending      bool          `json:"pending"`
	Setup        string        `json:"setup,omitempty"`
	Waiting      bool          `json:"waiting,omitempty"`
	Missing      bool          `json:"missing,omitempty"`
	Stale        bool          `json:"stale,omitempty"`
	DefaultAgent string        `json:"default_agent,omitempty"`
	SyncStatus   string        `json:"sync_status"`
	SurfaceCount int           `json:"surface_count"`
	Surfaces     []SurfaceInfo `json:"surfaces,omitempty"`
}

// Label returns the canonical display label for the bay — the same string
// shown in the picker, tmux tabs, and status-line `full` field
// (e.g. "home", "b1", or "b1.auth-fix"). Use this for any user-facing
// reference to a bay so unnamed bays don't render as a blank.
//
// The label is derived from the path-backed dir tag and the optional
// Name; it never depends on the (possibly empty) Name alone. Type is
// forwarded so the home pseudo-bay short-circuits to "home".
func (b BayInfo) Label() string {
	return BayCompactLabel(&manifest.Bay{
		Type: manifest.BayType(b.Type),
		Name: b.Name,
		Path: b.Path,
	})
}

// DockNew creates a new dock configuration and tmux session.
func (e *Engine) DockNew(name, path, worktreeDir, agent, terminal string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot determine current directory")
		}
		path = cwd
	}
	path = config.NormalizePath(path)
	if worktreeDir != "" {
		worktreeDir = config.NormalizePath(worktreeDir)
	}

	if agent != "" {
		if _, ok := e.Config.ResolveAgent(agent); !ok {
			return fmt.Errorf("unknown agent %q", agent)
		}
	}

	m, err := e.LoadManifest()
	if err != nil {
		return err
	}
	if m.FindDock(name) != nil {
		return fmt.Errorf("dock %q already exists", name)
	}
	if err := validateDockCheckoutPath(m, path); err != nil {
		return err
	}

	exists, err := e.Tmux.HasSession(name)
	if err != nil {
		return fmt.Errorf("checking tmux session: %w", err)
	}
	if exists {
		return fmt.Errorf("tmux session %q already exists and is not a bay dock", name)
	}

	sessionID, err := e.ensureSession(name, "")
	if err != nil {
		return err
	}

	// Roll back tmux/terminal/config side effects if the manifest update
	// fails — otherwise a losing race against a concurrent dock create on
	// the same path leaves a stray session, terminal process, or config entry.
	var host *manifest.GUIAttrs
	var previousDockCfg config.DockConfig
	hadDockCfg := false
	success := false
	defer func() {
		if success {
			return
		}
		_ = e.Tmux.KillSession(name)
		if host != nil && host.PID != 0 {
			if proc, err := os.FindProcess(host.PID); err == nil {
				_ = proc.Kill()
			}
		}
		if terminal != "" {
			if hadDockCfg {
				e.Config.Docks[name] = previousDockCfg
			} else {
				delete(e.Config.Docks, name)
			}
			if e.configPath != "" {
				_ = config.Save(e.configPath, e.Config)
			}
		}
	}()

	// Save terminal override to config if set.
	if terminal != "" {
		previousDockCfg, hadDockCfg = e.Config.Docks[name]
		e.Config.Docks[name] = config.DockConfig{Terminal: terminal}
		if e.configPath != "" {
			if err := config.Save(e.configPath, e.Config); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}
		}
	}

	// Record host terminal if configured.
	if terminal != "" {
		host = &manifest.GUIAttrs{AppCommand: terminal}
		// Attempt to launch the terminal. Best-effort — don't fail dock creation.
		if pid, err := launchTerminal(terminal, name); err == nil {
			host.PID = pid
		}
	}

	if err := e.withManifest(func(m *manifest.Manifest) error {
		if err := validateDockCheckoutPath(m, path); err != nil {
			return err
		}
		return m.AddDock(manifest.Dock{
			Name:        name,
			Path:        path,
			WorktreeDir: worktreeDir,
			Agent:       agent,
			Host:        host,
			SessionID:   sessionID,
		})
	}); err != nil {
		return err
	}
	success = true
	return nil
}

func validateDockCheckoutPath(m *manifest.Manifest, path string) error {
	expanded := config.ExpandPath(path)
	info, err := os.Stat(expanded)
	if err != nil {
		return fmt.Errorf("checkout path %q: %w", expanded, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("checkout path %q is not a directory", expanded)
	}
	gitPath := filepath.Join(expanded, ".git")
	gitInfo, err := os.Stat(gitPath)
	if err != nil {
		return fmt.Errorf("checkout path %q is not a main git checkout (missing .git directory)", expanded)
	}
	if !gitInfo.IsDir() {
		return fmt.Errorf("checkout path %q is a git worktree; create docks from the main checkout", expanded)
	}

	canonical := config.CanonicalPath(expanded)
	for i := range m.Docks {
		dock := &m.Docks[i]
		if dock.Path != "" && config.CanonicalPath(dock.Path) == canonical {
			return fmt.Errorf("checkout path %q is already owned by dock %q", expanded, dock.Name)
		}
		for j := range dock.Bays {
			bay := &dock.Bays[j]
			if bay.Path != "" && config.CanonicalPath(bay.Path) == canonical {
				return fmt.Errorf("checkout path %q is already registered as bay %s:%s", expanded, dock.Name, bay.ID)
			}
		}
	}
	return nil
}

// launchTerminal launches a terminal app attached to a tmux session.
// Returns the PID of the launched process.
var launchTerminal = func(terminal, session string) (int, error) {
	cmd := exec.Command(terminal, "-e", "tmux", "attach", "-t", session)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

// DockRename renames a dock in manifest, config overrides, and tmux.
func (e *Engine) DockRename(oldName, newName string) error {
	if err := ValidateName(newName); err != nil {
		return err
	}

	// Rename tmux session.
	exists, _ := e.Tmux.HasSession(oldName)
	if exists {
		if err := e.Tmux.RenameSession(oldName, newName); err != nil {
			return fmt.Errorf("renaming tmux session: %w", err)
		}
	}

	// Rename config overrides if any.
	if dockCfg, ok := e.Config.Docks[oldName]; ok {
		e.Config.Docks[newName] = dockCfg
		delete(e.Config.Docks, oldName)
		if e.configPath != "" {
			if err := config.Save(e.configPath, e.Config); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}
		}
	}

	// Rename in manifest.
	return e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(oldName)
		if dock == nil {
			return fmt.Errorf("dock %q not found", oldName)
		}
		if m.FindDock(newName) != nil {
			return fmt.Errorf("dock %q already exists", newName)
		}
		dock.Name = newName
		return nil
	})
}

// DockClose closes all bays in a dock and kills the tmux session.
//
// All manifest mutations (per bay + the dock itself) happen before
// any tmux kill, so the user-visible state is correct even if bay is
// invoked from inside a pane in this dock and dies during KillSession.
func (e *Engine) DockClose(name string, force bool) error {
	m, _ := e.LoadManifest()
	var dock *manifest.Dock
	if m != nil {
		dock = m.FindDock(name)
	}
	if dock == nil {
		return fmt.Errorf("unknown dock %q", name)
	}

	// Collect bay IDs first to avoid modifying the slice during
	// iteration. Names may be empty; IDs are the stable handle.
	var bayIDs []string
	for _, bay := range dock.Bays {
		if manifest.IsHomeBay(&bay) {
			continue
		}
		bayIDs = append(bayIDs, bay.ID)
	}

	// Manifest pass: archive + remove each bay. On the success
	// path we discard the returned window IDs because KillSession at
	// the end takes out every pane in the session in one shot. On a
	// non-force failure mid-loop we use them to kill just the windows
	// for bays that *were* removed from the manifest, so we don't
	// leave orphaned tmux windows with no manifest reference.
	var killedWindowIDs []string
	for _, bayID := range bayIDs {
		result, err := e.closeBayState(name, bayID, force)
		if err != nil {
			if !force {
				// Sync tmux state with the manifest mutations we already
				// did before bailing out — otherwise the bays we
				// closed earlier in the loop would have orphan windows.
				for _, id := range killedWindowIDs {
					e.ensurePlaceholderIfLastWindow(name, id)
					_ = e.Tmux.KillWindow(id)
				}
				return fmt.Errorf("bay %q: %w", bayID, err)
			}
		}
		killedWindowIDs = append(killedWindowIDs, result.windowIDs...)
	}

	// Remove dock from manifest.
	if err := e.withManifest(func(m *manifest.Manifest) error {
		return m.RemoveDock(name)
	}); err != nil {
		return fmt.Errorf("removing dock from manifest: %w", err)
	}

	// Remove config overrides.
	delete(e.Config.Docks, name)
	if e.configPath != "" {
		if err := config.Save(e.configPath, e.Config); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
	}

	_ = os.RemoveAll(filepath.Join(filepath.Dir(e.manifestPath), "logs", name))

	// All manifest state is persisted. Now kill the tmux session.
	// KillSession errors are non-fatal — the session may already be dead.
	_ = e.Tmux.KillSession(name)
	return nil
}

// List returns all docks and bays with runtime status.
func (e *Engine) List() ([]DockInfo, error) {
	e.SyncAll()

	m, err := e.LoadManifest()
	if err != nil {
		return nil, err
	}

	var docks []DockInfo
	for i := range m.Docks {
		dock := &m.Docks[i]
		agent := e.resolvedDockAgent(dock.Name, m)
		info := DockInfo{
			Name:        dock.Name,
			Agent:       agent,
			Path:        dock.Path,
			WorktreeDir: dock.EffectiveWorktreeDir(),
		}
		waitingWindows, _ := e.Tmux.WaitingOrBellWindowIDs(dock.Name)

		for _, s := range dock.Surfaces {
			sInfo := SurfaceInfo{
				ID:      s.ID,
				Name:    s.Name,
				Type:    string(s.Type),
				Backend: string(s.Backend),
				Status:  manifest.SyncStatusOK,
			}
			if s.Command != nil {
				sInfo.Command = *s.Command
			}
			info.Surfaces = append(info.Surfaces, sInfo)
		}
		for j := range dock.Bays {
			bay := &dock.Bays[j]
			if err := validatePersistedHomeBayShape(dock, bay); err != nil {
				return nil, err
			}
			info.Bays = append(info.Bays, e.buildBayInfo(bay, agent, waitingWindows))
		}
		docks = append(docks, info)
	}
	return docks, nil
}

// buildBayInfo constructs a BayInfo from a manifest bay,
// checking path existence and tmux liveness for each surface.
func (e *Engine) buildBayInfo(bay *manifest.Bay, agent string, waitingWindows map[string]bool) BayInfo {
	bayPath := config.ExpandPath(bay.Path)
	_, statErr := os.Stat(bayPath)

	branch := ""
	pr := ""
	if !manifest.IsHomeBay(bay) && bay.Worktree != nil {
		branch = bay.Worktree.Branch
		pr = bay.Worktree.PR
	}

	bayInfo := BayInfo{
		ID:           bay.ID,
		Name:         bay.Name,
		Description:  bay.Description,
		Type:         string(bay.Type),
		Path:         bay.Path,
		Branch:       branch,
		PR:           pr,
		Pending:      !manifest.IsHomeBay(bay) && bay.Worktree != nil && bay.Worktree.Branch != "" && !bay.IsMerged(),
		Setup:        manifest.PrepareSetupSummary(bay.Prepare),
		Missing:      !manifest.IsHomeBay(bay) && bay.Path != "" && statErr != nil,
		DefaultAgent: agent,
		SyncStatus:   manifest.SyncStatusOK,
		SurfaceCount: len(bay.Surfaces),
	}

	if bayInfo.Missing {
		bayInfo.SyncStatus = manifest.SyncStatusMissing
	}

	// Compute dirty state from git.
	if !manifest.IsHomeBay(bay) && bay.Path != "" && statErr == nil {
		if dirty, err := e.Git.IsDirty(bayPath); err == nil {
			bayInfo.Dirty = dirty
		}
	}

	for _, s := range bay.Surfaces {
		sInfo := SurfaceInfo{
			ID:      s.ID,
			Name:    s.Name,
			Type:    string(s.Type),
			Backend: string(s.Backend),
			Status:  manifest.SyncStatusOK,
		}
		if s.Agent != nil {
			sInfo.Agent = *s.Agent
		}
		if s.Command != nil {
			sInfo.Command = *s.Command
		}

		if s.Tmux != nil && s.Tmux.WindowID != "" {
			exists, _ := e.Tmux.WindowExists(s.Tmux.WindowID)
			if !exists {
				sInfo.Status = manifest.SyncStatusStale
				bayInfo.Stale = true
			} else if waitingWindows[s.Tmux.WindowID] {
				bayInfo.Waiting = true
			}
		}

		bayInfo.Surfaces = append(bayInfo.Surfaces, sInfo)
	}

	if bayInfo.Missing {
		bayInfo.Stale = false
	}
	if bayInfo.SyncStatus == manifest.SyncStatusOK && bayInfo.Stale {
		bayInfo.SyncStatus = manifest.SyncStatusStale
	}
	return bayInfo
}

// BayInfoByName returns the runtime view for a single bay.
// This does NOT call List() or SyncAll — it loads the manifest and
// builds info for just the requested bay. Callers that display
// data should call SyncAll first.
func (e *Engine) BayInfoByName(dockName, bayID string) (*BayInfo, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return nil, err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return nil, fmt.Errorf("unknown dock %q", dockName)
	}
	bay := dock.FindBayByID(bayID)
	if bay == nil {
		return nil, fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
	}
	if err := validatePersistedHomeBayShape(dock, bay); err != nil {
		return nil, err
	}
	agent := e.resolvedDockAgent(dockName, m)
	waitingWindows, _ := e.Tmux.WaitingOrBellWindowIDs(dockName)
	info := e.buildBayInfo(bay, agent, waitingWindows)
	return &info, nil
}
