package engine

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"

	"github.com/commontoolsinc/bay/internal/config"
	"github.com/commontoolsinc/bay/internal/manifest"
)

// RecoverResult holds the results of a recovery operation.
type RecoverResult struct {
	Dock      string
	Recovered []string // bay names that had surfaces recreated
	Warnings  []string
	AttachCmd string
}

type recoverOutcome struct {
	recovered []string
	errs      []string
	warnings  []string
	changed   bool
}

// Recover reconstructs all tmux state from the manifest after reboot.
func (e *Engine) Recover() ([]RecoverResult, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return nil, err
	}

	results := make([]RecoverResult, 0, len(m.Docks))
	var errs []string
	for i := range m.Docks {
		dock := &m.Docks[i]

		sessionIDChanged, err := e.applyRecoveredSessionID(dock)
		if err != nil {
			// One unhealthy dock must not abort the others.
			errs = append(errs, fmt.Sprintf("dock %s: %v", dock.Name, err))
			continue
		}
		if sessionIDChanged {
			// Server restart or session takeover — recorded pane/window
			// IDs may collide with foreign panes/windows in the new
			// server. Force the recover loop to recreate from scratch
			// rather than reconcile against potentially-stale IDs.
			invalidateRecordedTmuxAttrs(dock)
		}

		outcome := e.recoverDockBays(dock, m)
		if sessionIDChanged {
			outcome.changed = true
		}
		e.cleanPlaceholders(dock.Name)
		e.recoverDockHostTerminal(dock, &outcome)
		e.refreshDockWindowNames(dock)

		if outcome.changed {
			if err := e.mergeRecoveredDockState(dock); err != nil {
				outcome.errs = append(outcome.errs, fmt.Sprintf("dock %s: save state: %v", dock.Name, err))
			}
		}

		results = append(results, RecoverResult{
			Dock:      dock.Name,
			Recovered: outcome.recovered,
			Warnings:  outcome.warnings,
			AttachCmd: fmt.Sprintf("tmux attach -t %s", dock.Name),
		})
		errs = append(errs, outcome.errs...)
	}

	if len(errs) > 0 {
		return results, fmt.Errorf("recovery completed with errors:\n%s", strings.Join(errs, "\n"))
	}
	return results, nil
}

// DockRecover recovers a single dock by name.
func (e *Engine) DockRecover(name string) (RecoverResult, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return RecoverResult{}, err
	}
	dock := m.FindDock(name)
	if dock == nil {
		return RecoverResult{}, fmt.Errorf("unknown dock %q in manifest", name)
	}

	sessionIDChanged, err := e.applyRecoveredSessionID(dock)
	if err != nil {
		return RecoverResult{}, err
	}
	if sessionIDChanged {
		// See Recover() for why we drop recorded IDs on a session
		// takeover.
		invalidateRecordedTmuxAttrs(dock)
	}

	outcome := e.recoverDockBays(dock, m)
	if sessionIDChanged {
		outcome.changed = true
	}
	e.recoverDockSurfaces(dock, &outcome)
	e.cleanPlaceholders(name)
	e.recoverDockHostTerminal(dock, &outcome)
	e.refreshDockWindowNames(dock)

	if outcome.changed {
		if err := e.mergeRecoveredDockState(dock); err != nil {
			return RecoverResult{}, err
		}
	}

	// Restore focus to the most recently active bay.
	e.restoreFocusedWindow(dock)

	result := RecoverResult{
		Dock:      name,
		Recovered: outcome.recovered,
		Warnings:  outcome.warnings,
		AttachCmd: fmt.Sprintf("tmux attach -t %s", name),
	}
	if len(outcome.errs) > 0 {
		return result, fmt.Errorf("recovery completed with errors:\n%s", strings.Join(outcome.errs, "\n"))
	}
	return result, nil
}

