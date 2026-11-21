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
	pids, errs := q.bayPanePIDs(bay, names)
	for _, s := range bay.Surfaces { // first failure in surface order
		if err := errs[s.Name]; err != nil {
			return nil, err
		}
	}
	return pids, nil
}

// LiveDockPanePIDs is LiveSurfacePanePIDs for every bay in the dock,
// keyed by bay ID then surface name, checking the dock's tmux session
// once. A surface whose window can't be read (it closed mid-lookup,
// say) is reported in surfErrs, keyed the same way, for that surface
// alone; every other surface's result stands, including other surfaces
// of the same bay. err is for a failure to read the dock's session.
func (e *Engine) LiveDockPanePIDs(dock *manifest.Dock) (pids map[string]map[string]int, surfErrs map[string]map[string]error, err error) {
	pids = map[string]map[string]int{}
	surfErrs = map[string]map[string]error{}
	q, err := e.newPaneQuery(dock)
	if err != nil || q == nil {
		return pids, surfErrs, err
	}
	for i := range dock.Bays {
		id := dock.Bays[i].ID
		bp, errs := q.bayPanePIDs(&dock.Bays[i], nil)
		pids[id] = bp
		if len(errs) > 0 {
			surfErrs[id] = errs
		}
	}
	return pids, surfErrs, nil
}

// paneQuery holds one dock's verified tmux state: the windows of its
// owned session, and the live panes of each window queried so far.
type paneQuery struct {
	e         *Engine
	inSession map[string]bool
	panes     map[string]map[string]int // window ID -> pane ID -> PID
	failed    map[string]error          // window ID -> ListPanes failure
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
	q := &paneQuery{e: e, inSession: make(map[string]bool, len(windows)), panes: map[string]map[string]int{}, failed: map[string]error{}}
	for _, w := range windows {
		q.inSession[w.ID] = true
	}
	return q, nil
}

// bayPanePIDs resolves the bay's surfaces (or only names) to live pane
// PIDs. A surface whose window can't be listed gets an entry in errs
// instead; the failure is remembered for that window, so its other
// surfaces get the same error without querying tmux again.
func (q *paneQuery) bayPanePIDs(bay *manifest.Bay, names []string) (pids map[string]int, errs map[string]error) {
	pids = map[string]int{}
	errs = map[string]error{}
	for _, s := range bay.Surfaces {
		if len(names) > 0 && !slices.Contains(names, s.Name) {
			continue
		}
		if s.Tmux == nil || s.Tmux.PaneID == "" || !q.inSession[s.Tmux.WindowID] {
			continue
		}
		panes, err := q.windowPanes(s.Tmux.WindowID)
		if err != nil {
			errs[s.Name] = err
			continue
		}
		if pid, ok := panes[s.Tmux.PaneID]; ok {
			pids[s.Name] = pid
		}
	}
	return pids, errs
}

// windowPanes returns the window's live panes (pane ID -> PID), querying
// tmux once per window and remembering a failure as well as a result.
func (q *paneQuery) windowPanes(windowID string) (map[string]int, error) {
	if err, ok := q.failed[windowID]; ok {
		return nil, err
	}
	if panes, ok := q.panes[windowID]; ok {
		return panes, nil
	}
	list, err := q.e.Tmux.ListPanes(windowID)
	if err != nil {
		err = fmt.Errorf("listing panes of window %s: %w", windowID, err)
		q.failed[windowID] = err
		return nil, err
	}
	panes := make(map[string]int, len(list))
	for _, p := range list {
		if !p.Dead { // a dead pane's PID is stale
			panes[p.ID] = p.PID
		}
	}
	q.panes[windowID] = panes
	return panes, nil
}
