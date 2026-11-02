package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/commonfabric/bay/internal/manifest"
)

// uniqueSurfaceName returns a name that doesn't collide with existing surfaces.
// If "shell" is taken, tries "shell-2", "shell-3", etc.
func uniqueSurfaceName(bay *manifest.Bay, name string) string {
	if bay.FindSurface(name) == nil {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if bay.FindSurface(candidate) == nil {
			return candidate
		}
	}
}

func (e *Engine) preferredSplitTarget(bay *manifest.Bay) *manifest.Surface {
	if paneID, err := e.Tmux.CurrentPaneID(); err == nil {
		for i := range bay.Surfaces {
			s := &bay.Surfaces[i]
			if s.Tmux != nil && s.Tmux.PaneID == paneID {
				return s
			}
		}
	}
	if bay.LastFocused != 0 {
		if s := bay.FindSurfaceByID(bay.LastFocused); s != nil && s.Tmux != nil && s.Tmux.WindowID != "" {
			return s
		}
	}
	for i := len(bay.Surfaces) - 1; i >= 0; i-- {
		s := &bay.Surfaces[i]
		if s.Tmux != nil && s.Tmux.WindowID != "" {
			return s
		}
	}
	return nil
}

func closedEntryForSurface(bayID string, s *manifest.Surface, closedAt int64) (manifest.ClosedEntry, bool) {
	if s.Backend != manifest.SurfaceBackendTmux {
		return manifest.ClosedEntry{}, false
	}
	entry := manifest.ClosedEntry{
		ClosedAt: closedAt,
		Kind:     manifest.ClosedKindSurface,
		Surface: &manifest.ClosedSurface{
			Bay:  bayID,
			Name: s.Name,
			Type: s.Type,
		},
	}
	if s.Agent != nil {
		entry.Surface.Agent = *s.Agent
	}
	if s.Command != nil {
		entry.Surface.Command = *s.Command
	}
	if s.Tmux != nil {
		// Preserve split direction and layout group so restore can rejoin
		// the original tmux window if any siblings survive. SplitDir=""
		// identifies a root pane; restoring into an existing layout group
		// uses split-window -fb to insert at the root position.
		entry.Surface.SplitDir = s.Tmux.SplitDir
		entry.Surface.LayoutGroup = s.Tmux.LayoutGroup
	}
	return entry, true
}

// lastSurfaceInLayoutGroup returns the most-recently-added surviving
// surface in the given layout group, or nil if none. Restore uses this
// as the split target so a re-created pane appends to the end of the
// current tmux pane stack — which matches user expectation for "delete
// the last pane, restore it" (the common case). Splitting against the
// first surface instead would place the new pane next to the root and
// visually insert it into the middle of the stack.
func lastSurfaceInLayoutGroup(bay *manifest.Bay, layoutGroup int) *manifest.Surface {
	if layoutGroup == 0 {
		return nil
	}
	for i := len(bay.Surfaces) - 1; i >= 0; i-- {
		s := &bay.Surfaces[i]
		if s.Tmux != nil && s.Tmux.LayoutGroup == layoutGroup && s.Tmux.PaneID != "" {
			return s
		}
	}
	return nil
}

// inferRestoreAxis picks the split axis ("h" or "v") for a root-pane
// restore into an existing layout group. A root pane's own SplitDir is
// empty by definition, so we consult siblings: if any sibling was
// created with SplitDir="h", the group is horizontally split and the
// restored root pane should be inserted with -h. Defaults to "v" if no
// sibling has a recorded split (single-pane window).
func inferRestoreAxis(bay *manifest.Bay, layoutGroup int) string {
	for i := range bay.Surfaces {
		s := &bay.Surfaces[i]
		if s.Tmux == nil || s.Tmux.LayoutGroup != layoutGroup {
			continue
		}
		if s.Tmux.SplitDir == "h" || s.Tmux.SplitDir == "v" {
			return s.Tmux.SplitDir
		}
	}
	return "v"
}

