package engine

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/tmux"
)

func TestSurfaceClose_PushesUndoEntry(t *testing.T) {
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeShell, Name: "shell-2", SplitDir: "v",
	}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	before := time.Now().Unix()
	if err := eng.SurfaceClose("labs", "b1", "shell-2", false); err != nil {
		t.Fatalf("SurfaceClose: %v", err)
	}

	entries, err := eng.ListClosedEntries("labs")
	if err != nil {
		t.Fatalf("ListClosedEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 queued entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Kind != manifest.ClosedKindSurface {
		t.Errorf("wrong kind: %q", e.Kind)
	}
	if e.Surface == nil {
		t.Fatal("missing Surface payload")
	}
	if e.Surface.Bay != "b1" || e.Surface.Name != "shell-2" {
		t.Errorf("wrong payload: %+v", e.Surface)
	}
	if e.Surface.Type != manifest.SurfaceTypeShell {
		t.Errorf("wrong type: %q", e.Surface.Type)
	}
	if e.ClosedAt < before {
		t.Errorf("ClosedAt=%d predates test start %d", e.ClosedAt, before)
	}
}

func TestSurfaceRestore_RoundTripsSurface(t *testing.T) {
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeShell, Name: "logs", SplitDir: "v",
	}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}
	if err := eng.SurfaceClose("labs", "b1", "logs", false); err != nil {
		t.Fatalf("SurfaceClose: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	if s := bay.FindSurface("logs"); s != nil {
		t.Fatal("surface should be gone before restore")
	}

	entry, err := eng.SurfaceRestore("labs")
	if err != nil {
		t.Fatalf("SurfaceRestore: %v", err)
	}
	if entry == nil || entry.Surface == nil || entry.Surface.Name != "logs" {
		t.Fatalf("SurfaceRestore returned unexpected entry: %+v", entry)
	}

	bay, _ = eng.BayShow("labs", "b1")
	restored := bay.FindSurface("logs")
	if restored == nil {
		t.Fatal("restored surface not found in bay")
	}
	// A closed split pane must come back as a split pane, not a new
	// tmux window. Regression test for the first local-testing bug: a
	// tiled shell was restored as a full window.
	if restored.Tmux == nil || restored.Tmux.SplitDir != "v" {
		t.Errorf("expected restored surface to preserve SplitDir=v, got %+v", restored.Tmux)
	}

	// Queue should be empty after a successful restore.
	entries, err := eng.ListClosedEntries("labs")
	if err != nil {
		t.Fatalf("ListClosedEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty queue after restore, got %d entries", len(entries))
	}
}

func TestSurfaceRestore_EmptyQueueReturnsNothingToRestore(t *testing.T) {
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	entry, err := eng.SurfaceRestore("labs")
	if !errors.Is(err, ErrNothingToRestore) {
		t.Errorf("expected ErrNothingToRestore, got %v", err)
	}
	if entry != nil {
		t.Errorf("expected nil entry on empty queue, got %+v", entry)
	}
}

func TestSurfaceRestore_DropsStaleEntryWhenBayGone(t *testing.T) {
	// When the parent bay has been closed between close and restore
	// (e.g. grace window elapsed under orphan-hygiene), the entry is stale
	// and should be silently dropped.
	defer withZeroGrace()()
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	bay, _ := eng.BayShow("labs", "b1")
	first := bay.Surfaces[0].Name

	if err := eng.SurfaceClose("labs", "b1", first, false); err != nil {
		t.Fatalf("SurfaceClose: %v", err)
	}
	// Zero grace → next sync finalizes the bay close. Queue entry
	// remains, but bay is gone.
	eng.SyncAll()

	_, err := eng.SurfaceRestore("labs")
	if !errors.Is(err, ErrNothingToRestore) {
		t.Errorf("expected ErrNothingToRestore for stale entry, got %v", err)
	}

	// Stale entry should have been dropped from the queue.
	entries, _ := eng.ListClosedEntries("labs")
	if len(entries) != 0 {
		t.Errorf("expected stale entry dropped, got %d entries", len(entries))
	}
}

