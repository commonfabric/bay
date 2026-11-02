package engine

import (
	"testing"

	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/tmux"
)

// seedSurfaceBay adds a bay with one surface whose recorded PaneID
// does not exist in the mock tmux server. With normal cleanup enabled
// (matching marker), SyncAll would mark the surface dead and strip
// it. The tests below toggle the marker state to verify the gate's
// owned/preserve decisions.
func seedSurfaceBay(t *testing.T, eng *Engine, dockName, bayName string) {
	t.Helper()
	err := eng.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		dock.Bays = append(dock.Bays, manifest.Bay{
			Name: bayName,
			Type: manifest.BayTypeWorktree,
			Surfaces: []manifest.Surface{
				{
					ID:      1,
					Name:    "agent",
					Type:    manifest.SurfaceTypeAgent,
					Backend: manifest.SurfaceBackendTmux,
					Tmux: &manifest.TmuxAttrs{
						WindowID: "@99",
						PaneID:   "%999", // not in the mock — would be flagged dead
					},
				},
			},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("seed surface bay: %v", err)
	}
}

func seedLiveSurfaceBay(t *testing.T, eng *Engine, dockName, bayName string) {
	t.Helper()
	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession(dockName); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	windowID, err := mockTmux.NewWindow(dockName, bayName, "")
	if err != nil {
		t.Fatalf("seed window: %v", err)
	}
	panes, err := mockTmux.ListPanes(windowID)
	if err != nil || len(panes) == 0 {
		t.Fatalf("seed panes: panes=%v err=%v", panes, err)
	}
	paneID := panes[0].ID

	err = eng.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		dock.Bays = append(dock.Bays, manifest.Bay{
			Name: bayName,
			Type: manifest.BayTypeWorktree,
			Surfaces: []manifest.Surface{
				{
					ID:      1,
					Name:    "agent",
					Type:    manifest.SurfaceTypeAgent,
					Backend: manifest.SurfaceBackendTmux,
					Tmux: &manifest.TmuxAttrs{
						WindowID: windowID,
						PaneID:   paneID,
					},
				},
			},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("seed live surface bay: %v", err)
	}
}

// setSessionID sets the manifest's Dock.SessionID for a dock.
func setSessionID(t *testing.T, eng *Engine, dockName, sessionID string) {
	t.Helper()
	err := eng.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock(dockName)
		dock.SessionID = sessionID
		return nil
	})
	if err != nil {
		t.Fatalf("set session id: %v", err)
	}
}

// surfaceCount returns the surface count for a bay.
func surfaceCount(t *testing.T, eng *Engine, dockName, bayName string) int {
	t.Helper()
	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		t.Fatalf("dock %q missing", dockName)
	}
	bay := dock.FindBay(bayName)
	if bay == nil {
		t.Fatalf("bay %q missing", bayName)
	}
	return len(bay.Surfaces)
}

// --- gate matrix ---

// TestSyncAll_PreservesSurfacesAfterServerRestart simulates the user-
// reported bug: tmux died and a fresh empty session of the same name
// came back up (e.g., user attached, wrapper script started a session).
// The manifest still has the pre-crash SessionID; tmux has no marker.
// SyncAll must NOT strip surfaces — recover hasn't run yet.
func TestSyncAll_PreservesSurfacesAfterServerRestart(t *testing.T) {
	eng, _ := testEngine(t)
	seedSurfaceBay(t, eng, "labs", "auth-fix")
	setSessionID(t, eng, "labs", "old-session-uuid")

	// Tmux has the dock's session name but no @bay-session-id marker
	// (fresh session after a restart).
	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	eng.SyncAll()

	if got := surfaceCount(t, eng, "labs", "auth-fix"); got != 1 {
		t.Errorf("surfaces stripped after restart: got %d, want 1 (preserved)", got)
	}
}

