package engine

import (
	"fmt"
	"time"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
)

func homePath(dock *manifest.Dock) (string, error) {
	if dock == nil {
		return "", fmt.Errorf("unknown dock")
	}
	if dock.Path == "" {
		return "", fmt.Errorf("dock %q has no checkout configured", dock.Name)
	}
	return dock.Path, nil
}

func validateHomeBayLifecycleShape(dock *manifest.Dock, bay *manifest.Bay) error {
	if !manifest.IsHomeBay(bay) {
		return nil
	}
	if dock == nil {
		return fmt.Errorf("unknown dock")
	}
	if bay.Type != manifest.BayTypeHome {
		return fmt.Errorf("dock %q has a non-home bay with reserved ID %q; repair the manifest before using home", dock.Name, manifest.HomeBayID)
	}
	if bay.ID != manifest.HomeBayID || bay.Name != manifest.HomeBayID {
		return fmt.Errorf("dock %q has a malformed home bay: home bay must use reserved ID/name %q (got id=%q name=%q)", dock.Name, manifest.HomeBayID, bay.ID, bay.Name)
	}
	if bay.Path != dock.Path {
		return fmt.Errorf("dock %q has a malformed home bay: home bay path %q does not match expected dock path %q", dock.Name, bay.Path, dock.Path)
	}
	if bay.Worktree != nil {
		return fmt.Errorf("dock %q has a malformed home bay: home bay %q must not have worktree metadata", dock.Name, manifest.HomeBayID)
	}
	return nil
}

func validatePersistedHomeBayShape(dock *manifest.Dock, bay *manifest.Bay) error {
	if manifest.IsHomeBay(bay) {
		return validateHomeBayLifecycleShape(dock, bay)
	}
	if bay != nil && bay.Name == manifest.HomeBayID {
		if dock == nil {
			return fmt.Errorf("unknown dock")
		}
		return fmt.Errorf("dock %q has a non-home bay named %q; rename or close it before using home", dock.Name, manifest.HomeBayID)
	}
	return nil
}

type currentHomeMatch struct {
	Dock    *manifest.Dock
	Bay     *manifest.Bay
	Surface *manifest.Surface
}

func currentTmuxHomeMatch(m *manifest.Manifest, currentSession, currentWindowID, currentPaneID string) (currentHomeMatch, bool, error) {
	if m == nil || currentWindowID == "" {
		return currentHomeMatch{}, false, nil
	}
	for i := range m.Docks {
		dock := &m.Docks[i]
		if currentSession != "" && dock.Name != currentSession {
			continue
		}
		for j := range dock.Bays {
			bay := &dock.Bays[j]
			if !manifest.IsHomeBay(bay) {
				continue
			}
			var matchedSurface *manifest.Surface
			windowMatched := false
			for k := range bay.Surfaces {
				s := &bay.Surfaces[k]
				if s.Tmux == nil || s.Tmux.WindowID != currentWindowID {
					continue
				}
				windowMatched = true
				if currentPaneID != "" && s.Tmux.PaneID == currentPaneID {
					matchedSurface = s
					break
				}
			}
			if !windowMatched {
				continue
			}
			if err := validateHomeBayLifecycleShape(dock, bay); err != nil {
				return currentHomeMatch{}, false, err
			}
			return currentHomeMatch{Dock: dock, Bay: bay, Surface: matchedSurface}, true, nil
		}
	}
	return currentHomeMatch{}, false, nil
}

func homeSurfaceCWD(dock *manifest.Dock, bay *manifest.Bay) string {
	if manifest.IsHomeBay(bay) {
		if path, err := homePath(dock); err == nil {
			return config.ExpandPath(path)
		}
		return ""
	}
	if bay == nil {
		return ""
	}
	return config.ExpandPath(bay.Path)
}

func newHomeBay(dock *manifest.Dock) (manifest.Bay, error) {
	if _, err := homePath(dock); err != nil {
		return manifest.Bay{}, err
	}
	home := manifest.SynthesizeHomeBay(dock)
	home.LastActive = time.Now().Unix()
	return home, nil
}