func TestSurfaceRestore_RestoreWithinGraceCancelsPendingClose(t *testing.T) {
	// The core composition with orphan-hygiene: Option+W on the last
	// surface schedules PendingCloseAt; Option+Z within the grace window
	// restores the surface, and SurfaceAdd's cancel path clears the timer.
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	bay, _ := eng.BayShow("labs", "b1")
	first := bay.Surfaces[0].Name

	if err := eng.SurfaceClose("labs", "b1", first, false); err != nil {
		t.Fatalf("SurfaceClose: %v", err)
	}
	bay, _ = eng.BayShow("labs", "b1")
	if bay.PendingCloseAt == 0 {
		t.Fatal("expected PendingCloseAt scheduled after last-surface close")
	}

	if _, err := eng.SurfaceRestore("labs"); err != nil {
		t.Fatalf("SurfaceRestore: %v", err)
	}

	bay, _ = eng.BayShow("labs", "b1")
	if bay.PendingCloseAt != 0 {
		t.Errorf("PendingCloseAt not cleared after restore: %d", bay.PendingCloseAt)
	}
	if len(bay.Surfaces) != 1 {
		t.Errorf("expected 1 surface after restore, got %d", len(bay.Surfaces))
	}
}

func TestSurfaceRestore_DiscoveredDeadAgentQueuesUndo(t *testing.T) {
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeAgent, Name: "codex-agent", Agent: "codex", SplitDir: "v",
	}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	agent := bay.FindSurface("codex-agent")
	if agent == nil || agent.Tmux == nil {
		t.Fatalf("agent surface missing tmux attrs: %+v", bay.Surfaces)
	}
	agentLayoutGroup := agent.Tmux.LayoutGroup
	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.KillPane(agent.Tmux.PaneID); err != nil {
		t.Fatalf("KillPane: %v", err)
	}

	eng.SyncAll()

	entries, err := eng.ListClosedEntries("labs")
	if err != nil {
		t.Fatalf("ListClosedEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 queued entry, got %d", len(entries))
	}
	got := entries[0].Surface
	if got == nil {
		t.Fatal("queued entry missing surface payload")
	}
	if got.Bay != "b1" || got.Name != "codex-agent" || got.Type != manifest.SurfaceTypeAgent || got.Agent != "codex" {
		t.Fatalf("wrong queued surface payload: %+v", got)
	}
	if got.SplitDir != "v" || got.LayoutGroup != agentLayoutGroup {
		t.Fatalf("queued layout attrs = SplitDir %q LayoutGroup %d, want v/%d", got.SplitDir, got.LayoutGroup, agentLayoutGroup)
	}

	bay, _ = eng.BayShow("labs", "b1")
	if bay.FindSurface("codex-agent") != nil {
		t.Fatal("dead agent surface should be stripped before restore")
	}

	if _, err := eng.SurfaceRestore("labs"); err != nil {
		t.Fatalf("SurfaceRestore: %v", err)
	}
	bay, _ = eng.BayShow("labs", "b1")
	restored := bay.FindSurface("codex-agent")
	if restored == nil {
		t.Fatal("agent surface was not restored")
	}
	if restored.Agent == nil || *restored.Agent != "codex" {
		t.Fatalf("restored agent = %v, want codex", restored.Agent)
	}
}

func TestMoveClosedEntriesBelowKnown_LeavesConcurrentCloseOnTop(t *testing.T) {
	eng, _ := testEngine(t)

	entry := func(closedAt int64, name string) manifest.ClosedEntry {
		return manifest.ClosedEntry{
			ClosedAt: closedAt,
			Kind:     manifest.ClosedKindSurface,
			Surface: &manifest.ClosedSurface{
				Bay:  "b1",
				Name: name,
				Type: manifest.SurfaceTypeShell,
			},
		}
	}
	now := time.Now().Unix()
	a := entry(now+1, "a")
	b := entry(now+2, "b")
	d := entry(now+4, "concurrent")
	c := entry(now+3, "discovered")

	if err := eng.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock("labs")
		dock.ClosedEntries = []manifest.ClosedEntry{a, b, d, c}
		return nil
	}); err != nil {
		t.Fatalf("seed queue: %v", err)
	}

	if err := eng.MoveClosedEntriesBelowKnown("labs", []manifest.ClosedEntry{c}, []manifest.ClosedEntry{b, a}); err != nil {
		t.Fatalf("MoveClosedEntriesBelowKnown: %v", err)
	}

	entries, err := eng.ListClosedEntries("labs")
	if err != nil {
		t.Fatalf("ListClosedEntries: %v", err)
	}
	gotNames := make([]string, 0, len(entries))
	for _, e := range entries {
		gotNames = append(gotNames, e.Surface.Name)
	}
	want := []string{"concurrent", "b", "a", "discovered"}
	if strings.Join(gotNames, ",") != strings.Join(want, ",") {
		t.Fatalf("restore order = %v, want %v", gotNames, want)
	}
}