// TestSyncAll_PreservesSurfacesWhenMarkerSetButManifestEmpty covers
// the in-flight recover case: ensureSession has tagged tmux with a new
// UUID, but mergeRecoveredDockState hasn't persisted SessionID yet.
// A probe that fires in this window must not strip surfaces.
func TestSyncAll_PreservesSurfacesWhenMarkerSetButManifestEmpty(t *testing.T) {
	eng, _ := testEngine(t)
	seedSurfaceBay(t, eng, "labs", "auth-fix")
	// Manifest SessionID stays empty (testEngine seeds it that way) —
	// simulates pre-merge state during recover.

	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := mockTmux.SetSessionOption("labs", sessionIDOption, "fresh-uuid"); err != nil {
		t.Fatalf("set marker: %v", err)
	}

	// But the dock had a prior SessionID — without one, the (empty,
	// empty) → owned legacy branch would fire instead.
	setSessionID(t, eng, "labs", "")

	// Force the (set marker, empty manifest) state for a meaningful test.
	// Manifest is already empty; tmux now has the marker.
	eng.SyncAll()

	if got := surfaceCount(t, eng, "labs", "auth-fix"); got != 1 {
		t.Errorf("surfaces stripped during in-flight recover: got %d, want 1 (preserved)", got)
	}
}

// TestSyncAll_PreservesSurfacesOnMismatchedMarker covers the post-
// crash case where bay's recover ran on a different bay process or
// session: tmux was re-tagged with a new UUID while the manifest
// kept the old one.
func TestSyncAll_PreservesSurfacesOnMismatchedMarker(t *testing.T) {
	eng, _ := testEngine(t)
	seedSurfaceBay(t, eng, "labs", "auth-fix")
	setSessionID(t, eng, "labs", "manifest-uuid")

	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := mockTmux.SetSessionOption("labs", sessionIDOption, "different-uuid"); err != nil {
		t.Fatalf("set marker: %v", err)
	}

	eng.SyncAll()

	if got := surfaceCount(t, eng, "labs", "auth-fix"); got != 1 {
		t.Errorf("surfaces stripped on mismatched marker: got %d, want 1 (preserved)", got)
	}
}

// TestSyncAll_StripsDeadSurfacesWhenMarkerMatches verifies normal
// operation: when the manifest's SessionID matches tmux's marker, the
// gate opens and dead pane IDs (e.g., panes the user closed manually)
// get stripped as before.
func TestSyncAll_StripsDeadSurfacesWhenMarkerMatches(t *testing.T) {
	eng, _ := testEngine(t)
	seedSurfaceBay(t, eng, "labs", "auth-fix")
	setSessionID(t, eng, "labs", "shared-uuid")

	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := mockTmux.SetSessionOption("labs", sessionIDOption, "shared-uuid"); err != nil {
		t.Fatalf("set marker: %v", err)
	}

	eng.SyncAll()

	if got := surfaceCount(t, eng, "labs", "auth-fix"); got != 0 {
		t.Errorf("dead surface not stripped on matching marker: got %d, want 0", got)
	}
}

// TestSyncAll_PreservesStaleSurfacesInLegacyEmptyState covers the
// ambiguous legacy state: pre-rollout manifest, untagged same-name
// session, and recorded panes that are not live. This can be either
// manually killed panes or a fresh session after tmux restart; preserve
// so recover remains possible.
func TestSyncAll_PreservesStaleSurfacesInLegacyEmptyState(t *testing.T) {
	eng, _ := testEngine(t)
	seedSurfaceBay(t, eng, "labs", "auth-fix")
	// SessionID stays empty.

	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// No marker set.

	eng.SyncAll()

	if got := surfaceCount(t, eng, "labs", "auth-fix"); got != 1 {
		t.Errorf("legacy stale surface stripped: got %d, want 1 (preserved)", got)
	}
}

// TestSyncAll_PreservesSurfacesWhenSessionFullyDead is today's
// existing protection — verified to still work after the rewrite.
func TestSyncAll_PreservesSurfacesWhenSessionFullyDead(t *testing.T) {
	eng, _ := testEngine(t)
	seedSurfaceBay(t, eng, "labs", "auth-fix")
	setSessionID(t, eng, "labs", "any-uuid")
	// No tmux session at all.

	eng.SyncAll()

	if got := surfaceCount(t, eng, "labs", "auth-fix"); got != 1 {
		t.Errorf("surfaces stripped when session is dead: got %d, want 1 (preserved)", got)
	}
}