func ensureHomeBay(dock *manifest.Dock) (*manifest.Bay, error) {
	if _, err := homePath(dock); err != nil {
		return nil, err
	}
	if existing := dock.FindBayByID(manifest.HomeBayID); existing != nil {
		if err := validateResolvableHome(dock); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if existing := dock.FindBay(manifest.HomeBayID); existing != nil {
		return nil, fmt.Errorf("dock %q has a non-home bay named %q; rename or close it before using home", dock.Name, manifest.HomeBayID)
	}
	home, err := newHomeBay(dock)
	if err != nil {
		return nil, err
	}
	if err := dock.AddBay(home); err != nil {
		return nil, err
	}
	return dock.FindBayByID(manifest.HomeBayID), nil
}

func validateResolvableHome(dock *manifest.Dock) error {
	m := &manifest.Manifest{Docks: []manifest.Dock{*dock}}
	_, _, err := m.ResolveBay(dock.Name + ":" + manifest.HomeBayID)
	return err
}

func removeHomeBayIfEmpty(dock *manifest.Dock) bool {
	if dock == nil {
		return false
	}
	for i := range dock.Bays {
		home := &dock.Bays[i]
		if !manifest.IsHomeBay(home) {
			continue
		}
		if len(home.Surfaces) > 0 {
			return false
		}
		if home.ID != "" {
			if err := dock.RemoveBay(home.ID); err == nil {
				return true
			}
		}
		dock.Bays = append(dock.Bays[:i], dock.Bays[i+1:]...)
		return true
	}
	return false
}

func lastHomeCloseError(dockName string) error {
	return fmt.Errorf("closing the last home surface in dock %q dismisses the dock UI/session; confirm the close or use --force", dockName)
}

func (e *Engine) homeCloseWouldDismissDock(dockName string, closingWindowIDs []string) (bool, error) {
	if len(closingWindowIDs) == 0 {
		return false, nil
	}
	closing := map[string]bool{}
	for _, id := range closingWindowIDs {
		if id != "" {
			closing[id] = true
		}
	}
	windows, err := e.Tmux.ListWindows(dockName)
	if err != nil {
		return false, fmt.Errorf("checking remaining dock windows: %w", err)
	}
	for _, win := range windows {
		if closing[win.ID] {
			continue
		}
		val, _ := e.Tmux.GetWindowOption(win.ID, "@bay-placeholder")
		if val != "1" {
			return false, nil
		}
	}
	return true, nil
}

func (e *Engine) homeSurfaceCloseWouldDismissDock(dockName string, bay *manifest.Bay, s *manifest.Surface) (bool, error) {
	if bay == nil || bay.Type != manifest.BayTypeHome || s == nil || s.Tmux == nil || s.Tmux.WindowID == "" {
		return false, nil
	}
	if exists, _ := e.Tmux.WindowExists(s.Tmux.WindowID); !exists {
		return false, nil
	}
	closingWindowID := ""
	if countSurfacesInLayoutGroup(bay, s.Tmux.LayoutGroup) <= 1 {
		closingWindowID = s.Tmux.WindowID
	} else if s.Tmux.PaneID != "" {
		if exists, _ := e.Tmux.PaneExists(s.Tmux.PaneID); exists {
			panes, err := e.Tmux.ListPanes(s.Tmux.WindowID)
			if err != nil {
				return false, fmt.Errorf("checking home panes: %w", err)
			}
			if len(panes) <= 1 {
				closingWindowID = s.Tmux.WindowID
			}
		}
	}
	if closingWindowID == "" {
		return false, nil
	}
	return e.homeCloseWouldDismissDock(dockName, []string{closingWindowID})
}

// BayCloseWouldDismissDock reports whether closing the target bay would dismiss
// the dock tmux UI. It returns true only for the final live home window in a
// dock; bay-owned placeholder windows are ignored, while arbitrary untagged
// windows still count as live UI.
func (e *Engine) BayCloseWouldDismissDock(dockName, bayID string) (bool, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return false, err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return false, fmt.Errorf("unknown dock %q", dockName)
	}
	bay := dock.FindBayByID(bayID)
	if bay == nil {
		if manifest.IsReservedBayID(bayID) {
			return false, nil
		}
		return false, fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
	}
	if bay.Type != manifest.BayTypeHome {
		return false, nil
	}
	return e.homeCloseWouldDismissDock(dockName, windowIDsForBay(bay))
}

// SurfaceCloseWouldDismissDock reports whether closing a surface would dismiss
// the dock tmux UI. It counts live tmux panes/windows rather than trusting only
// recorded manifest surfaces, so stale home records cannot mask the final live
// pane.
func (e *Engine) SurfaceCloseWouldDismissDock(dockName, bayID, surfaceName string) (bool, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return false, err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return false, fmt.Errorf("unknown dock %q", dockName)
	}
	bay := dock.FindBayByID(bayID)
	if bay == nil {
		return false, fmt.Errorf("bay %q not found in dock %q", bayID, dockName)
	}
	if bay.Type != manifest.BayTypeHome {
		return false, nil
	}
	s := bay.FindSurface(surfaceName)
	if s == nil {
		return false, fmt.Errorf("surface %q not found in bay %q", surfaceName, bayID)
	}
	return e.homeSurfaceCloseWouldDismissDock(dockName, bay, s)
}