// recoverDockSurfaces recreates dock-level surfaces (e.g., dock editor).
func (e *Engine) recoverDockSurfaces(dock *manifest.Dock, outcome *recoverOutcome) {
	for i := range dock.Surfaces {
		s := &dock.Surfaces[i]
		if s.Tmux == nil {
			continue
		}
		// Check if the window still exists.
		if s.Tmux.WindowID != "" {
			exists, _ := e.Tmux.WindowExists(s.Tmux.WindowID)
			if exists {
				continue // already alive
			}
		}

		// Determine CWD — use the dock's worktree dir if available.
		cwd := "/tmp"
		if parentDir, err := e.EditAllParentDir(dock.Name); err == nil {
			cwd = parentDir
		}

		// Recreate the window.
		winID, err := e.Tmux.NewWindow(dock.Name, s.Name, cwd)
		if err != nil {
			outcome.errs = append(outcome.errs, fmt.Sprintf("dock surface %s: %v", s.Name, err))
			continue
		}
		_ = e.Tmux.SetWindowOption(winID, "@bay-dock-editor", "1")
		_ = e.Tmux.MoveWindow(winID, 0)

		panes, _ := e.Tmux.ListPanes(winID)
		if len(panes) > 0 {
			s.Tmux.PaneID = panes[0].ID
			if s.Command != nil && *s.Command != "" {
				_ = e.Tmux.RespawnPane(panes[0].ID, cwd, *s.Command)
			}
		}
		s.Tmux.WindowID = winID
		outcome.recovered = append(outcome.recovered, fmt.Sprintf("dock surface: %s", s.Name))
		outcome.changed = true
	}
}

// restoreFocusedWindow selects the tmux window for the most recently
// active bay, so recovery leaves the user where they were.
func (e *Engine) restoreFocusedWindow(dock *manifest.Dock) {
	var bestBay *manifest.Bay
	for i := range dock.Bays {
		bay := &dock.Bays[i]
		if bestBay == nil || bay.LastActive > bestBay.LastActive {
			bestBay = bay
		}
	}
	if bestBay == nil {
		return
	}
	// Find the window for the last-focused surface, or the first surface.
	for _, s := range bestBay.Surfaces {
		if s.Tmux != nil && s.Tmux.WindowID != "" {
			if bestBay.LastFocused > 0 && s.ID == bestBay.LastFocused {
				_ = e.Tmux.SelectWindow(s.Tmux.WindowID)
				if s.Tmux.PaneID != "" {
					_ = e.Tmux.SelectPane(s.Tmux.PaneID)
				}
				return
			}
		}
	}
	// Fallback: select the first surface's window.
	for _, s := range bestBay.Surfaces {
		if s.Tmux != nil && s.Tmux.WindowID != "" {
			_ = e.Tmux.SelectWindow(s.Tmux.WindowID)
			return
		}
	}
}

// recoverDockBays handles recovery for all bays in a dock.
func (e *Engine) recoverDockBays(dock *manifest.Dock, m *manifest.Manifest) recoverOutcome {
	outcome := recoverOutcome{}

	for i := range dock.Bays {
		bay := &dock.Bays[i]

		if manifest.IsHomeBay(bay) {
			if err := validateHomeBayLifecycleShape(dock, bay); err != nil {
				outcome.errs = append(outcome.errs, err.Error())
				continue
			}
			if len(bay.Surfaces) == 0 {
				continue
			}
		}
		cwd, ok := recoverBayCWD(dock, bay)
		if !ok {
			continue
		}
		if _, err := os.Stat(cwd); err != nil {
			continue
		}

		bayRecovered := false
		label := BayCompactLabel(bay)

		// Group surfaces by layout group for recovery.
		groups := groupSurfacesByLayout(bay)

		for _, layoutGroup := range sortedLayoutGroups(groups) {
			surfaceIndices := orderedSurfaceIndices(bay, groups[layoutGroup])
			// Check if the tmux window for this group still exists.
			var existingWindowID string
			for _, idx := range surfaceIndices {
				s := &bay.Surfaces[idx]
				if s.Tmux != nil && s.Tmux.WindowID != "" {
					exists, err := e.Tmux.WindowExists(s.Tmux.WindowID)
					if err != nil {
						outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s: check window %s: %v", label, s.Tmux.WindowID, err))
						continue
					}
					if exists {
						existingWindowID = s.Tmux.WindowID
						break
					}
				}
			}

			if existingWindowID != "" {
				// Window exists — reconcile panes.
				outcome.changed = e.reconcileSurfaces(dock.Name, existingWindowID, cwd, bay, surfaceIndices, m, &outcome) || outcome.changed
			} else {
				// Window gone — recreate it. Primary windows (group 1) get
				// the bay compact label; secondary windows get :surfacename.
				bayRecovered = true
				windowName := bayWindowLabel(bay)
				if layoutGroup > 1 && len(surfaceIndices) > 0 {
					windowName = ":" + bay.Surfaces[surfaceIndices[0]].Name
				}
				newWindowID, err := e.Tmux.NewWindow(dock.Name, windowName, cwd)
				if err != nil {
					outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s: create window: %v", label, err))
					continue
				}
				outcome.changed = true

				for j, idx := range surfaceIndices {
					s := &bay.Surfaces[idx]
					if s.Tmux == nil {
						continue
					}
					s.Tmux.WindowID = newWindowID
					s.Tmux.LayoutGroup = layoutGroup

					if j == 0 {
						// First surface uses the window's initial pane.
						panes, err := e.Tmux.ListPanes(newWindowID)
						if err != nil {
							outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s: list panes for %s: %v", label, newWindowID, err))
							continue
						}
						if len(panes) == 0 {
							outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s: window %s has no initial pane", label, newWindowID))
							continue
						}
						s.Tmux.PaneID = panes[0].ID
						if err := e.recoverSurfaceLaunch(dock.Name, s, s.Tmux.PaneID, cwd, m, true); err != nil {
							outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s surface %s: %v", label, s.Name, err))
						}
					} else {
						// Subsequent surfaces split from their recorded parent pane.
						dir := s.Tmux.SplitDir
						if dir == "" {
							dir = "v"
						}
						splitTargetID, err := recoverSplitTargetID(bay, s, newWindowID)
						if err != nil {
							outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s surface %s: %v", label, s.Name, err))
							continue
						}
						newPaneID, err := e.Tmux.SplitWindow(splitTargetID, dir, cwd, false)
						if err != nil {
							outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s surface %s: split window: %v", label, s.Name, err))
							continue
						}
						s.Tmux.PaneID = newPaneID
						if err := e.recoverSurfaceLaunch(dock.Name, s, newPaneID, cwd, m, true); err != nil {
							outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s surface %s: %v", label, s.Name, err))
						}
					}
				}
			}
		}

		if bayRecovered {
			outcome.recovered = append(outcome.recovered, label)
		}
	}
	return outcome
}