// --- ensureSession matrix ---

// withDeterministicSessionID swaps newSessionID for a counter-based
// generator so tests can predict the assigned UUID. Returns a restore
// func to call in t.Cleanup.
func withDeterministicSessionID(t *testing.T, ids ...string) {
	t.Helper()
	prev := newSessionID
	idx := 0
	newSessionID = func() string {
		if idx >= len(ids) {
			t.Fatalf("newSessionID called more times than expected: %d > %d", idx+1, len(ids))
		}
		id := ids[idx]
		idx++
		return id
	}
	t.Cleanup(func() { newSessionID = prev })
}

// TestEnsureSession_TagsNewSession exercises the "no session" branch:
// session is created and tagged with a fresh UUID.
func TestEnsureSession_TagsNewSession(t *testing.T) {
	eng, _ := testEngine(t)
	withDeterministicSessionID(t, "fresh-uuid")

	got, err := eng.ensureSession("new-dock", "")
	if err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	if got != "fresh-uuid" {
		t.Errorf("returned id = %q, want fresh-uuid", got)
	}
	mockTmux := eng.Tmux.(*tmux.Mock)
	marker, _ := mockTmux.GetSessionOption("new-dock", sessionIDOption)
	if marker != "fresh-uuid" {
		t.Errorf("tmux marker = %q, want fresh-uuid", marker)
	}
}

// TestEnsureSession_NoOpOnMatchingMarker covers the steady-state case:
// session is alive, marker matches expected — no work, no UUID churn.
func TestEnsureSession_NoOpOnMatchingMarker(t *testing.T) {
	eng, _ := testEngine(t)

	mockTmux := eng.Tmux.(*tmux.Mock)
	_ = mockTmux.NewSession("labs")
	_ = mockTmux.SetSessionOption("labs", sessionIDOption, "stable-uuid")

	got, err := eng.ensureSession("labs", "stable-uuid")
	if err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	if got != "stable-uuid" {
		t.Errorf("returned id = %q, want stable-uuid", got)
	}
	// Marker unchanged.
	marker, _ := mockTmux.GetSessionOption("labs", sessionIDOption)
	if marker != "stable-uuid" {
		t.Errorf("marker churned: %q, want stable-uuid", marker)
	}
}

// TestEnsureSession_AdoptsExistingMarker covers (set marker, empty
// expected): tmux already carries a UUID but the manifest hasn't
// recorded it. The function adopts the existing marker rather than
// minting a new one.
func TestEnsureSession_AdoptsExistingMarker(t *testing.T) {
	eng, _ := testEngine(t)

	mockTmux := eng.Tmux.(*tmux.Mock)
	_ = mockTmux.NewSession("labs")
	_ = mockTmux.SetSessionOption("labs", sessionIDOption, "existing-uuid")

	got, err := eng.ensureSession("labs", "")
	if err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	if got != "existing-uuid" {
		t.Errorf("returned id = %q, want existing-uuid (adopted)", got)
	}
}

// TestEnsureSession_ClaimsUntaggedSession covers (empty marker, empty
// expected): the legacy state. The function tags the session so future
// probes have something to verify.
func TestEnsureSession_ClaimsUntaggedSession(t *testing.T) {
	eng, _ := testEngine(t)
	withDeterministicSessionID(t, "claimed-uuid")

	mockTmux := eng.Tmux.(*tmux.Mock)
	_ = mockTmux.NewSession("labs")

	got, err := eng.ensureSession("labs", "")
	if err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	if got != "claimed-uuid" {
		t.Errorf("returned id = %q, want claimed-uuid", got)
	}
	marker, _ := mockTmux.GetSessionOption("labs", sessionIDOption)
	if marker != "claimed-uuid" {
		t.Errorf("marker = %q, want claimed-uuid", marker)
	}
}