// SurfaceAddOptions holds the parameters for adding a surface.
type SurfaceAddOptions struct {
	DockName string
	BayName  string
	Type     manifest.SurfaceType
	Name     string
	Agent    string
	Command  string
	SplitDir string

	// RestoreLayoutGroup, when non-zero, asks SurfaceAdd to place the new
	// surface in the existing tmux window of that layout group if any
	// sibling survives. Used by undo-close so a restored pane rejoins its
	// original window (splitting against a surviving sibling) rather than
	// creating a new tmux window. When SplitDir is "" and a sibling
	// exists, SurfaceAdd uses `tmux split-window -fb` so the pane lands
	// at the root position (top/left) of the layout group.
	RestoreLayoutGroup int

	// Resume, when true, launches an agent surface with the agent's
	// configured resume_args (e.g. `claude --continue`) so the prior
	// session is picked up. Used by undo-close so restoring an agent
	// surface continues its previous conversation rather than starting
	// fresh. Ignored for non-agent surfaces.
	Resume bool
}

// SurfaceAdd adds a new surface to a bay.
func (e *Engine) SurfaceAdd(opts SurfaceAddOptions) error {
	dockName := opts.DockName
	bayID := opts.BayName
	surfaceType := opts.Type
	name := opts.Name
	agent := opts.Agent
	cmd := opts.Command
	splitDir := opts.SplitDir
	m, err := e.LoadManifest()
	if err != nil {
		return err
	}

	dock := m.FindDock(dockName)
	if dock == nil {
		return fmt.Errorf("unknown dock %q", dockName)
	}
	bay := dock.FindBayByID(bayID)
	if bay == nil {
		if manifest.IsReservedBayID(bayID) {
			if err := validateResolvableHome(dock); err != nil {
				return err
			}
			home, err := newHomeBay(dock)
			if err != nil {
				return err
			}
			bay = &home
		} else {
			return fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
		}
	}
	if manifest.IsReservedBayID(bayID) {
		if err := validateResolvableHome(dock); err != nil {
			return err
		}
		if err := e.ensureDockSessionForHomeSurface(dockName, dock); err != nil {
			return err
		}
	}
	cwd := homeSurfaceCWD(dock, bay)

	// Determine the layout group — find an existing tmux window to split into,
	// or create a new one.
	var tmuxWindowID string
	var tmuxSplitTargetID string
	var layoutGroup int
	var splitFromSurface int
	splitBefore := false
	splitAxis := splitDir

	if opts.RestoreLayoutGroup > 0 && len(bay.Surfaces) > 0 {
		if sibling := lastSurfaceInLayoutGroup(bay, opts.RestoreLayoutGroup); sibling != nil && sibling.Tmux != nil {
			tmuxWindowID = sibling.Tmux.WindowID
			tmuxSplitTargetID = sibling.Tmux.PaneID
			layoutGroup = sibling.Tmux.LayoutGroup
			if splitDir == "" {
				// Root-pane restore: insert at the root position of the
				// layout group via -fb. Infer axis from any sibling's
				// SplitDir; default "v" if the layout group has no
				// recorded splits yet. SplitFrom stays 0 — the restored
				// surface is the new root.
				splitBefore = true
				splitAxis = inferRestoreAxis(bay, opts.RestoreLayoutGroup)
			} else {
				// Split child: parent is whichever sibling we land against.
				splitFromSurface = sibling.ID
			}
		}
	} else if splitDir != "" && len(bay.Surfaces) > 0 {
		if parent := e.preferredSplitTarget(bay); parent != nil {
			tmuxWindowID = parent.Tmux.WindowID
			tmuxSplitTargetID = parent.Tmux.PaneID
			layoutGroup = parent.Tmux.LayoutGroup
			splitFromSurface = parent.ID
		}
	}

	var tmuxPaneID string
	rollbackSurface := func() {}

	if tmuxWindowID != "" && (splitDir != "" || splitBefore) {
		// Split an existing window.
		targetID := tmuxSplitTargetID
		if targetID == "" {
			targetID = tmuxWindowID
		}
		newPaneID, splitErr := e.Tmux.SplitWindow(targetID, splitAxis, cwd, splitBefore)
		if splitErr != nil {
			return fmt.Errorf("splitting window: %w", splitErr)
		}
		tmuxPaneID = newPaneID
		rollbackSurface = func() {
			_ = e.Tmux.KillPane(newPaneID)
		}
	} else {
		// Create a new tmux window. Secondary windows get a ":surfacename"
		// tab name; the first window keeps the bay compact label.
		windowName := bayWindowLabel(bay)
		if len(bay.Surfaces) > 0 {
			windowName = ":" + name
		}
		winID, err := e.Tmux.NewWindow(dockName, windowName, cwd)
		if err != nil {
			return fmt.Errorf("creating tmux window: %w", err)
		}
		e.cleanPlaceholders(dockName)
		if bay.Type == manifest.BayTypeHome && len(bay.Surfaces) == 0 {
			_ = e.Tmux.MoveWindow(winID, 0)
		} else {
			e.positionNewWindow(dockName, winID, bayID, nil)
		}

		tmuxWindowID = winID
		layoutGroup = nextLayoutGroup(bay)

		panes, _ := e.Tmux.ListPanes(winID)
		if len(panes) > 0 {
			tmuxPaneID = panes[0].ID
		}
		rollbackSurface = func() {
			_ = e.Tmux.KillWindow(winID)
		}
	}

	if surfaceType == manifest.SurfaceTypeAgent {
		if err := e.validateAgentName(agent); err != nil {
			rollbackSurface()
			return err
		}
	}

	// Resolve agent args.
	m2, _ := e.LoadManifest()
	agentArgs, launchArgs := e.resolvedAgentArgs(dockName, agent, m2)

	// Launch the surface process.
	surface, err := e.launchSurfaceInTmux(tmuxPaneID, dockName, surfaceType, agent, cmd, cwd, agentArgs, launchArgs, opts.Resume)
	if err != nil {
		rollbackSurface()
		return err
	}
	surface.Name = uniqueSurfaceName(bay, name)
	surface.Tmux.PaneID = tmuxPaneID
	surface.Tmux.WindowID = tmuxWindowID
	surface.Tmux.LayoutGroup = layoutGroup
	surface.Tmux.SplitFrom = splitFromSurface
	surface.Tmux.SplitDir = splitDir

	err = e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			rollbackSurface()
			return fmt.Errorf("unknown dock %q", dockName)
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			if manifest.IsReservedBayID(bayID) {
				var err error
				bay, err = ensureHomeBay(dock)
				if err != nil {
					rollbackSurface()
					return err
				}
			} else {
				rollbackSurface()
				return fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
			}
		} else if manifest.IsReservedBayID(bayID) {
			if err := validateResolvableHome(dock); err != nil {
				rollbackSurface()
				return err
			}
		}

		surface.Name = uniqueSurfaceName(bay, name)
		if _, err := bay.AddSurface(surface); err != nil {
			rollbackSurface()
			return err
		}
		bay.LastActive = time.Now().Unix()
		// Adding a surface cancels any scheduled auto-close — the user
		// clearly wants this bay to live.
		bay.PendingCloseAt = 0
		return nil
	})
	if err != nil {
		return err
	}

	// If this is a secondary window and the name was uniquified (e.g.,
	// "shell" → "shell-2"), update the tmux window name to match.
	if layoutGroup > 1 && surface.Name != name {
		_ = e.Tmux.RenameWindow(tmuxWindowID, ":"+surface.Name)
	}
	return nil
}