// reconcileSurfaces checks existing panes against manifest surfaces and repairs missing ones.
func (e *Engine) reconcileSurfaces(dockName, tmuxWindowID, cwd string, bay *manifest.Bay, surfaceIndices []int, m *manifest.Manifest, outcome *recoverOutcome) bool {
	label := BayCompactLabel(bay)
	tmuxPanes, err := e.Tmux.ListPanes(tmuxWindowID)
	if err != nil {
		outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s: list panes for %s: %v", label, tmuxWindowID, err))
		return false
	}
	changed := false

	// Update pane IDs for existing surfaces.
	for i, idx := range surfaceIndices {
		s := &bay.Surfaces[idx]
		if s.Tmux != nil && i < len(tmuxPanes) && s.Tmux.PaneID != tmuxPanes[i].ID {
			s.Tmux.PaneID = tmuxPanes[i].ID
			changed = true
		}
	}

	// Recreate missing panes.
	for j := len(tmuxPanes); j < len(surfaceIndices); j++ {
		s := &bay.Surfaces[surfaceIndices[j]]
		if s.Tmux == nil {
			continue
		}
		dir := s.Tmux.SplitDir
		if dir == "" {
			dir = "v"
		}
		splitTargetID, err := recoverSplitTargetID(bay, s, tmuxWindowID)
		if err != nil {
			outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s surface %s: %v", label, s.Name, err))
			continue
		}
		newPaneID, err := e.Tmux.SplitWindow(splitTargetID, dir, cwd, false)
		if err != nil {
			outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s surface %s: split window: %v", label, s.Name, err))
			continue
		}
		s.Tmux.PaneID = newPaneID
		changed = true
		if err := e.recoverSurfaceLaunch(dockName, s, newPaneID, cwd, m, true); err != nil {
			outcome.errs = append(outcome.errs, fmt.Sprintf("bay %s surface %s: %v", label, s.Name, err))
		}
	}
	return changed
}

func recoverBayCWD(dock *manifest.Dock, bay *manifest.Bay) (string, bool) {
	if manifest.IsHomeBay(bay) {
		if dock == nil || dock.Path == "" {
			return "", false
		}
		return config.ExpandPath(dock.Path), true
	}
	if bay == nil || bay.Path == "" {
		return "", false
	}
	return config.ExpandPath(bay.Path), true
}