func TestMoveClosedEntriesBelowKnown_FullQueueDropsDiscoveredBeforeKnown(t *testing.T) {
	eng, _ := testEngine(t)

	entry := func(closedAt int64, name string) manifest.ClosedEntry {
		return manifest.ClosedEntry{
			ClosedAt: closedAt,
			Kind:     manifest.ClosedKindSurface,
			Surface: &manifest.ClosedSurface{
				Bay:  "b1",
				Name: name,
				Type: manifest.SurfaceTypeShell,
			},
		}
	}
	now := time.Now().Unix()
	var knownStorage []manifest.ClosedEntry
	var knownNewestFirst []manifest.ClosedEntry
	for i := 0; i < manifest.ClosedQueueMax; i++ {
		e := entry(now+int64(i+1), fmt.Sprintf("known-%d", i+1))
		knownStorage = append(knownStorage, e)
		knownNewestFirst = append([]manifest.ClosedEntry{e}, knownNewestFirst...)
	}
	discovered := entry(now+100, "discovered")

	if err := eng.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock("labs")
		dock.ClosedEntries = append(append([]manifest.ClosedEntry{}, knownStorage...), discovered)
		return nil
	}); err != nil {
		t.Fatalf("seed queue: %v", err)
	}

	if err := eng.MoveClosedEntriesBelowKnown("labs", []manifest.ClosedEntry{discovered}, knownNewestFirst); err != nil {
		t.Fatalf("MoveClosedEntriesBelowKnown: %v", err)
	}

	entries, err := eng.ListClosedEntries("labs")
	if err != nil {
		t.Fatalf("ListClosedEntries: %v", err)
	}
	if len(entries) != manifest.ClosedQueueMax {
		t.Fatalf("queue len = %d, want %d", len(entries), manifest.ClosedQueueMax)
	}
	for _, e := range entries {
		if e.Surface.Name == "discovered" {
			t.Fatalf("discovered entry survived full known queue: %+v", entries)
		}
	}
	for i, e := range entries {
		want := fmt.Sprintf("known-%d", manifest.ClosedQueueMax-i)
		if e.Surface.Name != want {
			t.Fatalf("entry %d = %q, want %q; full order=%+v", i, e.Surface.Name, want, entries)
		}
	}
}

