package engine

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
)

// Context describes the current Bay location resolved from cwd and tmux.
type Context struct {
	Dock      string `json:"dock,omitempty"`
	BayID     string `json:"-"`
	Bay       string `json:"-"`
	Surface   string `json:"surface,omitempty"`
	SurfaceID int    `json:"surface_id,omitempty"`
	Path      string `json:"path,omitempty"`
}

// BayLabel returns the canonical display label for the bay portion of the
// context — "home" for the home bay, the stable BayID for unnamed bays,
// and "<id>.<name>" when both are set. Mirrors what the picker, tmux
// tabs, and `bay ls` show.
//
// Returns "" when the context has no bay (e.g. a dock-only location).
func (c Context) BayLabel() string {
	return manifest.BayLabel(c.BayID, c.Bay)
}

// MarshalJSON emits the user-facing bay vocabulary while preserving the
// internal field names used by the rest of the engine.
func (c Context) MarshalJSON() ([]byte, error) {
	type contextJSON struct {
		Dock      string `json:"dock,omitempty"`
		BayID     string `json:"bay_id,omitempty"`
		Bay       string `json:"bay,omitempty"`
		Surface   string `json:"surface,omitempty"`
		SurfaceID int    `json:"surface_id,omitempty"`
		Path      string `json:"path,omitempty"`
	}
	return json.Marshal(contextJSON(c))
}

// CurrentContext resolves the current Bay context from cwd and tmux state.
// This is a read-only query — it does NOT call SyncAll. The monitor
// handles background sync; callers that need the freshest state should
// call SyncAll explicitly before calling this.
func (e *Engine) CurrentContext() (*Context, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return nil, err
	}

	ctx := &Context{}
	cwd, _ := os.Getwd()
	if cwd != "" {
		ctx.Path = config.CanonicalPath(cwd)
	}

	currentSession, _ := e.Tmux.CurrentSession()
	currentWindowID, _ := e.Tmux.CurrentWindowID()
	currentPaneID, _ := e.Tmux.CurrentPaneID()

	// A tracked home tmux surface is the current bay context regardless of
	// where the shell in that pane has cd'd.
	if match, ok, err := currentTmuxHomeMatch(m, currentSession, currentWindowID, currentPaneID); err != nil {
		return nil, err
	} else if ok {
		ctx.Dock = match.Dock.Name
		ctx.BayID = match.Bay.ID
		ctx.Bay = match.Bay.Name
		ctx.Path = config.CanonicalPath(match.Dock.Path)
		if match.Surface != nil {
			ctx.Surface = match.Surface.Name
			ctx.SurfaceID = match.Surface.ID
		}
		return ctx, nil
	}

	// Try matching CWD against bay paths.
	for i := range m.Docks {
		dock := &m.Docks[i]
		for j := range dock.Bays {
			bay := &dock.Bays[j]
			if manifest.IsHomeBay(bay) {
				continue
			}
			if cwd != "" && config.IsPathUnder(cwd, bay.Path) {
				ctx.Dock = dock.Name
				ctx.BayID = bay.ID
				ctx.Bay = bay.Name
				// Find current surface from tmux pane.
				for _, s := range bay.Surfaces {
					if s.Tmux != nil && s.Tmux.PaneID == currentPaneID {
						ctx.Surface = s.Name
						ctx.SurfaceID = s.ID
						break
					}
				}
				return ctx, nil
			}
		}
	}

	// Fallback: match current tmux window/pane against surfaces.
	if currentWindowID != "" {
		for i := range m.Docks {
			dock := &m.Docks[i]
			for j := range dock.Bays {
				bay := &dock.Bays[j]
				for _, s := range bay.Surfaces {
					if s.Tmux == nil {
						continue
					}
					if s.Tmux.WindowID != currentWindowID {
						continue
					}
					ctx.Dock = dock.Name
					ctx.BayID = bay.ID
					ctx.Bay = bay.Name
					ctx.Path = config.CanonicalPath(bay.Path)
					if s.Tmux.PaneID == currentPaneID {
						ctx.Surface = s.Name
						ctx.SurfaceID = s.ID
					}
					return ctx, nil
				}
			}
		}
	}

	// Fallback: match tmux session to a dock.
	if currentSession != "" {
		dock := m.FindDock(currentSession)
		if dock != nil {
			ctx.Dock = currentSession
			return ctx, nil
		}
	}

	// Fallback: match CWD to a dock checkout or its worktree dir.
	for i := range m.Docks {
		dock := &m.Docks[i]
		if cwd != "" && (config.IsPathUnder(cwd, dock.Path) || config.IsPathUnder(cwd, dock.EffectiveWorktreeDir())) {
			ctx.Dock = dock.Name
			return ctx, nil
		}
	}

	if ctx.Dock != "" {
		return ctx, nil
	}
	return ctx, fmt.Errorf("not in a bay context")
}