// recoverSurfaceLaunch sends the appropriate launch command to a recovered surface.
func (e *Engine) recoverSurfaceLaunch(dockName string, s *manifest.Surface, tmuxPaneID, cwd string, m *manifest.Manifest, newlyCreated bool) error {
	if tmuxPaneID == "" {
		return fmt.Errorf("missing tmux pane id")
	}

	if !newlyCreated {
		pid, err := e.Tmux.GetPanePID(tmuxPaneID)
		if err == nil && pid > 0 {
			if proc, findErr := os.FindProcess(pid); findErr == nil {
				if proc.Signal(syscall.Signal(0)) == nil {
					return nil
				}
			}
		}
	}

	switch s.Type {
	case manifest.SurfaceTypeAgent:
		if s.Agent == nil || *s.Agent == "" {
			return fmt.Errorf("missing agent configuration")
		}
		agentName := *s.Agent
		agentArgs, launchArgs := e.resolvedAgentArgs(dockName, agentName, m)
		agentCmd, err := e.buildAgentCommand(agentName, agentArgs, launchArgs, true)
		if err != nil {
			return err
		}
		if err := e.Tmux.SendKeys(tmuxPaneID, agentCmd); err != nil {
			return err
		}
	case manifest.SurfaceTypeCmd:
		if s.Command != nil && *s.Command != "" {
			if err := e.Tmux.SendKeys(tmuxPaneID, *s.Command); err != nil {
				return err
			}
		}
	case manifest.SurfaceTypeShell:
		// No command needed.
	case manifest.SurfaceTypeEditor:
		if s.Command != nil && *s.Command != "" {
			if err := e.Tmux.RespawnPane(tmuxPaneID, cwd, *s.Command); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) recoverDockHostTerminal(dock *manifest.Dock, outcome *recoverOutcome) {
	terminal := e.Config.ResolvedDockTerminal(dock.Name)
	if dock.Host == nil || terminal == "" {
		return
	}
	if dock.Host.PID != 0 && processAlive(dock.Host.PID) {
		return
	}

	pid, err := launchTerminal(terminal, dock.Name)
	if err != nil {
		outcome.warnings = append(outcome.warnings, fmt.Sprintf("dock %s: launch terminal: %v", dock.Name, err))
		return
	}
	if dock.Host.PID != pid {
		dock.Host.PID = pid
		outcome.changed = true
	}
}

// applyRecoveredSessionID resolves the live tmux session's identity
// against the dock's stored SessionID and writes the result onto the
// in-memory recovered dock. Returns true when the stored ID moved, so
// the caller can flag the recover outcome dirty even if no other
// state changed. The manifest write happens later in
// mergeRecoveredDockState — see docs/design/session-identity.md for
// why ensureSession's tmux side and the manifest write must straddle
// the per-dock recover work.
func (e *Engine) applyRecoveredSessionID(dock *manifest.Dock) (bool, error) {
	sessionID, err := e.ensureSession(dock.Name, dock.SessionID)
	if err != nil {
		return false, err
	}
	changed := dock.SessionID != sessionID
	dock.SessionID = sessionID
	return changed, nil
}

func (e *Engine) mergeRecoveredDockState(recovered *manifest.Dock) error {
	return e.withManifestMaybe(func(m *manifest.Manifest) (bool, error) {
		dock := m.FindDock(recovered.Name)
		if dock == nil {
			return false, fmt.Errorf("dock %q disappeared during recovery", recovered.Name)
		}
		return mergeRecoveredDockRuntimeState(dock, recovered), nil
	})
}

func mergeRecoveredDockRuntimeState(dst, src *manifest.Dock) bool {
	changed := false

	if src.SessionID != "" && dst.SessionID != src.SessionID {
		dst.SessionID = src.SessionID
		changed = true
	}

	if src.Host != nil {
		if dst.Host == nil {
			dst.Host = &manifest.GUIAttrs{}
			changed = true
		}
		if dst.Host.PID != src.Host.PID {
			dst.Host.PID = src.Host.PID
			changed = true
		}
	}

	for i := range src.Bays {
		srcWS := &src.Bays[i]
		dstWS := findBayForRecoveryMerge(dst, srcWS)
		if dstWS == nil {
			continue
		}
		for j := range srcWS.Surfaces {
			srcSurface := &srcWS.Surfaces[j]
			if srcSurface.Tmux == nil {
				continue
			}
			dstSurface := findSurfaceForRecoveryMerge(dstWS, srcSurface)
			if dstSurface == nil {
				continue
			}
			if dstSurface.Tmux == nil {
				dstSurface.Tmux = &manifest.TmuxAttrs{
					LayoutGroup: srcSurface.Tmux.LayoutGroup,
					SplitFrom:   srcSurface.Tmux.SplitFrom,
					SplitDir:    srcSurface.Tmux.SplitDir,
				}
				changed = true
			}
			if dstSurface.Tmux.WindowID != srcSurface.Tmux.WindowID {
				dstSurface.Tmux.WindowID = srcSurface.Tmux.WindowID
				changed = true
			}
			if dstSurface.Tmux.PaneID != srcSurface.Tmux.PaneID {
				dstSurface.Tmux.PaneID = srcSurface.Tmux.PaneID
				changed = true
			}
		}
	}

	return changed
}

func findBayForRecoveryMerge(dock *manifest.Dock, src *manifest.Bay) *manifest.Bay {
	if manifest.IsHomeBay(src) {
		return dock.FindBayByID(manifest.HomeBayID)
	}
	for i := range dock.Bays {
		if dock.Bays[i].Path == src.Path {
			return &dock.Bays[i]
		}
	}
	if bay := dock.FindBayByID(src.ID); bay != nil {
		return bay
	}
	if src.Name == "" {
		return nil
	}
	return dock.FindBay(src.Name)
}

func findSurfaceForRecoveryMerge(bay *manifest.Bay, src *manifest.Surface) *manifest.Surface {
	if src.ID != 0 {
		if surface := bay.FindSurfaceByID(src.ID); surface != nil {
			return surface
		}
	}
	if src.Name == "" {
		return nil
	}
	return bay.FindSurface(src.Name)
}

func recoverSplitTargetID(bay *manifest.Bay, s *manifest.Surface, fallbackTargetID string) (string, error) {
	if s.Tmux == nil || s.Tmux.SplitFrom == 0 {
		return fallbackTargetID, nil
	}
	parent := bay.FindSurfaceByID(s.Tmux.SplitFrom)
	if parent == nil || parent.Tmux == nil || parent.Tmux.PaneID == "" {
		return "", fmt.Errorf("missing split parent %d", s.Tmux.SplitFrom)
	}
	return parent.Tmux.PaneID, nil
}

// groupSurfacesByLayout groups surface indices by their layout group.
// Returns a map of layout group -> slice of surface indices, ordered by group number.
func groupSurfacesByLayout(bay *manifest.Bay) map[int][]int {
	groups := map[int][]int{}
	for i, s := range bay.Surfaces {
		if s.Tmux == nil {
			continue
		}
		groups[s.Tmux.LayoutGroup] = append(groups[s.Tmux.LayoutGroup], i)
	}
	return groups
}

func sortedLayoutGroups(groups map[int][]int) []int {
	keys := make([]int, 0, len(groups))
	for group := range groups {
		keys = append(keys, group)
	}
	slices.Sort(keys)
	return keys
}

func orderedSurfaceIndices(bay *manifest.Bay, indices []int) []int {
	if len(indices) < 2 {
		return append([]int(nil), indices...)
	}

	idxByID := make(map[int]int, len(indices))
	for _, idx := range indices {
		idxByID[bay.Surfaces[idx].ID] = idx
	}

	children := map[int][]int{}
	for _, idx := range indices {
		parentID := 0
		if tmuxAttrs := bay.Surfaces[idx].Tmux; tmuxAttrs != nil {
			parentID = tmuxAttrs.SplitFrom
			if parentID != 0 {
				if _, ok := idxByID[parentID]; !ok {
					parentID = 0
				}
			}
		}
		children[parentID] = append(children[parentID], idx)
	}
	for parentID := range children {
		slices.SortFunc(children[parentID], func(a, b int) int {
			return bay.Surfaces[a].ID - bay.Surfaces[b].ID
		})
	}

	var ordered []int
	visited := make(map[int]bool, len(indices))
	var visit func(parentID int)
	visit = func(parentID int) {
		for _, idx := range children[parentID] {
			if visited[idx] {
				continue
			}
			visited[idx] = true
			ordered = append(ordered, idx)
			visit(bay.Surfaces[idx].ID)
		}
	}
	visit(0)

	if len(ordered) == len(indices) {
		return ordered
	}

	fallback := append([]int(nil), indices...)
	slices.SortFunc(fallback, func(a, b int) int {
		return bay.Surfaces[a].ID - bay.Surfaces[b].ID
	})
	for _, idx := range fallback {
		if !visited[idx] {
			ordered = append(ordered, idx)
		}
	}
	return ordered
}