func TestSurfaceRestore_PreservesQueueOnAddFailure(t *testing.T) {
	// If SurfaceAdd fails during restore, the entry must stay queued so
	// the user can retry after fixing the cause. We simulate a transient
	// failure by injecting a queue entry for an unknown agent — the
	// bay exists, so the stale-drop path is not taken, but SurfaceAdd
	// fails in validateAgentName.
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	// Inject a bad entry directly — bypasses SurfaceClose and its own
	// validation, simulating the "config regressed between close and
	// restore" scenario that the design promises to be recoverable from.
	err := eng.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock("labs")
		dock.PushClosedEntry(manifest.ClosedEntry{
			ClosedAt: time.Now().Unix(),
			Kind:     manifest.ClosedKindSurface,
			Surface: &manifest.ClosedSurface{
				Bay:   "b1",
				Name:  "a1",
				Type:  manifest.SurfaceTypeAgent,
				Agent: "definitely-not-a-real-agent",
			},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}

	_, err = eng.SurfaceRestore("labs")
	if err == nil {
		t.Fatal("expected restore to fail on unknown agent")
	}
	if errors.Is(err, ErrNothingToRestore) {
		t.Errorf("unexpected ErrNothingToRestore on real failure: %v", err)
	}

	entries, _ := eng.ListClosedEntries("labs")
	if len(entries) != 1 {
		t.Errorf("expected entry preserved on restore failure, got %d entries", len(entries))
	}
}

// TestSurfaceRestore_RootPaneRejoinsWindow verifies the layout-faithful
// restore of a root pane: with siblings surviving in the same layout
// group, the restored pane re-enters the original tmux window via
// `split-window -fb` instead of creating a new window.
func TestSurfaceRestore_RootPaneRejoinsWindow(t *testing.T) {
	eng, _ := testEngine(t)
	mockTmux := eng.Tmux.(*tmux.Mock)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	// BayNew's initial surface is the root (SplitDir=""). Add two split
	// children so the layout group survives after the root is closed.
	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeShell, Name: "middle", SplitDir: "v",
	}); err != nil {
		t.Fatalf("SurfaceAdd middle: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeShell, Name: "bottom", SplitDir: "v",
	}); err != nil {
		t.Fatalf("SurfaceAdd bottom: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	rootName := bay.Surfaces[0].Name
	rootLayoutGroup := bay.Surfaces[0].Tmux.LayoutGroup

	if err := eng.SurfaceClose("labs", "b1", rootName, false); err != nil {
		t.Fatalf("SurfaceClose root: %v", err)
	}

	before := len(mockTmux.Calls)
	if _, err := eng.SurfaceRestore("labs"); err != nil {
		t.Fatalf("SurfaceRestore: %v", err)
	}

	// Assert SplitWindowBefore was called (not NewWindow) during restore.
	sawSplitBefore := false
	for _, c := range mockTmux.Calls[before:] {
		if c.Method == "SplitWindowBefore" {
			sawSplitBefore = true
			break
		}
		if c.Method == "NewWindow" {
			t.Errorf("restore created a new window; expected SplitWindowBefore. Call: %+v", c)
		}
	}
	if !sawSplitBefore {
		t.Error("expected SplitWindowBefore call during root-pane restore")
	}

	bay, _ = eng.BayShow("labs", "b1")
	restored := bay.FindSurface(rootName)
	if restored == nil {
		t.Fatal("root surface not found after restore")
	}
	if restored.Tmux == nil || restored.Tmux.LayoutGroup != rootLayoutGroup {
		t.Errorf("restored root LayoutGroup=%d; want %d", restored.Tmux.LayoutGroup, rootLayoutGroup)
	}
	if restored.Tmux.SplitDir != "" {
		t.Errorf("restored root SplitDir=%q; want \"\"", restored.Tmux.SplitDir)
	}
	if restored.Tmux.SplitFrom != 0 {
		t.Errorf("restored root SplitFrom=%d; want 0", restored.Tmux.SplitFrom)
	}
}

// TestSurfaceRestore_SplitChildRejoinsWindow verifies that a closed split
// pane comes back as a split in its original layout group (not a new
// window) when a sibling survives.
func TestSurfaceRestore_SplitChildRejoinsWindow(t *testing.T) {
	eng, _ := testEngine(t)
	mockTmux := eng.Tmux.(*tmux.Mock)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeShell, Name: "split", SplitDir: "v",
	}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	splitLayoutGroup := bay.FindSurface("split").Tmux.LayoutGroup

	if err := eng.SurfaceClose("labs", "b1", "split", false); err != nil {
		t.Fatalf("SurfaceClose: %v", err)
	}

	before := len(mockTmux.Calls)
	if _, err := eng.SurfaceRestore("labs"); err != nil {
		t.Fatalf("SurfaceRestore: %v", err)
	}

	// Regular SplitWindow (not SplitWindowBefore, not NewWindow) is the
	// correct call for a split-child restore.
	sawSplit := false
	for _, c := range mockTmux.Calls[before:] {
		if c.Method == "NewWindow" {
			t.Errorf("restore created a new window for a split child; expected SplitWindow. Call: %+v", c)
		}
		if c.Method == "SplitWindow" {
			sawSplit = true
		}
	}
	if !sawSplit {
		t.Error("expected SplitWindow call during split-child restore")
	}

	bay, _ = eng.BayShow("labs", "b1")
	restored := bay.FindSurface("split")
	if restored == nil || restored.Tmux == nil {
		t.Fatal("split surface not found after restore")
	}
	if restored.Tmux.LayoutGroup != splitLayoutGroup {
		t.Errorf("restored split LayoutGroup=%d; want %d", restored.Tmux.LayoutGroup, splitLayoutGroup)
	}
	if restored.Tmux.SplitDir != "v" {
		t.Errorf("restored split SplitDir=%q; want \"v\"", restored.Tmux.SplitDir)
	}
}