// DockSurfaceClose removes a dock-level surface and kills its tmux pane/window.
func (e *Engine) DockSurfaceClose(dockName, surfaceName string) error {
	var windowIDToKill, paneIDToKill string

	err := e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		s := dock.FindDockSurface(surfaceName)
		if s == nil {
			return fmt.Errorf("dock surface %q not found in dock %q", surfaceName, dockName)
		}
		if s.Tmux != nil {
			windowIDToKill = s.Tmux.WindowID
			paneIDToKill = s.Tmux.PaneID
		}
		return dock.RemoveDockSurface(surfaceName)
	})
	if err != nil {
		return err
	}

	if windowIDToKill != "" {
		_ = e.Tmux.KillWindow(windowIDToKill)
	} else if paneIDToKill != "" {
		_ = e.Tmux.KillPane(paneIDToKill)
	}
	return nil
}

// SurfaceClose removes a surface from a bay.
//
// The manifest update happens BEFORE the destructive tmux kill so that
// when bay is invoked from inside the pane being closed, the user-visible
// state is already correct by the time tmux SIGHUPs bay. See also
// SurfaceRestart and the closeBayState helper for the same pattern.
//
// Records an undo-close entry on the dock's queue so `bay sf restore`
// (Option+Z) can recreate the surface. Skipped for non-tmux-backed surfaces.
func (e *Engine) SurfaceClose(dockName, bayID, surfaceName string, force bool) error {
	var windowIDToKill, paneIDToKill string
	bayEmpty := false
	homeSurface := false
	dismissDock := false

	err := e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
		}
		homeSurface = manifest.IsHomeBay(bay)
		if homeSurface {
			if err := validateHomeBayLifecycleShape(dock, bay); err != nil {
				return err
			}
		}
		s := bay.FindSurface(surfaceName)
		if s == nil {
			return fmt.Errorf("surface %q not found in bay %q", surfaceName, bayID)
		}

		// Capture what we'll kill before the surface is removed from
		// the in-memory manifest (RemoveSurface invalidates s).
		if s.Tmux != nil && s.Tmux.WindowID != "" {
			if countSurfacesInLayoutGroup(bay, s.Tmux.LayoutGroup) <= 1 {
				windowIDToKill = s.Tmux.WindowID
			} else if s.Tmux.PaneID != "" {
				if exists, _ := e.Tmux.PaneExists(s.Tmux.PaneID); exists {
					panes, err := e.Tmux.ListPanes(s.Tmux.WindowID)
					if err != nil {
						return fmt.Errorf("checking panes: %w", err)
					}
					if len(panes) <= 1 {
						windowIDToKill = s.Tmux.WindowID
					} else {
						paneIDToKill = s.Tmux.PaneID
					}
				}
			}
		}
		if homeSurface {
			wouldDismiss, err := e.homeSurfaceCloseWouldDismissDock(dockName, bay, s)
			if err != nil {
				return err
			}
			if wouldDismiss && !force {
				return lastHomeCloseError(dockName)
			}
			dismissDock = wouldDismiss
		}

		// Queue an undo entry (tmux-backed surfaces only).
		if entry, ok := closedEntryForSurface(bayID, s, time.Now().Unix()); ok {
			dock.PushClosedEntry(entry)
		}

		if err := bay.RemoveSurface(s.Name); err != nil {
			return err
		}
		bay.LastActive = time.Now().Unix()
		bayEmpty = len(bay.Surfaces) == 0
		if dismissDock {
			if err := dock.RemoveBay(bayID); err != nil {
				return err
			}
			dock.SessionID = ""
			bayEmpty = true
		} else if homeSurface && bayEmpty {
			removeHomeBayIfEmpty(dock)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Manifest is saved. Now do the destructive tmux work — bay may die
	// mid-call if it's running in the pane being killed, but the
	// user-visible state is already correct.
	if dismissDock {
		_ = e.Tmux.KillSession(dockName)
		return nil
	}
	if windowIDToKill != "" {
		if !homeSurface {
			e.ensureHomeIfLastWindow(dockName, windowIDToKill)
		}
		_ = e.Tmux.KillWindow(windowIDToKill)
	} else if paneIDToKill != "" {
		_ = e.Tmux.KillPane(paneIDToKill)
	}

	// Bay became empty. With force, close immediately; without,
	// schedule the grace-windowed auto-close (see orphan-hygiene.md).
	if bayEmpty {
		if homeSurface {
			return nil
		}
		if force {
			if err := e.BayClose(dockName, bayID, force); err != nil {
				return fmt.Errorf("surface closed, but bay %q not removed: %w", bayID, err)
			}
			return nil
		}
		_ = e.withManifest(func(m *manifest.Manifest) error {
			dock := m.FindDock(dockName)
			if dock == nil {
				return nil
			}
			bay := dock.FindBayByID(bayID)
			if bay == nil {
				return nil
			}
			if bay.PendingCloseAt == 0 {
				bay.PendingCloseAt = time.Now().Unix() + orphanGraceSeconds
			}
			return nil
		})
	}
	return nil
}

