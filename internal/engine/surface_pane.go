package engine

import (
	"fmt"
	"slices"

	"github.com/commonfabric/bay/internal/manifest"
)

// LiveSurfacePanePIDs maps each of a bay's tmux surfaces to the root
// process of its pane, for surfaces whose recorded pane is verifiably
// still the bay's. It only reads tmux and the manifest, unlike SyncAll.
//
// Pane and window IDs are server-local and reused after a tmux restart,
// so a recorded "%0" may now name a pane in some other session. A
// surface is included only when the dock's session exists and carries
// the dock's session-ID marker, the recorded window is in that session,
// and the recorded pane is in that window and alive (a dead pane kept by
// remain-on-exit has a stale PID). Surfaces that fail any check are
// absent from the map. A tmux query that fails is an error, so a surface
// is never reported absent because tmux could not be read.
//
// With names, only those surfaces are checked, and only their windows
// are queried, so an unrelated window closing mid-lookup can't fail it.
func (e *Engine) LiveSurfacePanePIDs(dock *manifest.Dock, bay *manifest.Bay, names ...string) (map[string]int, error) {
	q, err := e.newPaneQuery(dock)
	if err != nil || q == nil {
		return map[string]int{}, err
	}
	return q.bayPanePIDs(bay, names)
}

// LiveDockPanePIDs is LiveSurfacePanePIDs for every bay in the dock,
// keyed by bay ID, checking the dock's tmux session once. A failure to
// read one bay's panes (a window closing mid-lookup, say) is reported in
// bayErrs for that bay alone; the other bays' results stand. err is for
// a failure to read the dock's session itself.
func (e *Engine) LiveDockPanePIDs(dock *manifest.Dock) (pids map[string]map[string]int, bayErrs map[string]error, err error) {
	pids = map[string]map[string]int{}
	bayErrs = map[string]error{}
	q, err := e.newPaneQuery(dock)
	if err != nil || q == nil {
		return pids, bayErrs, err
	}
	for i := range dock.Bays {
		id := dock.Bays[i].ID
		bp, err := q.bayPanePIDs(&dock.Bays[i], nil)
		if err != nil {
			bayErrs[id] = err
			continue
		}
		pids[id] = bp
	}
	return pids, bayErrs, nil
}

// paneQuery holds one dock's verified tmux state: the windows of its
// owned session, and the live panes of each window queried so far.
type paneQuery struct {
	e         *Engine
	inSession map[string]bool
	panes     map[string]map[string]int // window ID -> pane ID -> PID
}

// newPaneQuery checks that the dock's tmux session exists and is the
// dock's own (its session-ID marker matches). It returns nil when the
// session is missing or not the dock's, and an error when tmux can't be
// read.
func (e *Engine) newPaneQuery(dock *manifest.Dock) (*paneQuery, error) {
	if dock.SessionID == "" {
		return nil, nil
	}
	ok, err := e.Tmux.HasSession(dock.Name)
	if err != nil {
		return nil, fmt.Errorf("checking tmux session %q: %w", dock.Name, err)
	}
	if !ok {
		return nil, nil
	}
	marker, err := e.Tmux.GetSessionOption(dock.Name, sessionIDOption)
	if err != nil {
		return nil, fmt.Errorf("reading tmux session %q marker: %w", dock.Name, err)
	}
	if marker != dock.SessionID {
		return nil, nil
	}
	windows, err := e.Tmux.ListWindows(dock.Name)
	if err != nil {
		return nil, fmt.Errorf("listing windows of tmux session %q: %w", dock.Name, err)
	}
	q := &paneQuery{e: e, inSession: make(map[string]bool, len(windows)), panes: map[string]map[string]int{}}
	for _, w := range windows {
		q.inSession[w.ID] = true
	}
	return q, nil
}

func (q *paneQuery) bayPanePIDs(bay *manifest.Bay, names []string) (map[string]int, error) {
	pids := map[string]int{}
	for _, s := range bay.Surfaces {
		if len(names) > 0 && !slices.Contains(names, s.Name) {
			continue
		}
		if s.Tmux == nil || s.Tmux.PaneID == "" || !q.inSession[s.Tmux.WindowID] {
			continue
		}
		panes, ok := q.panes[s.Tmux.WindowID]
		if !ok {
			list, err := q.e.Tmux.ListPanes(s.Tmux.WindowID)
			if err != nil {
				return nil, fmt.Errorf("listing panes of window %s: %w", s.Tmux.WindowID, err)
			}
			panes = make(map[string]int, len(list))
			for _, p := range list {
				if !p.Dead { // a dead pane's PID is stale
					panes[p.ID] = p.PID
				}
			}
			q.panes[s.Tmux.WindowID] = panes
		}
		if pid, ok := panes[s.Tmux.PaneID]; ok {
			pids[s.Name] = pid
		}
	}
	return pids, nil
}