// TestSurfaceRestore_StackedRestoreRejoinsOriginalWindow is the end-to-end
// scenario that motivated layout-faithful restore: close three panes of
// a split-window, then restore three times. The LIFO queue means each
// restore finds its predecessor still alive, so all three land in the
// same tmux window as splits — no "new windows that never existed."
func TestSurfaceRestore_StackedRestoreRejoinsOriginalWindow(t *testing.T) {
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeShell, Name: "middle", SplitDir: "v",
	}); err != nil {
		t.Fatalf("SurfaceAdd middle: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeShell, Name: "bottom", SplitDir: "v",
	}); err != nil {
		t.Fatalf("SurfaceAdd bottom: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	rootName := bay.Surfaces[0].Name
	originalLayoutGroup := bay.Surfaces[0].Tmux.LayoutGroup

	// Close all three in order: bottom, middle, root.
	for _, name := range []string{"bottom", "middle", rootName} {
		if err := eng.SurfaceClose("labs", "b1", name, false); err != nil {
			t.Fatalf("SurfaceClose %s: %v", name, err)
		}
	}

	// Restore three times — the LIFO order is root, middle, bottom.
	for range 3 {
		if _, err := eng.SurfaceRestore("labs"); err != nil {
			t.Fatalf("SurfaceRestore: %v", err)
		}
	}

	bay, _ = eng.BayShow("labs", "b1")
	if len(bay.Surfaces) != 3 {
		t.Fatalf("expected 3 surfaces after stacked restore, got %d", len(bay.Surfaces))
	}
	// All three must share the originally recorded layout group.
	for _, s := range bay.Surfaces {
		if s.Tmux == nil {
			t.Errorf("surface %q has no Tmux attrs after restore", s.Name)
			continue
		}
		if s.Tmux.LayoutGroup != originalLayoutGroup {
			t.Errorf("surface %q LayoutGroup=%d; want %d (all in original window)",
				s.Name, s.Tmux.LayoutGroup, originalLayoutGroup)
		}
	}
}

// TestSurfaceRestore_FallsBackToNewWindowWhenLayoutGroupGone covers the
// case where every surface in the original layout group has been closed.
// With no sibling to split against, restore creates a new tmux window.
func TestSurfaceRestore_FallsBackToNewWindowWhenLayoutGroupGone(t *testing.T) {
	eng, _ := testEngine(t)
	mockTmux := eng.Tmux.(*tmux.Mock)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	// Add a surface in a new tmux window so the bay has a second
	// layout group; this gives SurfaceRestore somewhere to land while
	// confirming it DOESN'T mistakenly rejoin the wrong group.
	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeShell, Name: "other-window",
	}); err != nil {
		t.Fatalf("SurfaceAdd other-window: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	rootName := bay.Surfaces[0].Name

	// Close the root (layout group 1). Its layout group becomes empty
	// because it had no siblings.
	if err := eng.SurfaceClose("labs", "b1", rootName, false); err != nil {
		t.Fatalf("SurfaceClose: %v", err)
	}

	before := len(mockTmux.Calls)
	if _, err := eng.SurfaceRestore("labs"); err != nil {
		t.Fatalf("SurfaceRestore: %v", err)
	}

	sawNewWindow := false
	for _, c := range mockTmux.Calls[before:] {
		if c.Method == "NewWindow" {
			sawNewWindow = true
		}
		if c.Method == "SplitWindowBefore" || c.Method == "SplitWindow" {
			t.Errorf("restore split into an existing window; layout group was gone. Call: %+v", c)
		}
	}
	if !sawNewWindow {
		t.Error("expected NewWindow call when the layout group is gone")
	}
}

// TestSurfaceRestore_AgentResumesPriorSession verifies that an agent
// surface restored via undo-close is launched with the agent's
// resume_args (e.g. `claude --continue`) so the user lands back in
// their previous conversation rather than a fresh session.
func TestSurfaceRestore_AgentResumesPriorSession(t *testing.T) {
	cases := []struct {
		agent string
		want  string // command substring expected in RespawnPane
	}{
		{"claude", "claude --continue"},
		{"codex", "codex resume --last"},
		{"antigravity", "agy --continue"},
		{"gemini", "agy --continue"},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			eng, _ := testEngine(t)
			mockTmux := eng.Tmux.(*tmux.Mock)

			if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Agent: tc.agent}); err != nil {
				t.Fatalf("BayNew: %v", err)
			}
			// Add a second agent surface in a split so closing it leaves
			// a sibling — keeps the layout group alive and the restore
			// path simple.
			if err := eng.SurfaceAdd(SurfaceAddOptions{
				DockName: "labs", BayName: "b1",
				Type: manifest.SurfaceTypeAgent, Name: "side",
				Agent: tc.agent, SplitDir: "v",
			}); err != nil {
				t.Fatalf("SurfaceAdd: %v", err)
			}

			if err := eng.SurfaceClose("labs", "b1", "side", false); err != nil {
				t.Fatalf("SurfaceClose: %v", err)
			}

			before := len(mockTmux.Calls)
			if _, err := eng.SurfaceRestore("labs"); err != nil {
				t.Fatalf("SurfaceRestore: %v", err)
			}

			found := false
			for _, c := range mockTmux.Calls[before:] {
				if c.Method != "RespawnPane" || len(c.Args) < 3 {
					continue
				}
				if strings.Contains(c.Args[2], tc.want) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected RespawnPane with %q during restore; calls=%+v",
					tc.want, mockTmux.Calls[before:])
			}
		})
	}
}