// TestEnsureSession_ReclaimsMismatchedMarker covers the post-restart
// case where bay recovers a session whose marker doesn't match what
// the manifest recorded. The function re-tags with a fresh UUID; the
// caller persists at the right moment.
func TestEnsureSession_ReclaimsMismatchedMarker(t *testing.T) {
	eng, _ := testEngine(t)
	withDeterministicSessionID(t, "reclaimed-uuid")

	mockTmux := eng.Tmux.(*tmux.Mock)
	_ = mockTmux.NewSession("labs")
	_ = mockTmux.SetSessionOption("labs", sessionIDOption, "stranded-uuid")

	got, err := eng.ensureSession("labs", "manifest-uuid")
	if err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	if got != "reclaimed-uuid" {
		t.Errorf("returned id = %q, want reclaimed-uuid", got)
	}
	marker, _ := mockTmux.GetSessionOption("labs", sessionIDOption)
	if marker != "reclaimed-uuid" {
		t.Errorf("marker = %q, want reclaimed-uuid", marker)
	}
}

// --- DockNew populates SessionID ---

// TestDockNew_PopulatesSessionID locks in that creating a dock writes
// SessionID to the manifest and tags the live session — a downstream
// SyncAll would then see matching marker/manifest and behave normally.
func TestDockNew_PopulatesSessionID(t *testing.T) {
	eng, _ := testEngine(t)
	withDeterministicSessionID(t, "new-dock-uuid")
	checkout := makeCheckout(t, t.TempDir())

	if err := eng.DockNew("new-dock", checkout, "", "", ""); err != nil {
		t.Fatalf("DockNew: %v", err)
	}

	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock("new-dock")
	if dock == nil {
		t.Fatalf("new-dock missing from manifest")
	}
	if dock.SessionID != "new-dock-uuid" {
		t.Errorf("dock.SessionID = %q, want new-dock-uuid", dock.SessionID)
	}
	mockTmux := eng.Tmux.(*tmux.Mock)
	marker, _ := mockTmux.GetSessionOption("new-dock", sessionIDOption)
	if marker != "new-dock-uuid" {
		t.Errorf("marker = %q, want new-dock-uuid", marker)
	}
}

// --- mid-recover race ---

// TestSyncAll_NoMidRecoverCleanup is the load-bearing test: it
// reproduces the race we engineered the design around. ensureSession
// has tagged tmux with the new UUID, but mergeRecoveredDockState
// hasn't fired yet — so the manifest still has the pre-crash SessionID
// AND the pre-crash pane IDs. A probe in this window must not
// interpret "marker matches manifest? → cleanup" because manifest's
// pane IDs are stale and would all look dead.
//
// We force the pre-merge state by setting SessionID on the manifest
// to the OLD value while tmux has the NEW value. The gate sees
// mismatched → owned=false → preserve.
func TestSyncAll_NoMidRecoverCleanup(t *testing.T) {
	eng, _ := testEngine(t)
	seedSurfaceBay(t, eng, "labs", "auth-fix")
	setSessionID(t, eng, "labs", "pre-crash-uuid")

	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// Tmux has been re-tagged by ensureSession but mergeRecoveredDockState
	// hasn't run yet — manifest still points at the old SessionID AND old
	// (now-dangling) pane IDs.
	_ = mockTmux.SetSessionOption("labs", sessionIDOption, "post-recover-uuid")

	eng.SyncAll()

	if got := surfaceCount(t, eng, "labs", "auth-fix"); got != 1 {
		t.Errorf("mid-recover race: surfaces stripped, got %d, want 1 (preserved)", got)
	}
}

// --- BayNew with foreign session ---

// TestBayNew_DoesNotPersistSessionIDWhenSessionExists locks in the
// fix for the post-restart `bay new` race: when the live session
// exists but bay didn't create it, BayNew must NOT write a new
// SessionID into the manifest, even if ensureSession-style logic
// would have minted one. Persisting here would let the next SyncAll
// see (matching marker → owned) and strip pre-existing bays'
// stale pane IDs.
func TestBayNew_DoesNotPersistSessionIDWhenSessionExists(t *testing.T) {
	eng, _ := testEngine(t)
	seedSurfaceBay(t, eng, "labs", "old-bay")
	setSessionID(t, eng, "labs", "pre-crash-uuid")

	// Simulate a same-name session that came back up empty after a
	// tmux restart — no marker.
	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	m, _ := eng.LoadManifest()
	dock := m.FindDock("labs")
	if dock.SessionID != "pre-crash-uuid" {
		t.Errorf("BayNew clobbered SessionID: got %q, want pre-crash-uuid", dock.SessionID)
	}
	// Original bay's surface must survive — the gate's
	// mismatched-marker preservation is what keeps recover viable.
	if got := surfaceCount(t, eng, "labs", "old-bay"); got != 1 {
		t.Errorf("pre-existing surface stripped after BayNew: got %d, want 1", got)
	}
}

