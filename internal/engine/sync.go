package engine

import (
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
)

// syncProbeConcurrency caps the number of concurrent probe goroutines in
// SyncAll. Each probe shells out (CurrentBranch, optional PRForBranch via
// gh, IsMergedIntoDefault) — gh is the slowest at ~500ms, so bounding keeps
// display latency reasonable on docks with many fresh bays.
const syncProbeConcurrency = 10

// orphanGraceSeconds is how long a newly-emptied bay lingers
// before auto-close finalizes. The user can cancel by re-adding a
// surface (bay sf new --bay <id>) during this window. Var (not
// const) so tests can set it to 0 to exercise immediate finalization.
var orphanGraceSeconds int64 = 60

type baySyncUpdate struct {
	dockName           string
	originalID         string
	path               string
	branch             string
	branchChanged      bool
	branchDetached     bool
	detachedBranch     string // previous branch name when branchDetached is true
	branchSafeToDelete bool   // detached branch was pushed; safe to delete locally
	pr                 string
	prBranch           string
	prChanged          bool
	merged             bool
	deadSurfaceIDs     map[int]bool
}

// SyncAll checks git branches, PR numbers, merge status, and tmux surface state
// for all bays and updates the manifest if anything changed. It returns
// undo-close entries discovered from dead bay surfaces during this sync
// pass; callers that don't care can ignore the return value.
//
// The probe phase runs in parallel (bounded to syncProbeConcurrency) so that
// fresh docks with many bays don't pay N × ~500ms gh latency on first
// display. Probes only read the manifest, so the parallelism is safe.
func (e *Engine) SyncAll() []manifest.ClosedEntry {
	return e.syncAll(true)
}

// SyncAllForRestore is the restore-path variant of SyncAll. It temporarily
// leaves sync-discovered undo entries uncapped so the caller can move them
// below already-known entries before enforcing the queue cap.
func (e *Engine) SyncAllForRestore() []manifest.ClosedEntry {
	return e.syncAll(false)
}