// ErrNothingToRestore indicates the undo-close queue is empty for the dock.
// Callers (CLI/keybinding) treat this as informational, not a failure.
var ErrNothingToRestore = errors.New("nothing to restore")

// ListClosedEntries returns the dock's undo-close queue in most-recent-first
// order after pruning expired entries. The prune is persisted.
func (e *Engine) ListClosedEntries(dockName string) ([]manifest.ClosedEntry, error) {
	var out []manifest.ClosedEntry
	err := e.withManifestMaybe(func(m *manifest.Manifest) (bool, error) {
		dock := m.FindDock(dockName)
		if dock == nil {
			return false, fmt.Errorf("unknown dock %q", dockName)
		}
		changed := dock.PruneClosedEntries(time.Now().Unix())
		for i := len(dock.ClosedEntries) - 1; i >= 0; i-- {
			out = append(out, dock.ClosedEntries[i])
		}
		return changed, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MoveClosedEntriesBelowKnown moves entries discovered by a restore-triggered
// sync below the queue entries that were already known before that sync. This
// keeps known bay-initiated closes in LIFO order while still allowing the sync
// to strip dead panes before restore. Entries added concurrently by other
// commands are left where they are.
func (e *Engine) MoveClosedEntriesBelowKnown(dockName string, discovered []manifest.ClosedEntry, knownNewestFirst []manifest.ClosedEntry) error {
	if len(discovered) == 0 {
		return nil
	}
	return e.withManifestMaybe(func(m *manifest.Manifest) (bool, error) {
		dock := m.FindDock(dockName)
		if dock == nil {
			return false, fmt.Errorf("unknown dock %q", dockName)
		}
		remove := make([]bool, len(dock.ClosedEntries))
		for _, target := range discovered {
			for i := len(dock.ClosedEntries) - 1; i >= 0; i-- {
				if remove[i] || !sameClosedEntry(dock.ClosedEntries[i], target) {
					continue
				}
				remove[i] = true
				break
			}
		}

		var remaining, moved []manifest.ClosedEntry
		for i, entry := range dock.ClosedEntries {
			if remove[i] {
				moved = append(moved, entry)
				continue
			}
			remaining = append(remaining, entry)
		}
		if len(moved) == 0 {
			return false, nil
		}

		changed := false
		if len(knownNewestFirst) > 0 {
			insertAt := -1
			for i, entry := range remaining {
				for _, known := range knownNewestFirst {
					if sameClosedEntry(entry, known) {
						insertAt = i
						break
					}
				}
				if insertAt >= 0 {
					break
				}
			}
			if insertAt >= 0 {
				next := make([]manifest.ClosedEntry, 0, len(dock.ClosedEntries))
				next = append(next, remaining[:insertAt]...)
				next = append(next, moved...)
				next = append(next, remaining[insertAt:]...)
				dock.ClosedEntries = next
				changed = true
			}
		}

		// If the restore-triggered sync appended entries uncapped, enforce
		// the queue limit only after discovered entries have been moved
		// below known entries. This prevents old discovered exits from
		// evicting known user closes when the queue was already full.
		if len(dock.ClosedEntries) > manifest.ClosedQueueMax {
			dock.ClosedEntries = dock.ClosedEntries[len(dock.ClosedEntries)-manifest.ClosedQueueMax:]
			changed = true
		}
		return changed, nil
	})
}

func sameClosedEntry(a, b manifest.ClosedEntry) bool {
	if a.ClosedAt != b.ClosedAt || a.Kind != b.Kind {
		return false
	}
	if a.Surface == nil || b.Surface == nil {
		return a.Surface == nil && b.Surface == nil
	}
	return *a.Surface == *b.Surface
}

// SurfaceRestore pops the most recent undo-close entry from the dock's
// queue and recreates the closed surface. Returns the restored entry on
// success so the caller can render user-visible feedback. Returns
// ErrNothingToRestore (with nil entry) if the queue is empty after pruning.
//
// If the parent bay no longer exists (typical when the grace window
// elapsed and the bay was closed), the stale entry is discarded
// silently and ErrNothingToRestore is returned — pretending the queue was
// empty is the right UX for the Option+Z muscle-memory case.
//
// On any other restore failure, the entry stays queued so the user can
// retry after fixing the underlying issue.
func (e *Engine) SurfaceRestore(dockName string) (*manifest.ClosedEntry, error) {
	var entry *manifest.ClosedEntry
	var nothingToRestore bool

	// Peek + bay-existence check in a single manifest pass: if the
	// parent bay is gone, drop the stale entry here so we don't
	// reacquire the lock just to remove it.
	err := e.withManifestMaybe(func(m *manifest.Manifest) (bool, error) {
		dock := m.FindDock(dockName)
		if dock == nil {
			return false, fmt.Errorf("unknown dock %q", dockName)
		}
		before := len(dock.ClosedEntries)
		entry = dock.PeekClosedEntry(time.Now().Unix())
		pruned := len(dock.ClosedEntries) != before

		if entry == nil {
			nothingToRestore = true
			return pruned, nil
		}
		if entry.Kind == manifest.ClosedKindSurface && entry.Surface != nil &&
			dock.FindBayByID(entry.Surface.Bay) == nil {
			nothingToRestore = true
			dock.RemoveClosedEntryAt(entry.ClosedAt)
			return true, nil
		}
		return pruned, nil
	})
	if err != nil {
		return nil, err
	}
	if nothingToRestore {
		return nil, ErrNothingToRestore
	}

	switch entry.Kind {
	case manifest.ClosedKindSurface:
		if err := e.restoreSurfaceEntry(dockName, entry); err != nil {
			return nil, err
		}
		return entry, nil
	default:
		// Unknown kind — drop it so we don't get stuck.
		_ = e.dropClosedEntry(dockName, entry.ClosedAt)
		return nil, fmt.Errorf("unknown closed-entry kind %q", entry.Kind)
	}
}

// dropClosedEntry removes the entry with the given ClosedAt timestamp from
// the dock's undo-close queue. Used to discard stale/unrestorable entries
// so the queue doesn't get stuck.
func (e *Engine) dropClosedEntry(dockName string, closedAt int64) error {
	return e.withManifestMaybe(func(m *manifest.Manifest) (bool, error) {
		dock := m.FindDock(dockName)
		if dock == nil {
			return false, nil
		}
		return dock.RemoveClosedEntryAt(closedAt), nil
	})
}

// restoreSurfaceEntry handles Kind == ClosedKindSurface. It recreates the
// surface via SurfaceAdd (which also clears any pending bay
// auto-close from orphan-hygiene) and drops the entry on success.
// The parent-bay check has already happened in SurfaceRestore.
func (e *Engine) restoreSurfaceEntry(dockName string, entry *manifest.ClosedEntry) error {
	cs := entry.Surface
	if cs == nil {
		_ = e.dropClosedEntry(dockName, entry.ClosedAt)
		return fmt.Errorf("closed-surface entry has no payload")
	}

	// Place the restored surface back in its original tmux window when a
	// sibling in the recorded layout group still exists (via
	// RestoreLayoutGroup). For a root pane, SurfaceAdd uses
	// split-window -fb so it lands at the top/left of the layout group.
	// If the entire layout group is gone, SurfaceAdd creates a new window.
	if addErr := e.SurfaceAdd(SurfaceAddOptions{
		DockName:           dockName,
		BayName:            cs.Bay,
		Type:               cs.Type,
		Name:               cs.Name,
		Agent:              cs.Agent,
		Command:            cs.Command,
		SplitDir:           cs.SplitDir,
		RestoreLayoutGroup: cs.LayoutGroup,
		Resume:             true,
	}); addErr != nil {
		// Leave the entry queued so the user can retry after fixing the
		// underlying issue (per design "Restore is atomic from the user's
		// perspective... leave the entry in the queue").
		return fmt.Errorf("restoring surface %q: %w", cs.Name, addErr)
	}

	return e.dropClosedEntry(dockName, entry.ClosedAt)
}

// SurfaceRename renames a surface within a bay.
func (e *Engine) SurfaceRename(dockName, bayID, oldName, newName string) error {
	if err := ValidateName(newName); err != nil {
		return err
	}

	return e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
		}
		s := bay.FindSurface(oldName)
		if s == nil {
			return fmt.Errorf("surface %q not found in bay %q", oldName, bayID)
		}
		if existing := bay.FindSurface(newName); existing != nil {
			return fmt.Errorf("surface name %q already in use in bay %q", newName, bayID)
		}
		s.Name = newName
		bay.LastActive = time.Now().Unix()

		// Update tmux window names if this surface could be a tab owner
		// (i.e. it lives in a secondary layout group).
		if s.Tmux != nil && s.Tmux.LayoutGroup > 1 {
			e.updateWindowNames(bay, "")
		}
		return nil
	})
}

// nextLayoutGroup returns the next layout group number for a bay.
func nextLayoutGroup(bay *manifest.Bay) int {
	max := 0
	for _, s := range bay.Surfaces {
		if s.Tmux != nil && s.Tmux.LayoutGroup > max {
			max = s.Tmux.LayoutGroup
		}
	}
	return max + 1
}

// countSurfacesInLayoutGroup counts surfaces sharing a layout group.
func countSurfacesInLayoutGroup(bay *manifest.Bay, group int) int {
	count := 0
	for _, s := range bay.Surfaces {
		if s.Tmux != nil && s.Tmux.LayoutGroup == group {
			count++
		}
	}
	return count
}