// TestSurfaceRestore_LaunchArgsNotReplayed verifies the launch_args
// contract end to end: a model profile's launch args appear on fresh
// launch but are dropped on undo-close restore. Claude Code restores a
// resumed session's own model, so replaying --model would clobber any
// in-session model switch.
func TestSurfaceRestore_LaunchArgsNotReplayed(t *testing.T) {
	eng, _ := testEngine(t)
	mockTmux := eng.Tmux.(*tmux.Mock)
	eng.Config.Agents["fable"] = config.AgentConfig{
		Extends:    "claude",
		LaunchArgs: []string{"--model", "fable"},
	}

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Agent: "fable"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	launched := false
	for _, c := range mockTmux.Calls {
		if c.Method == "RespawnPane" && len(c.Args) >= 3 && strings.Contains(c.Args[2], "claude --model fable") {
			launched = true
		}
	}
	if !launched {
		t.Errorf("fresh launch should include launch args; calls=%+v", mockTmux.Calls)
	}

	// Split in a sibling so the restore path stays simple.
	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeAgent, Name: "side",
		Agent: "fable", SplitDir: "v",
	}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}
	if err := eng.SurfaceClose("labs", "b1", "side", false); err != nil {
		t.Fatalf("SurfaceClose: %v", err)
	}

	before := len(mockTmux.Calls)
	if _, err := eng.SurfaceRestore("labs"); err != nil {
		t.Fatalf("SurfaceRestore: %v", err)
	}
	resumed := false
	for _, c := range mockTmux.Calls[before:] {
		if c.Method != "RespawnPane" || len(c.Args) < 3 {
			continue
		}
		if strings.Contains(c.Args[2], "--model") {
			t.Errorf("restore replayed launch args: %q", c.Args[2])
		}
		if strings.Contains(c.Args[2], "claude --continue") {
			resumed = true
		}
	}
	if !resumed {
		t.Errorf("expected restore to respawn with claude --continue; calls=%+v", mockTmux.Calls[before:])
	}
}