func windowIDsForBay(bay *manifest.Bay) []string {
	seen := map[string]bool{}
	var windowIDs []string
	if bay == nil {
		return windowIDs
	}
	for _, s := range bay.Surfaces {
		if s.Tmux != nil && s.Tmux.WindowID != "" && !seen[s.Tmux.WindowID] {
			seen[s.Tmux.WindowID] = true
			windowIDs = append(windowIDs, s.Tmux.WindowID)
		}
	}
	return windowIDs
}

// Home focuses the most recent home surface in a dock, creating a home shell
// at the dock checkout when no focusable home surface exists.
func (e *Engine) Home(dockName string) error {
	e.SyncAll()

	m, err := e.LoadManifest()
	if err != nil {
		return err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return fmt.Errorf("unknown dock %q", dockName)
	}
	if _, err := homePath(dock); err != nil {
		return err
	}
	if home := dock.FindBayByID(manifest.HomeBayID); home != nil && len(home.Surfaces) > 0 {
		if e.focusHomeSurface(home) {
			return nil
		}
		if err := e.removeHomeBayRecords(dockName); err != nil {
			return err
		}
	}
	return e.SurfaceAdd(SurfaceAddOptions{
		DockName: dockName,
		BayName:  manifest.HomeBayID,
		Type:     manifest.SurfaceTypeShell,
		Name:     "shell",
		SplitDir: "",
	})
}

func (e *Engine) removeHomeBayRecords(dockName string) error {
	return e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return fmt.Errorf("unknown dock %q", dockName)
		}
		home := dock.FindBayByID(manifest.HomeBayID)
		if home == nil {
			return nil
		}
		if err := validateHomeBayLifecycleShape(dock, home); err != nil {
			return err
		}
		return dock.RemoveBay(manifest.HomeBayID)
	})
}

func (e *Engine) focusHomeSurface(home *manifest.Bay) bool {
	if home == nil {
		return false
	}
	if home.LastFocused != 0 {
		if s := home.FindSurfaceByID(home.LastFocused); e.selectSurface(s) {
			return true
		}
	}
	for i := len(home.Surfaces) - 1; i >= 0; i-- {
		if e.selectSurface(&home.Surfaces[i]) {
			return true
		}
	}
	return false
}

func (e *Engine) selectSurface(s *manifest.Surface) bool {
	if s == nil || s.Tmux == nil || s.Tmux.WindowID == "" {
		return false
	}
	if err := e.Tmux.SelectWindow(s.Tmux.WindowID); err != nil {
		return false
	}
	if s.Tmux.PaneID != "" {
		_ = e.Tmux.SelectPane(s.Tmux.PaneID)
	}
	return true
}

func (e *Engine) ensureDockSessionForHomeSurface(dockName string, dock *manifest.Dock) error {
	if dockHasRecordedTmuxSurfaces(dock) {
		_, _, err := e.ensureSessionForBay(dockName, dock.SessionID)
		return err
	}
	sessionID, err := e.ensureSession(dockName, dock.SessionID)
	if err != nil {
		return err
	}
	return e.withManifestMaybe(func(m *manifest.Manifest) (bool, error) {
		dock := m.FindDock(dockName)
		if dock == nil {
			return false, fmt.Errorf("unknown dock %q", dockName)
		}
		if dockHasRecordedTmuxSurfaces(dock) || dock.SessionID == sessionID {
			return false, nil
		}
		dock.SessionID = sessionID
		return true, nil
	})
}

// ensureHomeIfLastWindow creates a home shell when a non-home window close is
// about to leave a checkout-backed dock without any real tmux windows.
func (e *Engine) ensureHomeIfLastWindow(dockName, closingWindowID string) {
	windows, err := e.Tmux.ListWindows(dockName)
	if err != nil {
		return
	}
	remaining := 0
	for _, win := range windows {
		if win.ID == closingWindowID {
			continue
		}
		val, _ := e.Tmux.GetWindowOption(win.ID, "@bay-placeholder")
		if val != "1" {
			remaining++
		}
	}
	if remaining > 0 {
		return
	}
	m, err := e.LoadManifest()
	if err != nil {
		return
	}
	dock := m.FindDock(dockName)
	if dock == nil || dock.Path == "" {
		e.ensurePlaceholderIfLastWindow(dockName, closingWindowID)
		return
	}
	_ = e.SurfaceAdd(SurfaceAddOptions{
		DockName: dockName,
		BayName:  manifest.HomeBayID,
		Type:     manifest.SurfaceTypeShell,
		Name:     "shell",
		SplitDir: "",
	})
}