func TestBayNew_DoesNotPersistSessionIDWhenCreatingSessionWithLegacySurfaces(t *testing.T) {
	eng, _ := testEngine(t)
	seedSurfaceBay(t, eng, "labs", "old-bay")
	// Manifest SessionID stays empty, and tmux has no session at all.
	withDeterministicSessionID(t, "created-session-uuid")

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	m, _ := eng.LoadManifest()
	dock := m.FindDock("labs")
	if dock.SessionID != "" {
		t.Errorf("BayNew persisted SessionID for legacy surfaces: got %q, want empty", dock.SessionID)
	}

	mockTmux := eng.Tmux.(*tmux.Mock)
	marker, _ := mockTmux.GetSessionOption("labs", sessionIDOption)
	if marker != "created-session-uuid" {
		t.Errorf("created session marker = %q, want created-session-uuid", marker)
	}

	eng.SyncAll()
	if got := surfaceCount(t, eng, "labs", "old-bay"); got != 1 {
		t.Errorf("pre-existing surface stripped after created-session BayNew: got %d, want 1", got)
	}
}

// --- legacy backfill ---

// TestSyncAll_BackfillsLegacySessionID covers fix #2: an upgraded
// dock with empty SessionID and an untagged tmux session must be
// proactively tagged on the next SyncAll, so a future tmux restart
// can't fall through the (empty, empty) → owned branch.
func TestSyncAll_BackfillsLegacySessionID(t *testing.T) {
	eng, _ := testEngine(t)
	// Pre-rollout state: manifest with no SessionID, untagged session.
	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	withDeterministicSessionID(t, "backfill-uuid")

	eng.SyncAll()

	m, _ := eng.LoadManifest()
	dock := m.FindDock("labs")
	if dock.SessionID != "backfill-uuid" {
		t.Errorf("legacy SessionID not backfilled: got %q, want backfill-uuid", dock.SessionID)
	}
	marker, _ := mockTmux.GetSessionOption("labs", sessionIDOption)
	if marker != "backfill-uuid" {
		t.Errorf("tmux marker not set during backfill: got %q, want backfill-uuid", marker)
	}
}

// TestSyncAll_DoesNotBackfillLegacyWhenSurfacesRecorded covers the
// ID-collision concern: tmux pane/window IDs are server-local and
// reset on restart, so a fresh same-name session may have low-numbered
// IDs that happen to match the manifest's records. Whether or not the
// recorded IDs are "alive" in the live server is therefore not proof
// of session continuity. When the dock has any recorded surfaces, the
// legacy backfill must refuse — the dock stays unowned, surfaces are
// preserved, and the user runs `bay recover` to reconcile.
func TestSyncAll_DoesNotBackfillLegacyWhenSurfacesRecorded(t *testing.T) {
	eng, _ := testEngine(t)
	// Seed a bay with surface tmux attrs that happen to be LIVE in
	// the mock — the worst-case "ID collision" simulation.
	seedLiveSurfaceBay(t, eng, "labs", "auth-fix")

	eng.SyncAll()

	m, _ := eng.LoadManifest()
	dock := m.FindDock("labs")
	if dock.SessionID != "" {
		t.Errorf("legacy SessionID backfilled despite recorded surfaces: got %q, want empty", dock.SessionID)
	}
	mockTmux := eng.Tmux.(*tmux.Mock)
	marker, _ := mockTmux.GetSessionOption("labs", sessionIDOption)
	if marker != "" {
		t.Errorf("tmux marker set despite recorded surfaces: got %q, want empty", marker)
	}
	if got := surfaceCount(t, eng, "labs", "auth-fix"); got != 1 {
		t.Errorf("surface stripped: got %d, want 1 (preserved)", got)
	}
}

