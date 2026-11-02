package engine

import (
	"fmt"
	"time"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
)

// Edit returns the bay path for opening in an editor and bumps
// LastActive so the monitor's activity gate keeps fetching merge data
// for this bay's repo.
func (e *Engine) Edit(dockName, bayID string) (string, error) {
	var path string
	err := e.withManifest(func(m *manifest.Manifest) error {
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
				home, err := homePath(dock)
				if err != nil {
					return err
				}
				path = config.ExpandPath(home)
				return nil
			}
			return fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
		}
		if manifest.IsReservedBayID(bayID) {
			if err := validateResolvableHome(dock); err != nil {
				return err
			}
			home, err := homePath(dock)
			if err != nil {
				return err
			}
			bay.LastActive = time.Now().Unix()
			path = config.ExpandPath(home)
			return nil
		}
		bay.LastActive = time.Now().Unix()
		path = bay.Path
		return nil
	})
	return path, err
}

// EditAll returns all active bay paths for a single dock.
func (e *Engine) EditAll(dockName string) ([]string, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return nil, err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return nil, fmt.Errorf("unknown dock %q", dockName)
	}
	var paths []string
	for _, bay := range dock.Bays {
		if !manifest.IsHomeBay(&bay) && bay.Path != "" {
			paths = append(paths, bay.Path)
		}
	}
	return paths, nil
}

// DockEditorAdd creates a dock-level editor surface. Creates a tmux window
// at index 0, tagged with @bay-dock-editor, and runs the editor command.
// If the editor window already exists, focuses it instead.
func (e *Engine) DockEditorAdd(dockName, editorCmd, editPath string) error {
	// Check if dock editor window already exists in tmux.
	if winID, found := e.Tmux.FindDockEditorWindow(dockName); found {
		return e.Tmux.SelectWindow(winID)
	}

	// Create a new tmux window.
	winID, err := e.Tmux.NewWindow(dockName, "edit", editPath)
	if err != nil {
		return fmt.Errorf("creating dock editor window: %w", err)
	}

	// Tag it and move to index 0.
	_ = e.Tmux.SetWindowOption(winID, "@bay-dock-editor", "1")
	_ = e.Tmux.MoveWindow(winID, 0)

	// Get the pane ID.
	panes, err := e.Tmux.ListPanes(winID)
	if err != nil || len(panes) == 0 {
		return fmt.Errorf("listing panes for dock editor: %w", err)
	}
	paneID := panes[0].ID

	// Launch the editor.
	fullCmd := editorCmd + " " + editPath
	_ = e.Tmux.RespawnPane(paneID, editPath, fullCmd)

	// Record in manifest.
	return e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		// Remove any stale dock editor surface.
		for i := range dock.Surfaces {
			if dock.Surfaces[i].Type == manifest.SurfaceTypeEditor {
				dock.Surfaces = append(dock.Surfaces[:i], dock.Surfaces[i+1:]...)
				break
			}
		}
		cmd := fullCmd
		dock.AddDockSurface(manifest.Surface{
			Name:    "edit",
			Type:    manifest.SurfaceTypeEditor,
			Backend: manifest.SurfaceBackendTmux,
			Command: &cmd,
			Tmux: &manifest.TmuxAttrs{
				PaneID:   paneID,
				WindowID: winID,
			},
		})
		return nil
	})
}

// EditAllParentDir returns the worktree parent directory for a dock.
func (e *Engine) EditAllParentDir(dockName string) (string, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return "", err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return "", fmt.Errorf("unknown dock %q", dockName)
	}
	if dock.Path == "" {
		return "", fmt.Errorf("dock %q has no checkout configured", dockName)
	}
	return dock.EffectiveWorktreeDir(), nil
}