func (e *Engine) syncAll(enforceClosedQueueCap bool) []manifest.ClosedEntry {
	m, err := e.LoadManifest()
	if err != nil {
		return nil
	}

	// Resolve session ownership once per dock so the per-bay
	// probe and the dock-surface cleanup below don't each spawn
	// `tmux show-options` per bay. Captured before the parallel
	// fanout so the read is unsynchronized (the map is only read from
	// goroutines after this loop, never mutated).
	//
	// This pass also opportunistically tags legacy docks (manifest
	// SessionID empty AND tmux marker empty) BUT only when the dock
	// holds no recorded tmux surfaces. Tmux pane/window IDs are
	// server-local and reset on restart, so a fresh same-name session
	// can have low-numbered IDs that collide with the manifest's
	// records — "ID exists in the live server" doesn't actually prove
	// the session is the one bay set up. Refusing to backfill when
	// surfaces are recorded leaves the dock in unowned state (cleanup
	// skipped, surfaces preserved) until `bay recover` reconciles.
	dockOwned := make(map[string]bool, len(m.Docks))
	legacyBackfill := map[string]string{}
	for i := range m.Docks {
		dock := &m.Docks[i]
		exists, _ := e.Tmux.HasSession(dock.Name)
		if !exists {
			continue
		}
		marker, _ := e.Tmux.GetSessionOption(dock.Name, sessionIDOption)
		if marker == "" && dock.SessionID == "" && !dockHasRecordedTmuxSurfaces(dock) {
			id := newSessionID()
			if err := e.Tmux.SetSessionOption(dock.Name, sessionIDOption, id); err == nil {
				marker = id
				dock.SessionID = id
				legacyBackfill[dock.Name] = id
			}
		}
		dockOwned[dock.Name] = marker != "" && marker == dock.SessionID
	}
	if len(legacyBackfill) > 0 {
		applied := map[string]bool{}
		err := e.withManifest(func(m *manifest.Manifest) error {
			for name, id := range legacyBackfill {
				if d := m.FindDock(name); d != nil {
					switch d.SessionID {
					case "":
						d.SessionID = id
						applied[name] = true
					case id:
						applied[name] = true
					}
				}
			}
			return nil
		})
		for name := range legacyBackfill {
			if err != nil || !applied[name] {
				dockOwned[name] = false
			}
		}
	}

	type probeJob struct {
		dock *manifest.Dock
		bay  *manifest.Bay
	}
	var jobs []probeJob
	for i := range m.Docks {
		dock := &m.Docks[i]
		for j := range dock.Bays {
			jobs = append(jobs, probeJob{dock: dock, bay: &dock.Bays[j]})
		}
	}
	if len(jobs) == 0 {
		return nil
	}

	results := make([]baySyncUpdate, len(jobs))
	valid := make([]bool, len(jobs))

	sem := make(chan struct{}, syncProbeConcurrency)
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, job probeJob) {
			defer wg.Done()
			defer func() { <-sem }()
			if update, ok := e.probeBaySync(job.dock, job.bay, dockOwned[job.dock.Name]); ok {
				results[i] = update
				valid[i] = true
			}
		}(i, job)
	}
	wg.Wait()

	var updates []baySyncUpdate
	for i, ok := range valid {
		if ok {
			updates = append(updates, results[i])
		}
	}
	// Note: no early return on `len(updates) == 0` — the pending-close
	// scan in the closure below must still run to finalize any
	// previously-scheduled auto-closes whose grace windows have
	// elapsed since the last sync.

	var dockNames []string
	var discoveredClosed []manifest.ClosedEntry
	// Bays whose PendingCloseAt has expired — finalize via
	// BayClose after the lock releases (BayClose acquires its own lock).
	type orphanCandidate struct{ dock, bay string }
	var toFinalize []orphanCandidate

	syncErr := e.withManifestMaybe(func(m *manifest.Manifest) (bool, error) {
		changed := false
		for _, update := range updates {
			updateChanged, closedEntries := e.applyBaySyncUpdate(m, update, enforceClosedQueueCap)
			if updateChanged {
				changed = true
			}
			discoveredClosed = append(discoveredClosed, closedEntries...)
		}

		// Clean up dead dock-level surfaces only when the session
		// identity precompute above proved this is bay's current
		// session.
		for i := range m.Docks {
			dock := &m.Docks[i]
			if len(dock.Surfaces) == 0 {
				continue
			}
			if !dockOwned[dock.Name] {
				continue
			}
			live := dock.Surfaces[:0]
			for _, s := range dock.Surfaces {
				if s.Tmux != nil && s.Tmux.PaneID != "" {
					exists, _ := e.Tmux.PaneExists(s.Tmux.PaneID)
					if !exists {
						changed = true
						continue
					}
				}
				live = append(live, s)
			}
			dock.Surfaces = live
		}

		// Scan for pending-close bays: clear the flag if a
		// surface was re-added since scheduling (user saved it);
		// collect the expired ones for post-lock finalization.
		now := time.Now().Unix()
		for di := range m.Docks {
			dock := &m.Docks[di]
			for wi := 0; wi < len(dock.Bays); {
				bay := &dock.Bays[wi]
				if manifest.IsHomeBay(bay) {
					if bay.PendingCloseAt != 0 {
						bay.PendingCloseAt = 0
						changed = true
					}
					if len(bay.Surfaces) == 0 {
						if removeHomeBayIfEmpty(dock) {
							changed = true
							continue
						}
					}
					wi++
					continue
				}
				if bay.PendingCloseAt == 0 {
					wi++
					continue
				}
				if len(bay.Surfaces) > 0 {
					bay.PendingCloseAt = 0
					changed = true
					wi++
					continue
				}
				if bay.PendingCloseAt <= now {
					toFinalize = append(toFinalize, orphanCandidate{dock.Name, bay.ID})
				}
				wi++
			}
		}

		// Capture dock names so we can refresh tab names after the
		// lock is released (tmux RPCs shouldn't block the manifest).
		dockNames = make([]string, len(m.Docks))
		for i := range m.Docks {
			dockNames[i] = m.Docks[i].Name
		}
		return changed, nil
	})
	if syncErr != nil {
		discoveredClosed = nil
	}

	// Finalize expired pending closes. Best-effort: any failure
	// (gate refused, I/O error) leaves PendingCloseAt set; we clear
	// it on gate failures to stop retrying every sync pass (a
	// permanent orphan is on the user to resolve).
	for _, o := range toFinalize {
		if stillEmpty, err := e.bayIsEmpty(o.dock, o.bay); err != nil || !stillEmpty {
			continue
		}
		if err := e.BayClose(o.dock, o.bay, false); err != nil {
			e.clearPendingClose(o.dock, o.bay)
		}
	}

	// Refresh tab name lengths for all docks.
	for _, name := range dockNames {
		e.refreshDockWindowNamesByName(name)
	}
	return discoveredClosed
}

