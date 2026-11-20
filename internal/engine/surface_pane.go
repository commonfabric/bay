package engine

import "github.com/commonfabric/bay/internal/manifest"

// LiveSurfacePanePIDs maps each of a bay's tmux surfaces to the root
// process of its pane, for surfaces whose recorded pane is verifiably
// still the bay's. It only reads tmux and the manifest, unlike SyncAll.
//
// Pane and window IDs are server-local and reused after a tmux restart,
// so a recorded "%0" may now name a pane in some other session. A
// surface is included only when the dock's session exists and carries
// the dock's session-ID marker, the recorded window is in that session,
// and the recorded pane is in that window. Surfaces that fail any check
// are absent from the map.
func (e *Engine) LiveSurfacePanePIDs(dock *manifest.Dock, bay *manifest.Bay) map[string]int {
	pids := map[string]int{}
	if dock.SessionID == "" {
		return pids
	}
	if ok, _ := e.Tmux.HasSession(dock.Name); !ok {
		return pids
	}
	if marker, _ := e.Tmux.GetSessionOption(dock.Name, sessionIDOption); marker != dock.SessionID {
		return pids
	}
	windows, err := e.Tmux.ListWindows(dock.Name)
	if err != nil {
		return pids
	}
	inSession := make(map[string]bool, len(windows))
	for _, w := range windows {
		inSession[w.ID] = true
	}
	panesByWindow := map[string]map[string]int{}
	for _, s := range bay.Surfaces {
		if s.Tmux == nil || s.Tmux.PaneID == "" || !inSession[s.Tmux.WindowID] {
			continue
		}
		panes, ok := panesByWindow[s.Tmux.WindowID]
		if !ok {
			panes = map[string]int{}
			if list, err := e.Tmux.ListPanes(s.Tmux.WindowID); err == nil {
				for _, p := range list {
					panes[p.ID] = p.PID
				}
			}
			panesByWindow[s.Tmux.WindowID] = panes
		}
		if pid, ok := panes[s.Tmux.PaneID]; ok {
			pids[s.Name] = pid
		}
	}
	return pids
}