// TestRecover_InvalidatesStaleTmuxIDsOnSessionTakeover covers the
// safety property that recover doesn't reconcile against foreign
// panes that happen to share IDs with manifest records. After a tmux
// server restart, any recorded pane/window ID may belong to a
// genuinely different window in the new server (allocation resets
// from @0/%0). Recover detects the takeover via applyRecoveredSessionID
// and clears the recorded IDs so the loop recreates rather than
// reconciles.
func TestRecover_InvalidatesStaleTmuxIDsOnSessionTakeover(t *testing.T) {
	eng, _ := testEngine(t)
	setSessionID(t, eng, "labs", "pre-crash-uuid")

	// Seed a bay whose recorded IDs were issued by a different
	// (now-dead) tmux server.
	bayPath := t.TempDir()
	err := eng.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock("labs")
		dock.Bays = append(dock.Bays, manifest.Bay{
			ID:   "w1",
			Name: "auth-fix",
			Type: manifest.BayTypeWorktree,
			Path: bayPath,
			Surfaces: []manifest.Surface{
				{
					ID:      1,
					Name:    "shell",
					Type:    manifest.SurfaceTypeShell,
					Backend: manifest.SurfaceBackendTmux,
					Tmux: &manifest.TmuxAttrs{
						WindowID:    "@5",
						PaneID:      "%9",
						LayoutGroup: 1,
					},
				},
			},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Simulate server takeover: a new same-name session with no marker.
	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if _, err := eng.DockRecover("labs"); err != nil {
		t.Fatalf("DockRecover: %v", err)
	}

	m, _ := eng.LoadManifest()
	ws := m.FindDock("labs").FindBayByID("w1")
	if ws.Surfaces[0].Tmux.WindowID == "@5" {
		t.Errorf("WindowID retained pre-takeover value @5 — recover reconciled against potential foreign window")
	}
	if ws.Surfaces[0].Tmux.PaneID == "%9" {
		t.Errorf("PaneID retained pre-takeover value %%9 — recover reconciled against potential foreign pane")
	}
}

// TestSyncAll_DoesNotBackfillLegacyWhenSurfacesStale exercises the
// other half of the same rule with stale (non-live) recorded IDs —
// also no backfill. Same policy regardless of liveness.
func TestSyncAll_DoesNotBackfillLegacyWhenSurfacesStale(t *testing.T) {
	eng, _ := testEngine(t)
	seedSurfaceBay(t, eng, "labs", "auth-fix")
	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	eng.SyncAll()

	m, _ := eng.LoadManifest()
	dock := m.FindDock("labs")
	if dock.SessionID != "" {
		t.Errorf("stale legacy SessionID backfilled: got %q, want empty", dock.SessionID)
	}
	marker, _ := mockTmux.GetSessionOption("labs", sessionIDOption)
	if marker != "" {
		t.Errorf("stale legacy tmux marker set: got %q, want empty", marker)
	}
	if got := surfaceCount(t, eng, "labs", "auth-fix"); got != 1 {
		t.Errorf("stale surface stripped during legacy backfill: got %d, want 1", got)
	}
}

// TestSyncAll_DoesNotBackfillWhenManifestSet verifies the backfill
// only fires for the (empty, empty) legacy state — a manifest with a
// SessionID but a foreign/untagged tmux session must continue to
// preserve surfaces (mismatched-marker branch), not get clobbered by
// a fresh backfill.
func TestSyncAll_DoesNotBackfillWhenManifestSet(t *testing.T) {
	eng, _ := testEngine(t)
	setSessionID(t, eng, "labs", "manifest-uuid")
	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// No marker on the live session.

	eng.SyncAll()

	m, _ := eng.LoadManifest()
	dock := m.FindDock("labs")
	if dock.SessionID != "manifest-uuid" {
		t.Errorf("non-legacy SessionID was changed: got %q, want manifest-uuid", dock.SessionID)
	}
	marker, _ := mockTmux.GetSessionOption("labs", sessionIDOption)
	if marker != "" {
		t.Errorf("tmux marker should remain empty on mismatched manifest: got %q", marker)
	}
}