func (e *Engine) probeBaySync(dock *manifest.Dock, bay *manifest.Bay, sessionOwned bool) (baySyncUpdate, bool) {
	update := baySyncUpdate{
		dockName:   dock.Name,
		originalID: bay.ID,
		path:       bay.Path,
	}

	if !manifest.IsHomeBay(bay) && bay.Path != "" && bay.Worktree != nil {
		bayPath := config.ExpandPath(bay.Path)
		if _, err := os.Stat(bayPath); err == nil {
			branch, err := e.Git.CurrentBranch(bayPath)
			if err == nil && branch != "" && branch != bay.Worktree.Branch {
				update.branch = branch
				update.branchChanged = true
			} else if err == nil && branch == "" && bay.Worktree.Branch != "" {
				update.branchDetached = true
				update.detachedBranch = bay.Worktree.Branch
				// Check now (in the probe) so the apply phase doesn't
				// hold the manifest lock during a shell-out.
				if unpushed, upErr := e.HasUnlandedCommits(bay); upErr == nil && !unpushed {
					update.branchSafeToDelete = true
				}
			}

			branchForChecks := bay.Worktree.Branch
			if update.branchChanged {
				branchForChecks = update.branch
			}
			// PR check: a branch change always forces a re-query (the
			// cached PR belongs to the old branch). Otherwise the
			// TTL-based sentinel decides — we re-query after PRCheckTTL
			// even if the previous answer was "no PR", so a PR opened
			// after the first check is eventually picked up.
			now := time.Now().Unix()
			shouldCheckPR := branchForChecks != "" &&
				(update.branchChanged || bay.Worktree.NeedsPRCheck(now))
			if shouldCheckPR {
				pr, err := e.Git.PRForBranch(bayPath, branchForChecks)
				if err == nil {
					update.pr = pr
					update.prBranch = branchForChecks
					update.prChanged = true
				}
			}
			// Re-check merge status on branchChanged even if the stored
			// flag says merged — the flag belongs to the old branch.
			// Apply clears it below; this probe then re-populates it for
			// the new branch when appropriate.
			if branchForChecks != "" && (update.branchChanged || !bay.IsMerged()) {
				merged, err := e.Git.IsMergedIntoDefault(bayPath, branchForChecks)
				if err == nil && merged {
					update.merged = true
				}
			}
		}
	}

	// Only clean up dead surfaces if the dock's tmux session is alive
	// AND it's the session bay set up. If the session is gone, foreign,
	// or in-flight (e.g., a recover that hasn't persisted new pane IDs
	// yet), the surfaces are needed for `bay recover` to know what to
	// recreate. Stripping them here would leave bays with surfaces=0 and
	// recover would report "nothing to recover."
	if sessionOwned {
		update.deadSurfaceIDs = e.deadSurfaceIDs(bay)
	}
	if !update.branchChanged && !update.branchDetached && !update.prChanged && !update.merged && len(update.deadSurfaceIDs) == 0 {
		return baySyncUpdate{}, false
	}
	return update, true
}

func (e *Engine) applyBaySyncUpdate(m *manifest.Manifest, update baySyncUpdate, enforceClosedQueueCap bool) (bool, []manifest.ClosedEntry) {
	dock := m.FindDock(update.dockName)
	if dock == nil {
		return false, nil
	}

	var bay *manifest.Bay
	if manifest.IsReservedBayID(update.originalID) {
		bay = dock.FindBayByID(update.originalID)
	} else {
		bay = findBayByPath(dock, update.path)
		if bay == nil {
			bay = dock.FindBayByID(update.originalID)
		}
	}
	if bay == nil {
		return false, nil
	}
	changed := false
	var discoveredClosed []manifest.ClosedEntry
	if update.branchChanged && bay.Worktree != nil && bay.Worktree.Branch != update.branch {
		bay.Worktree.Branch = update.branch
		// Cached PR belongs to the old branch — invalidate it. The probe
		// will already have queried gh for the new branch (via the
		// branchChanged bypass) and may set PR below. If the probe's
		// query failed transiently, the cleared state means future syncs
		// treat this as fresh and try again, rather than displaying the
		// wrong PR.
		bay.Worktree.PR = ""
		bay.Worktree.PRCheckedAt = 0
		// Merged belongs to the old branch too. Without this, a worktree
		// that gets reused after its prior branch was merged stays stuck
		// at Merged=true — blocking PR detection and display until the
		// bay is recreated.
		bay.Worktree.Merged = false
		changed = true
		// Sticky-once-set: only fill Name from the branch when it's a
		// placeholder (empty or generated b<N>/legacy w<N>). User-set Names are
		// stable — live branch info is on right-status, so the tab label
		// doesn't need to follow.
		if isPlaceholderName(bay.Name) {
			newName := uniqueBayName(dock, bay, abbreviateBranch(update.branch))
			if newName != bay.Name {
				bay.Name = newName
				e.updateWindowNames(bay, "")
				changed = true
			}
		}
	}

	if update.branchDetached && bay.Worktree != nil && bay.Worktree.Branch != "" {
		// Delete the local branch if the probe determined it was pushed.
		// Without this, the branch name is lost once metadata is cleared,
		// and closeBayState can never clean it up.
		if update.branchSafeToDelete && dock.Path != "" {
			repoPath := config.ExpandPath(dock.Path)
			_ = e.Git.DeleteBranch(repoPath, update.detachedBranch)
		}
		bay.Worktree.Branch = ""
		bay.Worktree.PR = ""
		bay.Worktree.PRCheckedAt = 0
		// No branch → no meaningful "merged" status; clear so the flag
		// doesn't resurrect when the worktree gets a new branch later.
		bay.Worktree.Merged = false
		changed = true
		// Name stays sticky: a previously-set Name persists even after the
		// branch is gone. The display may be slightly stale relative to the
		// current worktree state, but stability matters more, and right-status
		// shows the live branch (or its absence). The user can bay rename if
		// they want the label to follow.
	}

	if update.prChanged && bay.Worktree != nil && bay.Worktree.Branch == update.prBranch && bay.Worktree.PR == "" {
		bay.Worktree.PR = update.pr
		// Use time.Now() rather than a probe-time timestamp so concurrent
		// processes can't write a stale value over a fresher one.
		bay.Worktree.PRCheckedAt = time.Now().Unix()
		changed = true
	}

	if update.merged && bay.Worktree != nil && !bay.IsMerged() {
		bay.Worktree.Merged = true
		changed = true
	}

	if len(update.deadSurfaceIDs) > 0 {
		live := bay.Surfaces[:0]
		var dead []manifest.Surface
		removed := false
		closedAt := time.Now().Unix()
		for i := range bay.Surfaces {
			s := &bay.Surfaces[i]
			if update.deadSurfaceIDs[s.ID] {
				removed = true
				dead = append(dead, *s)
				continue
			}
			live = append(live, *s)
		}
		if removed {
			// Push in reverse manifest order so LIFO restore recreates a
			// whole-window death in its original visual order.
			for i := len(dead) - 1; i >= 0; i-- {
				if entry, ok := closedEntryForSurface(bay.ID, &dead[i], closedAt); ok {
					if enforceClosedQueueCap {
						dock.PushClosedEntry(entry)
					} else {
						dock.ClosedEntries = append(dock.ClosedEntries, entry)
					}
					discoveredClosed = append(discoveredClosed, entry)
				}
			}
			bay.Surfaces = live
			changed = true
			// If the strip emptied the bay, schedule auto-close
			// after a grace window. The user has that long to re-open
			// a surface (bay sf new --bay <id>) to cancel.
			if len(bay.Surfaces) == 0 && manifest.IsHomeBay(bay) {
				removeHomeBayIfEmpty(dock)
			} else if len(bay.Surfaces) == 0 && bay.PendingCloseAt == 0 {
				bay.PendingCloseAt = time.Now().Unix() + orphanGraceSeconds
			}
		}
	}

	return changed, discoveredClosed
}

// clearPendingClose resets PendingCloseAt on a bay to 0. Used
// after a finalize attempt fails so the orphan doesn't get retried
// on every sync pass; the user must resolve the underlying issue
// (dirty, unlanded) and close manually.
func (e *Engine) clearPendingClose(dockName, bayID string) {
	_ = e.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		if dock == nil {
			return nil
		}
		bay := dock.FindBayByID(bayID)
		if bay == nil {
			return nil
		}
		bay.PendingCloseAt = 0
		return nil
	})
}

// bayIsEmpty returns true if the bay exists and has zero
// surfaces at this moment. Used by the orphan auto-close guard to
// skip bays that gained a surface concurrently with sync.
func (e *Engine) bayIsEmpty(dockName, bayID string) (bool, error) {
	m, err := e.LoadManifest()
	if err != nil {
		return false, err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return false, nil
	}
	bay := dock.FindBayByID(bayID)
	if bay == nil {
		return false, nil
	}
	return len(bay.Surfaces) == 0, nil
}

func (e *Engine) deadSurfaceIDs(bay *manifest.Bay) map[int]bool {
	dead := map[int]bool{}
	for _, s := range bay.Surfaces {
		if s.Tmux != nil && s.Tmux.PaneID != "" {
			exists, _ := e.Tmux.PaneExists(s.Tmux.PaneID)
			if !exists {
				dead[s.ID] = true
			}
		}
	}
	return dead
}

// processAlive checks if a process with the given PID is still running.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
