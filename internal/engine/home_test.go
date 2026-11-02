package engine

import (
	"os"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/git"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/nav"
	"github.com/commonfabric/bay/internal/tmux"
)

func TestBayNew_RejectsHomeNameWithGuidance(t *testing.T) {
	eng, _ := testEngine(t)

	_, err := eng.BayNew(BayNewOptions{Dock: "labs", Name: manifest.HomeBayID, Shell: true})
	if err == nil {
		t.Fatal("BayNew(home) succeeded, want reserved-name error")
	}
	const want = "home is reserved for the dock checkout; use `bay home`"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}

	mockGit := eng.Git.(*git.Mock)
	if got := len(mockGit.CreatedWorktrees()); got != 0 {
		t.Fatalf("created worktrees = %d, want 0", got)
	}
}

func TestBayNew_RejectsExternalHomeName(t *testing.T) {
	eng, dir := testEngine(t)

	_, err := eng.BayNew(BayNewOptions{Dock: "labs", Dir: dir, Name: manifest.HomeBayID, Shell: true})
	if err == nil || err.Error() != "home is reserved for the dock checkout; use `bay home`" {
		t.Fatalf("BayNew external home error = %v", err)
	}
}

func TestBayNew_BranchDerivedHomeNameIsMadeSafe(t *testing.T) {
	eng, _ := testEngine(t)

	bay, err := eng.BayNew(BayNewOptions{Dock: "labs", Branch: "feature/home", Shell: true})
	if err != nil {
		t.Fatalf("BayNew(feature/home): %v", err)
	}
	if bay.Name != branchAbbrevReservedPrefix+manifest.HomeBayID {
		t.Fatalf("bay.Name = %q, want %q", bay.Name, branchAbbrevReservedPrefix+manifest.HomeBayID)
	}
}

func TestBayCompactLabel_HomeUsesReservedLabel(t *testing.T) {
	label := BayCompactLabel(&manifest.Bay{
		ID:   manifest.HomeBayID,
		Name: manifest.HomeBayID,
		Type: manifest.BayTypeHome,
		Path: "/repo/labs",
	})
	if label != manifest.HomeBayID {
		t.Fatalf("BayCompactLabel(home) = %q, want %q", label, manifest.HomeBayID)
	}
}

func TestBayRename_RejectsHomeBay(t *testing.T) {
	eng, _ := testEngine(t)

	err := eng.BayRename("labs", manifest.HomeBayID, "anything")
	if err == nil || !strings.Contains(err.Error(), "rename is not supported") {
		t.Fatalf("BayRename(home) error = %v", err)
	}
}

func TestBayRename_RejectsRenamingToHome(t *testing.T) {
	eng, _ := testEngine(t)
	bay, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	err = eng.BayRename("labs", bay.ID, manifest.HomeBayID)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("BayRename(to home) error = %v", err)
	}
}

func TestBayDescribe_RejectsHomeBay(t *testing.T) {
	eng, _ := testEngine(t)

	err := eng.BayDescribe("labs", manifest.HomeBayID, "anything")
	if err == nil || !strings.Contains(err.Error(), "describe is not supported") {
		t.Fatalf("BayDescribe(home) error = %v", err)
	}
}

func TestBayUpdate_RejectsHomeBay(t *testing.T) {
	eng, _ := testEngine(t)
	branch := "main"

	err := eng.BayUpdate("labs", manifest.HomeBayID, &branch, nil)
	if err == nil || !strings.Contains(err.Error(), "branch/PR metadata") {
		t.Fatalf("BayUpdate(home) error = %v", err)
	}
}

func TestBayCleanReview_RejectsHomeBay(t *testing.T) {
	eng, _ := testEngine(t)

	_, err := eng.BayCleanReview("labs", manifest.HomeBayID)
	if err == nil || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("BayCleanReview(home) error = %v", err)
	}
}

func TestBayClose_HomeIsNoOp(t *testing.T) {
	eng, _ := testEngine(t)

	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock("labs")
	if dock == nil {
		t.Fatal("missing labs dock")
	}
	dockPath := dock.Path

	if err := eng.BayClose("labs", manifest.HomeBayID, false); err != nil {
		t.Fatalf("BayClose(home): %v", err)
	}

	m2, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest after close: %v", err)
	}
	dock2 := m2.FindDock("labs")
	if dock2 == nil {
		t.Fatal("dock disappeared after BayClose(home)")
	}
	if dock2.Path != dockPath {
		t.Fatalf("dock.Path = %q, want %q", dock2.Path, dockPath)
	}
	mockGit := eng.Git.(*git.Mock)
	if got := len(mockGit.RemovedWorktrees()); got != 0 {
		t.Fatalf("removed worktrees = %d, want 0", got)
	}
}

func TestBayClose_LastNonHomeCreatesHomeShell(t *testing.T) {
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.BayClose("labs", "b1", true); err != nil {
		t.Fatalf("BayClose(b1): %v", err)
	}

	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock("labs")
	if dock == nil {
		t.Fatal("missing labs dock")
	}
	home := dock.FindBayByID(manifest.HomeBayID)
	if home == nil {
		t.Fatal("closing the last non-home bay did not create home")
	}
	if len(home.Surfaces) != 1 || home.Surfaces[0].Type != manifest.SurfaceTypeShell {
		t.Fatalf("home surfaces = %+v, want one shell", home.Surfaces)
	}

	mockTmux := eng.Tmux.(*tmux.Mock)
	windows, _ := mockTmux.ListWindows("labs")
	for _, w := range windows {
		val, _ := mockTmux.GetWindowOption(w.ID, "@bay-placeholder")
		if val == "1" {
			t.Fatalf("placeholder %s should be replaced by home", w.ID)
		}
	}
}

func TestBayCloseBatch_LastNonHomeCreatesHomeShell(t *testing.T) {
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	closed, skipped, err := eng.BayCloseClean("labs", true, false)
	if err != nil {
		t.Fatalf("BayCloseClean: %v", err)
	}
	if len(closed) != 1 || closed[0] != "labs:b1" || len(skipped) != 0 {
		t.Fatalf("closed=%v skipped=%v, want labs:b1 only", closed, skipped)
	}

	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock("labs")
	if dock == nil {
		t.Fatal("missing labs dock")
	}
	home := dock.FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 1 || home.Surfaces[0].Type != manifest.SurfaceTypeShell {
		t.Fatalf("home = %+v, want one shell surface", home)
	}

	mockTmux := eng.Tmux.(*tmux.Mock)
	windows, _ := mockTmux.ListWindows("labs")
	for _, w := range windows {
		val, _ := mockTmux.GetWindowOption(w.ID, "@bay-placeholder")
		if val == "1" {
			t.Fatalf("placeholder %s should be replaced by home", w.ID)
		}
	}
}

func TestBayCloseCleanAndDoneSkipExistingHome(t *testing.T) {
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{DockName: "labs", BayName: manifest.HomeBayID, Type: manifest.SurfaceTypeShell, Name: "shell"}); err != nil {
		t.Fatalf("SurfaceAdd(home): %v", err)
	}

	closed, skipped, err := eng.BayCloseClean("labs", true, false)
	if err != nil {
		t.Fatalf("BayCloseClean: %v", err)
	}
	if len(closed) != 1 || closed[0] != "labs:b1" || len(skipped) != 0 {
		t.Fatalf("BayCloseClean closed=%v skipped=%v, want labs:b1 only", closed, skipped)
	}
	if home := mustEngineHome(t, eng); home == nil || len(home.Surfaces) != 1 {
		t.Fatalf("home after BayCloseClean = %+v, want one preserved surface", home)
	}

	closed, skipped, err = eng.BayCloseDone("labs", true, false)
	if err == nil || !strings.Contains(err.Error(), "no bays found") {
		t.Fatalf("BayCloseDone after worktree close error = %v, want no bays found", err)
	}
	if len(closed) != 0 || len(skipped) != 0 {
		t.Fatalf("BayCloseDone closed=%v skipped=%v, want none", closed, skipped)
	}
	if home := mustEngineHome(t, eng); home == nil || len(home.Surfaces) != 1 {
		t.Fatalf("home after BayCloseDone = %+v, want one preserved surface", home)
	}
}

func TestSyncAll_StripsDeadHomeSurfaceWithLiveNonHome(t *testing.T) {
	eng, _ := testEngine(t)

	bay, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := os.MkdirAll(bay.Path, 0o755); err != nil {
		t.Fatalf("MkdirAll bay path: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{DockName: "labs", BayName: manifest.HomeBayID, Type: manifest.SurfaceTypeShell, Name: "shell"}); err != nil {
		t.Fatalf("SurfaceAdd(home): %v", err)
	}

	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 1 || home.Surfaces[0].Tmux == nil {
		t.Fatalf("home = %+v, want one tmux surface", home)
	}

	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.KillPane(home.Surfaces[0].Tmux.PaneID); err != nil {
		t.Fatalf("KillPane(home): %v", err)
	}

	eng.SyncAll()

	m2, _ := eng.LoadManifest()
	dock := m2.FindDock("labs")
	if dock == nil {
		t.Fatal("dock disappeared")
	}
	if dock.Path != dockPath {
		t.Fatalf("dock.Path = %q, want %q", dock.Path, dockPath)
	}
	if home := dock.FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("empty home should be removed after sync strip, got %+v", home)
	}
	if got := dock.FindBayByID(bay.ID); got == nil || got.PendingCloseAt != 0 {
		t.Fatalf("non-home bay after home strip = %+v, want live and not pending", got)
	}
	mockGit := eng.Git.(*git.Mock)
	for _, call := range mockGit.RemovedWorktrees() {
		if len(call.Args) >= 2 && call.Args[1] == dockPath {
			t.Fatalf("sync pruned dock.path as a worktree: %+v", call)
		}
	}
}

func TestSyncAll_PreservesRecordedHomeWhenSessionDead(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 1 {
		t.Fatalf("home = %+v, want one recorded surface", home)
	}

	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.KillSession("labs"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	mockTmux.Calls = nil

	eng.SyncAll()

	m2, _ := eng.LoadManifest()
	dock := m2.FindDock("labs")
	if dock == nil || dock.Path != dockPath {
		t.Fatalf("dock after sync = %+v, want registered dock at %q", dock, dockPath)
	}
	home = dock.FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 1 {
		t.Fatalf("home after dead-session sync = %+v, want preserved for recovery", home)
	}
	for _, call := range mockTmux.Calls {
		if call.Method == "NewWindow" || call.Method == "SetWindowOption" {
			t.Fatalf("sync recreated tmux UI after native session death; calls: %+v", mockTmux.Calls)
		}
	}
}

func TestHome_AfterNativeSessionDeathDropsStaleHomeRecords(t *testing.T) {
	eng, _ := testEngine(t)
	withDeterministicSessionID(t, "first-home-session", "second-home-session")

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path
	stalePane := m.FindDock("labs").FindBayByID(manifest.HomeBayID).Surfaces[0].Tmux.PaneID

	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.KillSession("labs"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	mockTmux.Calls = nil

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home after session death: %v", err)
	}

	m2, _ := eng.LoadManifest()
	dock := m2.FindDock("labs")
	home := dock.FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 1 {
		t.Fatalf("home after explicit recreate = %+v, want one fresh shell", home)
	}
	if home.Surfaces[0].Tmux == nil || home.Surfaces[0].Tmux.PaneID == stalePane {
		t.Fatalf("home kept stale pane %q in surfaces: %+v", stalePane, home.Surfaces)
	}
	if dock.SessionID != "second-home-session" {
		t.Fatalf("dock.SessionID = %q, want second-home-session", dock.SessionID)
	}
	sawDockPathWindow := false
	for _, call := range mockTmux.Calls {
		if call.Method == "NewWindow" && len(call.Args) >= 3 && call.Args[2] == dockPath {
			sawDockPathWindow = true
		}
	}
	if !sawDockPathWindow {
		t.Fatalf("bay home did not recreate a shell at dock path %q; calls: %+v", dockPath, mockTmux.Calls)
	}
}

func TestRecover_RestoresRecordedHomeSurfacesAtDockPath(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.SurfaceAdd(SurfaceAddOptions{DockName: "labs", BayName: manifest.HomeBayID, Type: manifest.SurfaceTypeShell, Name: "shell"}); err != nil {
		t.Fatalf("SurfaceAdd home shell: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{DockName: "labs", BayName: manifest.HomeBayID, Type: manifest.SurfaceTypeCmd, Name: "tests", Command: "go test ./...", SplitDir: "h"}); err != nil {
		t.Fatalf("SurfaceAdd home cmd: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{DockName: "labs", BayName: manifest.HomeBayID, Type: manifest.SurfaceTypeAgent, Name: "codex-agent", Agent: "codex"}); err != nil {
		t.Fatalf("SurfaceAdd home agent: %v", err)
	}

	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path
	mockTmux := eng.Tmux.(*tmux.Mock)
	mockTmux.Reset()

	if _, err := eng.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	home := mustEngineHome(t, eng)
	if len(home.Surfaces) != 3 {
		t.Fatalf("recovered home surfaces = %+v, want 3", home.Surfaces)
	}
	if s := home.FindSurface("shell"); s == nil || s.Type != manifest.SurfaceTypeShell {
		t.Fatalf("shell surface after recover = %+v", s)
	}
	if s := home.FindSurface("tests"); s == nil || s.Type != manifest.SurfaceTypeCmd || s.Command == nil || *s.Command != "go test ./..." {
		t.Fatalf("cmd surface after recover = %+v", s)
	}
	if s := home.FindSurface("codex-agent"); s == nil || s.Type != manifest.SurfaceTypeAgent || s.Agent == nil || *s.Agent != "codex" {
		t.Fatalf("agent surface after recover = %+v", s)
	}

	sawNewWindowAtDockPath := false
	sawSplitAtDockPath := false
	sawAgentLaunch := false
	sawCmdLaunch := false
	for _, call := range mockTmux.Calls {
		if call.Method == "NewWindow" && len(call.Args) >= 3 && call.Args[2] == dockPath {
			sawNewWindowAtDockPath = true
		}
		if call.Method == "SplitWindow" && len(call.Args) >= 3 && call.Args[2] == dockPath {
			sawSplitAtDockPath = true
		}
		if call.Method == "SendKeys" && len(call.Args) >= 2 && strings.HasPrefix(call.Args[1], "codex") {
			sawAgentLaunch = true
		}
		if call.Method == "SendKeys" && len(call.Args) >= 2 && call.Args[1] == "go test ./..." {
			sawCmdLaunch = true
		}
	}
	if !sawNewWindowAtDockPath || !sawSplitAtDockPath {
		t.Fatalf("home recovery did not launch at dock path %q; calls: %+v", dockPath, mockTmux.Calls)
	}
	if !sawAgentLaunch || !sawCmdLaunch {
		t.Fatalf("home recovery did not relaunch recorded agent/cmd surfaces; calls: %+v", mockTmux.Calls)
	}
}

func TestRecover_DoesNotMaterializeEmptyHome(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock("labs")
		home := manifest.SynthesizeHomeBay(dock)
		return dock.AddBay(home)
	}); err != nil {
		t.Fatalf("seed empty home: %v", err)
	}
	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path

	mockTmux := eng.Tmux.(*tmux.Mock)
	mockTmux.Reset()
	if _, err := eng.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	for _, call := range mockTmux.Calls {
		if call.Method == "NewWindow" && len(call.Args) >= 3 && call.Args[1] == manifest.HomeBayID && call.Args[2] == dockPath {
			t.Fatalf("recovery materialized empty home; calls: %+v", mockTmux.Calls)
		}
	}
	m2, _ := eng.LoadManifest()
	if home := m2.FindDock("labs").FindBayByID(manifest.HomeBayID); home != nil && len(home.Surfaces) != 0 {
		t.Fatalf("empty home recovered surfaces unexpectedly: %+v", home)
	}
}

func TestRecover_RejectsMalformedHomeWithoutGitWorktreeChecks(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock("labs")
		dock.Bays = append(dock.Bays, manifest.Bay{
			ID:       manifest.HomeBayID,
			Name:     manifest.HomeBayID,
			Type:     manifest.BayTypeHome,
			Path:     dock.Path,
			Worktree: &manifest.WorktreeAttrs{Branch: "feature/not-home"},
			Surfaces: []manifest.Surface{
				{
					ID:      1,
					Name:    "shell",
					Type:    manifest.SurfaceTypeShell,
					Backend: manifest.SurfaceBackendTmux,
					Tmux:    &manifest.TmuxAttrs{WindowID: "@99", PaneID: "%99", LayoutGroup: 1},
				},
			},
		})
		return nil
	}); err != nil {
		t.Fatalf("seed malformed home: %v", err)
	}

	_, err := eng.Recover()
	if err == nil || !strings.Contains(err.Error(), "must not have worktree metadata") {
		t.Fatalf("Recover malformed home error = %v, want clear home invariant error", err)
	}
	mockGit := eng.Git.(*git.Mock)
	if calls := mockGit.Calls("CurrentBranch"); len(calls) != 0 {
		t.Fatalf("recovery treated home as a git worktree; CurrentBranch calls: %+v", calls)
	}
	if calls := mockGit.Calls("IsDirty"); len(calls) != 0 {
		t.Fatalf("recovery checked home dirty state; IsDirty calls: %+v", calls)
	}
}

func TestDisplayQueries_RejectMalformedPersistedHome(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.withManifest(func(m *manifest.Manifest) error {
		dock := m.FindDock("labs")
		dock.Bays = append(dock.Bays, manifest.Bay{
			ID:       manifest.HomeBayID,
			Name:     manifest.HomeBayID,
			Type:     manifest.BayTypeHome,
			Path:     dock.Path,
			Worktree: &manifest.WorktreeAttrs{Branch: "feature/not-home"},
			Surfaces: []manifest.Surface{
				{
					ID:      1,
					Name:    "shell",
					Type:    manifest.SurfaceTypeShell,
					Backend: manifest.SurfaceBackendTmux,
				},
			},
		})
		return nil
	}); err != nil {
		t.Fatalf("seed malformed home: %v", err)
	}

	if _, err := eng.List(); err == nil || !strings.Contains(err.Error(), "malformed home bay") {
		t.Fatalf("List malformed home error = %v, want clear home invariant error", err)
	}
	if _, err := eng.BayInfoByName("labs", manifest.HomeBayID); err == nil || !strings.Contains(err.Error(), "malformed home bay") {
		t.Fatalf("BayInfoByName malformed home error = %v, want clear home invariant error", err)
	}
	if _, err := eng.BayShow("labs", manifest.HomeBayID); err == nil || !strings.Contains(err.Error(), "malformed home bay") {
		t.Fatalf("BayShow malformed home error = %v, want clear home invariant error", err)
	}
	mockGit := eng.Git.(*git.Mock)
	if calls := mockGit.Calls("IsDirty"); len(calls) != 0 {
		t.Fatalf("display queries checked home dirty state before validation; IsDirty calls: %+v", calls)
	}
}

func TestApplyBaySyncUpdate_RemovesMalformedReservedHomeInsteadOfPendingClose(t *testing.T) {
	eng, _ := testEngine(t)

	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock("labs")
	if dock == nil {
		t.Fatal("missing labs dock")
	}
	dock.Bays = append(dock.Bays, manifest.Bay{
		ID:   manifest.HomeBayID,
		Name: manifest.HomeBayID,
		Type: manifest.BayTypeExternal,
		Path: dock.Path,
		Surfaces: []manifest.Surface{
			{
				ID:      1,
				Name:    "shell",
				Type:    manifest.SurfaceTypeShell,
				Backend: manifest.SurfaceBackendTmux,
				Tmux:    &manifest.TmuxAttrs{WindowID: "@99", PaneID: "%99", LayoutGroup: 1},
			},
		},
	})

	changed, _ := eng.applyBaySyncUpdate(m, baySyncUpdate{
		dockName:       "labs",
		originalID:     manifest.HomeBayID,
		deadSurfaceIDs: map[int]bool{1: true},
	}, true)
	if !changed {
		t.Fatal("applyBaySyncUpdate changed=false, want malformed reserved home removed")
	}
	if home := dock.FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("malformed empty home after dead-surface strip = %+v, want removed", home)
	}
}

func TestBayCloseAll_ClosesHomeAfterNonHomeAndPersistsBeforeKill(t *testing.T) {
	eng, _ := testEngine(t)

	bay, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := os.MkdirAll(bay.Path, 0o755); err != nil {
		t.Fatalf("MkdirAll bay path: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{DockName: "labs", BayName: manifest.HomeBayID, Type: manifest.SurfaceTypeShell, Name: "shell"}); err != nil {
		t.Fatalf("SurfaceAdd(home): %v", err)
	}

	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path
	mockTmux := eng.Tmux.(*tmux.Mock)
	mockTmux.Calls = nil
	checkedBeforeKill := false
	mockTmux.OnKill = func(method, target string) {
		checkedBeforeKill = true
		mid, err := eng.LoadManifest()
		if err != nil {
			t.Fatalf("LoadManifest during %s: %v", method, err)
		}
		dock := mid.FindDock("labs")
		if dock == nil {
			t.Fatal("dock disappeared before destructive tmux call")
		}
		if dock.Path != dockPath {
			t.Fatalf("dock.Path during %s = %q, want %q", method, dock.Path, dockPath)
		}
		if got := dock.FindBayByID(bay.ID); got != nil {
			t.Fatalf("non-home bay still persisted during %s: %+v", method, got)
		}
		if home := dock.FindBayByID(manifest.HomeBayID); home != nil {
			t.Fatalf("home still persisted during %s: %+v", method, home)
		}
	}

	closed, skipped, err := eng.BayCloseAll("labs", true, true, false)
	if err != nil {
		t.Fatalf("BayCloseAll: %v", err)
	}
	if len(closed) != 2 || closed[0] != "labs:"+bay.ID || closed[1] != "labs:"+manifest.HomeBayID || len(skipped) != 0 {
		t.Fatalf("closed=%v skipped=%v, want non-home then home", closed, skipped)
	}
	if !checkedBeforeKill {
		t.Fatal("no destructive tmux call observed")
	}

	m2, _ := eng.LoadManifest()
	dock := m2.FindDock("labs")
	if dock == nil || dock.Path != dockPath {
		t.Fatalf("dock after close all = %+v, want registered dock at %q", dock, dockPath)
	}
	if dock.FindBayByID(bay.ID) != nil || dock.FindBayByID(manifest.HomeBayID) != nil {
		t.Fatalf("bays after close all = %+v, want none", dock.Bays)
	}
	if dock.SessionID != "" {
		t.Fatalf("dock.SessionID = %q, want cleared after final home dismissal", dock.SessionID)
	}
	mockGit := eng.Git.(*git.Mock)
	for _, call := range mockGit.RemovedWorktrees() {
		if len(call.Args) >= 2 && call.Args[1] == dockPath {
			t.Fatalf("BayCloseAll removed dock.path as a worktree: %+v", call)
		}
	}
	for _, call := range mockTmux.Calls {
		if call.Method == "NewWindow" {
			t.Fatalf("BayCloseAll created a replacement home/placeholder window; calls: %+v", mockTmux.Calls)
		}
	}
}

func TestResolveBay_HomeWithDockPrefix(t *testing.T) {
	eng, _ := testEngine(t)

	dockName, bayID, err := eng.ResolveBay("labs:home")
	if err != nil {
		t.Fatalf("ResolveBay(labs:home): %v", err)
	}
	if dockName != "labs" || bayID != manifest.HomeBayID {
		t.Fatalf("resolved (%q, %q), want (labs, home)", dockName, bayID)
	}
}

func TestSurfaceAdd_HomeMaterializesBay(t *testing.T) {
	eng, _ := testEngine(t)

	err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs",
		BayName:  manifest.HomeBayID,
		Type:     manifest.SurfaceTypeShell,
		Name:     "shell",
	})
	if err != nil {
		t.Fatalf("SurfaceAdd(home): %v", err)
	}

	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock("labs")
	if dock == nil {
		t.Fatal("missing labs dock")
	}
	home := dock.FindBayByID(manifest.HomeBayID)
	if home == nil {
		t.Fatal("home bay was not materialized")
	}
	if home.Name != manifest.HomeBayID || home.Type != manifest.BayTypeHome || home.Path != dock.Path || home.Worktree != nil {
		t.Fatalf("home shape = %+v, dock.Path=%q", home, dock.Path)
	}
	if len(home.Surfaces) != 1 || home.Surfaces[0].Type != manifest.SurfaceTypeShell {
		t.Fatalf("home surfaces = %+v, want one shell", home.Surfaces)
	}

	mockTmux := eng.Tmux.(*tmux.Mock)
	sawMoveZero := false
	sawNewWindowCWD := false
	for _, call := range mockTmux.Calls {
		if call.Method == "MoveWindow" && len(call.Args) == 2 && call.Args[1] == "0" {
			sawMoveZero = true
		}
		if call.Method == "NewWindow" && len(call.Args) >= 3 && call.Args[2] == dock.Path {
			sawNewWindowCWD = true
		}
	}
	if !sawMoveZero {
		t.Fatalf("first home window should move to index 0; calls: %+v", mockTmux.Calls)
	}
	if !sawNewWindowCWD {
		t.Fatalf("home shell should start at dock path %q; calls: %+v", dock.Path, mockTmux.Calls)
	}
}

func TestHome_FocusesExistingSurface(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home create: %v", err)
	}
	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 1 || home.Surfaces[0].Tmux == nil {
		t.Fatalf("home = %+v, want one tmux surface", home)
	}
	winID := home.Surfaces[0].Tmux.WindowID

	mockTmux := eng.Tmux.(*tmux.Mock)
	mockTmux.Calls = nil
	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home focus: %v", err)
	}
	for _, call := range mockTmux.Calls {
		if call.Method == "NewWindow" || call.Method == "SplitWindow" {
			t.Fatalf("Home focus should not create another surface; calls: %+v", mockTmux.Calls)
		}
	}
	sawSelect := false
	for _, call := range mockTmux.Calls {
		if call.Method == "SelectWindow" && len(call.Args) == 1 && call.Args[0] == winID {
			sawSelect = true
		}
	}
	if !sawSelect {
		t.Fatalf("Home focus did not select %s; calls: %+v", winID, mockTmux.Calls)
	}
}

func TestBayClose_HomeClosesMaterializedSurfaces(t *testing.T) {
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{DockName: "labs", BayName: manifest.HomeBayID, Type: manifest.SurfaceTypeShell, Name: "shell"}); err != nil {
		t.Fatalf("SurfaceAdd(home): %v", err)
	}
	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	winID := home.Surfaces[0].Tmux.WindowID

	if err := eng.BayClose("labs", manifest.HomeBayID, false); err != nil {
		t.Fatalf("BayClose(home): %v", err)
	}
	m2, _ := eng.LoadManifest()
	dock := m2.FindDock("labs")
	if dock == nil {
		t.Fatal("dock disappeared")
	}
	if dock.Path != dockPath {
		t.Fatalf("dock.Path = %q, want %q", dock.Path, dockPath)
	}
	if home := dock.FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("home bay still persisted after close: %+v", home)
	}
	mockGit := eng.Git.(*git.Mock)
	if got := len(mockGit.RemovedWorktrees()); got != 0 {
		t.Fatalf("removed worktrees = %d, want 0", got)
	}
	mockTmux := eng.Tmux.(*tmux.Mock)
	if exists, _ := mockTmux.WindowExists(winID); exists {
		t.Fatalf("home window %s still exists after close", winID)
	}
	if has, _ := mockTmux.HasSession("labs"); !has {
		t.Fatal("tmux session should remain while non-home windows are live")
	}
	for _, call := range mockTmux.Calls {
		if call.Method == "KillSession" {
			t.Fatalf("BayClose(home) should not kill session with non-home windows live; calls: %+v", mockTmux.Calls)
		}
	}
	if _, err := eng.BayShow("labs", "b1"); err != nil {
		t.Fatalf("non-home bay disappeared after BayClose(home): %v", err)
	}
}

func TestBayClose_HomeLastSurfaceRequiresConfirmation(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	winID := home.Surfaces[0].Tmux.WindowID

	err := eng.BayClose("labs", manifest.HomeBayID, false)
	if err == nil || !strings.Contains(err.Error(), "dismisses the dock UI/session") {
		t.Fatalf("BayClose(last home) error = %v", err)
	}

	m2, _ := eng.LoadManifest()
	if home := m2.FindDock("labs").FindBayByID(manifest.HomeBayID); home == nil || len(home.Surfaces) != 1 {
		t.Fatalf("home after rejected close = %+v, want unchanged", home)
	}
	mockTmux := eng.Tmux.(*tmux.Mock)
	if exists, _ := mockTmux.WindowExists(winID); !exists {
		t.Fatalf("home window %s was killed after rejected close", winID)
	}
	windows, _ := mockTmux.ListWindows("labs")
	for _, w := range windows {
		val, _ := mockTmux.GetWindowOption(w.ID, "@bay-placeholder")
		if val == "1" {
			t.Fatalf("rejected last-home close should not create placeholder %s", w.ID)
		}
	}
}

func TestBayClose_HomeForceDismissesLastHomeDock(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path
	agent := m.FindDock("labs").Agent

	mockTmux := eng.Tmux.(*tmux.Mock)
	mockTmux.Calls = nil
	checkedBeforeKill := false
	mockTmux.OnKill = func(method, target string) {
		if method != "KillSession" || target != "labs" {
			return
		}
		checkedBeforeKill = true
		mid, err := eng.LoadManifest()
		if err != nil {
			t.Fatalf("LoadManifest during KillSession: %v", err)
		}
		dock := mid.FindDock("labs")
		if dock == nil {
			t.Fatal("dock was unregistered before KillSession")
		}
		if dock.Path != dockPath {
			t.Fatalf("dock.Path during KillSession = %q, want %q", dock.Path, dockPath)
		}
		if dock.Agent != agent {
			t.Fatalf("dock agent during KillSession = %q, want %q", dock.Agent, agent)
		}
		if home := dock.FindBayByID(manifest.HomeBayID); home != nil {
			t.Fatalf("home persisted during KillSession: %+v", home)
		}
	}

	if err := eng.BayClose("labs", manifest.HomeBayID, true); err != nil {
		t.Fatalf("BayClose(home --force): %v", err)
	}
	if !checkedBeforeKill {
		t.Fatal("KillSession hook did not run")
	}

	m2, _ := eng.LoadManifest()
	dock := m2.FindDock("labs")
	if dock == nil {
		t.Fatal("dock disappeared after confirmed home dismissal")
	}
	if dock.Path != dockPath {
		t.Fatalf("dock.Path = %q, want %q", dock.Path, dockPath)
	}
	if dock.Agent != agent {
		t.Fatalf("dock agent = %q, want %q", dock.Agent, agent)
	}
	if home := dock.FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("home bay persisted after confirmed dismissal: %+v", home)
	}
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("tmux session still exists after confirmed home dismissal")
	}
	mockGit := eng.Git.(*git.Mock)
	if got := len(mockGit.RemovedWorktrees()); got != 0 {
		t.Fatalf("removed worktrees = %d, want 0", got)
	}
	for _, call := range mockTmux.Calls {
		if call.Method == "NewWindow" {
			t.Fatalf("confirmed dismissal should not create a replacement window; calls: %+v", mockTmux.Calls)
		}
		if call.Method == "SetWindowOption" && len(call.Args) >= 3 && call.Args[1] == "@bay-placeholder" {
			t.Fatalf("confirmed dismissal should not create a placeholder; calls: %+v", mockTmux.Calls)
		}
	}
}

func TestBayClose_HomeForceRemovesPersistedHomeWhenSessionGone(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path

	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.KillSession("labs"); err != nil {
		t.Fatalf("seed KillSession: %v", err)
	}
	mockTmux.Calls = nil

	if err := eng.BayClose("labs", manifest.HomeBayID, true); err != nil {
		t.Fatalf("BayClose(home --force) with missing session: %v", err)
	}
	m2, _ := eng.LoadManifest()
	dock := m2.FindDock("labs")
	if dock == nil {
		t.Fatal("dock disappeared")
	}
	if dock.Path != dockPath {
		t.Fatalf("dock.Path = %q, want %q", dock.Path, dockPath)
	}
	if home := dock.FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("home persisted after forced close with missing session: %+v", home)
	}
	if dock.SessionID != "" {
		t.Fatalf("dock.SessionID = %q, want cleared", dock.SessionID)
	}
}

func TestBayClose_HomePlaceholderDoesNotPreventDismissal(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	mockTmux := eng.Tmux.(*tmux.Mock)
	placeholderID, err := mockTmux.NewWindow("labs", "~", "")
	if err != nil {
		t.Fatalf("NewWindow placeholder: %v", err)
	}
	if err := mockTmux.SetWindowOption(placeholderID, "@bay-placeholder", "1"); err != nil {
		t.Fatalf("SetWindowOption placeholder: %v", err)
	}

	err = eng.BayClose("labs", manifest.HomeBayID, false)
	if err == nil || !strings.Contains(err.Error(), "dismisses the dock UI/session") {
		t.Fatalf("BayClose(home with placeholder) error = %v", err)
	}
	if has, _ := mockTmux.HasSession("labs"); !has {
		t.Fatal("unconfirmed close killed the session")
	}

	if err := eng.BayClose("labs", manifest.HomeBayID, true); err != nil {
		t.Fatalf("BayClose(home --force): %v", err)
	}
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("placeholder kept session alive after confirmed dismissal")
	}
}

func TestBayClose_HomeArbitraryTildeWindowKeepsSessionLive(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	homeWinID := home.Surfaces[0].Tmux.WindowID

	mockTmux := eng.Tmux.(*tmux.Mock)
	tildeID, err := mockTmux.NewWindow("labs", "~", m.FindDock("labs").Path)
	if err != nil {
		t.Fatalf("NewWindow arbitrary ~: %v", err)
	}

	if err := eng.BayClose("labs", manifest.HomeBayID, false); err != nil {
		t.Fatalf("BayClose(home with arbitrary ~): %v", err)
	}
	if exists, _ := mockTmux.WindowExists(homeWinID); exists {
		t.Fatalf("home window %s still exists", homeWinID)
	}
	if exists, _ := mockTmux.WindowExists(tildeID); !exists {
		t.Fatalf("arbitrary ~ window %s was treated as home/placeholder", tildeID)
	}
	if has, _ := mockTmux.HasSession("labs"); !has {
		t.Fatal("session should remain because arbitrary ~ is a live window")
	}
	for _, call := range mockTmux.Calls {
		if call.Method == "KillSession" {
			t.Fatalf("arbitrary ~ should prevent dock dismissal; calls: %+v", mockTmux.Calls)
		}
	}
}

func TestSurfaceClose_HomeLastSurfaceRequiresConfirmation(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	winID := home.Surfaces[0].Tmux.WindowID

	err := eng.SurfaceClose("labs", manifest.HomeBayID, "shell", false)
	if err == nil || !strings.Contains(err.Error(), "dismisses the dock UI/session") {
		t.Fatalf("SurfaceClose(last home) error = %v", err)
	}

	m2, _ := eng.LoadManifest()
	if home := m2.FindDock("labs").FindBayByID(manifest.HomeBayID); home == nil || len(home.Surfaces) != 1 {
		t.Fatalf("home after rejected surface close = %+v, want unchanged", home)
	}
	mockTmux := eng.Tmux.(*tmux.Mock)
	if exists, _ := mockTmux.WindowExists(winID); !exists {
		t.Fatalf("home window %s was killed after rejected surface close", winID)
	}
}

func TestSurfaceClose_HomeLastLivePaneRequiresConfirmationWithStaleRecordedSurface(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	live := home.Surfaces[0]
	winID := live.Tmux.WindowID

	if err := eng.withManifest(func(m *manifest.Manifest) error {
		home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
		home.Surfaces = append(home.Surfaces, manifest.Surface{
			ID:      live.ID + 1,
			Name:    "stale",
			Type:    manifest.SurfaceTypeShell,
			Backend: manifest.SurfaceBackendTmux,
			Tmux: &manifest.TmuxAttrs{
				PaneID:      "%999",
				WindowID:    winID,
				LayoutGroup: live.Tmux.LayoutGroup,
				SplitFrom:   live.ID,
				SplitDir:    "v",
			},
		})
		return nil
	}); err != nil {
		t.Fatalf("seed stale home surface: %v", err)
	}

	err := eng.SurfaceClose("labs", manifest.HomeBayID, live.Name, false)
	if err == nil || !strings.Contains(err.Error(), "dismisses the dock UI/session") {
		t.Fatalf("SurfaceClose(last live home pane) error = %v", err)
	}

	m2, _ := eng.LoadManifest()
	home = m2.FindDock("labs").FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 2 {
		t.Fatalf("home after rejected close = %+v, want unchanged live+stale surfaces", home)
	}
	mockTmux := eng.Tmux.(*tmux.Mock)
	if exists, _ := mockTmux.WindowExists(winID); !exists {
		t.Fatalf("home window %s was killed after rejected close", winID)
	}

	if err := eng.SurfaceClose("labs", manifest.HomeBayID, live.Name, true); err != nil {
		t.Fatalf("SurfaceClose(last live home pane --force): %v", err)
	}
	m3, _ := eng.LoadManifest()
	if home := m3.FindDock("labs").FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("stale home records persisted after confirmed dismissal: %+v", home)
	}
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("tmux session still exists after confirmed stale-record dismissal")
	}
}

func TestHome_AfterDismissalRecreatesUsableHomeShell(t *testing.T) {
	eng, _ := testEngine(t)
	withDeterministicSessionID(t, "first-home-session", "second-home-session")

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home create: %v", err)
	}
	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path
	if got := m.FindDock("labs").SessionID; got != "first-home-session" {
		t.Fatalf("initial dock.SessionID = %q, want first-home-session", got)
	}

	if err := eng.SurfaceClose("labs", manifest.HomeBayID, "shell", true); err != nil {
		t.Fatalf("SurfaceClose(home --force): %v", err)
	}
	m2, _ := eng.LoadManifest()
	if home := m2.FindDock("labs").FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("home persisted after dismissal: %+v", home)
	}
	if got := m2.FindDock("labs").SessionID; got != "" {
		t.Fatalf("dismissed dock SessionID = %q, want cleared", got)
	}

	mockTmux := eng.Tmux.(*tmux.Mock)
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("session still exists after dismissal")
	}
	mockTmux.Calls = nil
	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home recreate: %v", err)
	}

	m3, _ := eng.LoadManifest()
	dock := m3.FindDock("labs")
	if dock == nil {
		t.Fatal("dock disappeared")
	}
	if dock.Path != dockPath {
		t.Fatalf("dock.Path = %q, want %q", dock.Path, dockPath)
	}
	if dock.SessionID != "second-home-session" {
		t.Fatalf("recreated dock.SessionID = %q, want second-home-session", dock.SessionID)
	}
	home := dock.FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 1 || home.Surfaces[0].Type != manifest.SurfaceTypeShell {
		t.Fatalf("recreated home = %+v, want one shell", home)
	}
	if has, _ := mockTmux.HasSession("labs"); !has {
		t.Fatal("Home did not recreate tmux session")
	}
	sawNewHomeWindow := false
	for _, call := range mockTmux.Calls {
		if call.Method == "NewWindow" && len(call.Args) >= 3 && call.Args[2] == dockPath {
			sawNewHomeWindow = true
		}
	}
	if !sawNewHomeWindow {
		t.Fatalf("Home did not create a shell at dock path %q; calls: %+v", dockPath, mockTmux.Calls)
	}
	windows, _ := mockTmux.ListWindows("labs")
	for _, w := range windows {
		val, _ := mockTmux.GetWindowOption(w.ID, "@bay-placeholder")
		if val == "1" {
			t.Fatalf("recreated home should not leave placeholder %s", w.ID)
		}
	}
}

func TestHomeVisibility_EmptyHiddenVisibleListed(t *testing.T) {
	eng, _ := testEngine(t)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	infos, err := eng.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, bay := range infos[0].Bays {
		if bay.ID == manifest.HomeBayID {
			t.Fatalf("empty home should not appear in list: %+v", infos[0].Bays)
		}
	}
	m, _ := eng.LoadManifest()
	entries := nav.CollectEntries(m, eng.Tmux)
	for _, entry := range entries {
		if entry.BayName == manifest.HomeBayID {
			t.Fatalf("empty home should not appear in navigation entries: %+v", entries)
		}
	}

	if err := eng.SurfaceAdd(SurfaceAddOptions{DockName: "labs", BayName: manifest.HomeBayID, Type: manifest.SurfaceTypeShell, Name: "shell"}); err != nil {
		t.Fatalf("SurfaceAdd(home): %v", err)
	}
	mockGit := eng.Git.(*git.Mock)
	mockGit.SetDirty(m.FindDock("labs").Path, true)
	infos, err = eng.List()
	if err != nil {
		t.Fatalf("List after home materialized: %v", err)
	}
	foundList := false
	for _, bay := range infos[0].Bays {
		if bay.ID == manifest.HomeBayID && bay.SurfaceCount == 1 {
			if bay.Dirty || bay.Pending || bay.Branch != "" || bay.PR != "" {
				t.Fatalf("visible home has worktree metadata: %+v", bay)
			}
			foundList = true
		}
	}
	if !foundList {
		t.Fatalf("visible home missing from list: %+v", infos[0].Bays)
	}
	m, _ = eng.LoadManifest()
	entries = nav.CollectEntries(m, eng.Tmux)
	foundNav := false
	for _, entry := range entries {
		if entry.BayName == manifest.HomeBayID && entry.SurfaceCount == 1 {
			foundNav = true
		}
	}
	if !foundNav {
		t.Fatalf("visible home missing from navigation entries: %+v", entries)
	}
}

func TestEdit_HomeReturnsDockPathWithoutMaterializing(t *testing.T) {
	eng, _ := testEngine(t)

	m, _ := eng.LoadManifest()
	wantPath := m.FindDock("labs").Path
	path, err := eng.Edit("labs", manifest.HomeBayID)
	if err != nil {
		t.Fatalf("Edit(home): %v", err)
	}
	if path != wantPath {
		t.Fatalf("Edit(home) path = %q, want %q", path, wantPath)
	}
	m2, _ := eng.LoadManifest()
	if home := m2.FindDock("labs").FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("Edit(home) should not materialize empty home, got %+v", home)
	}
}

func TestEditAllParentDir_HomeDoesNotChangeDockEditorTarget(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	dock := m.FindDock("labs")
	want := dock.EffectiveWorktreeDir()

	got, err := eng.EditAllParentDir("labs")
	if err != nil {
		t.Fatalf("EditAllParentDir: %v", err)
	}
	if got != want {
		t.Fatalf("EditAllParentDir = %q, want %q", got, want)
	}
}

func TestDockClose_HomeOnlyDockCloses(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}

	if err := eng.DockClose("labs", false); err != nil {
		t.Fatalf("DockClose(home-only): %v", err)
	}
	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if dock := m.FindDock("labs"); dock != nil {
		t.Fatalf("dock still present after DockClose: %+v", dock)
	}
	mockTmux := eng.Tmux.(*tmux.Mock)
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("tmux session still exists after DockClose")
	}
	mockGit := eng.Git.(*git.Mock)
	if got := len(mockGit.RemovedWorktrees()); got != 0 {
		t.Fatalf("removed worktrees = %d, want 0", got)
	}
}

func TestResolveSelf_DockPathDoesNotInferHomeFromCWD(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock("labs")
	home := dock.FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 1 || home.Surfaces[0].Tmux == nil {
		t.Fatalf("home = %+v, want one tmux surface", home)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dock.Path); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	if _, _, err := eng.ResolveSelf(); err == nil {
		t.Fatal("ResolveSelf from dock path inferred home; want no bay")
	}
	ctx, err := eng.CurrentContext()
	if err != nil {
		t.Fatalf("CurrentContext: %v", err)
	}
	if ctx.Dock != "labs" || ctx.BayID != "" || ctx.Bay != "" {
		t.Fatalf("CurrentContext from dock path = %#v, want dock-only context", ctx)
	}

	mockTmux := eng.Tmux.(*tmux.Mock)
	mockTmux.SetCurrentSession("labs")
	mockTmux.SetCurrentWindowID(home.Surfaces[0].Tmux.WindowID)
	mockTmux.SetCurrentPaneID(home.Surfaces[0].Tmux.PaneID)
	dockName, bayID, err := eng.ResolveSelf()
	if err != nil {
		t.Fatalf("ResolveSelf in home tmux surface: %v", err)
	}
	if dockName != "labs" || bayID != manifest.HomeBayID {
		t.Fatalf("ResolveSelf in home tmux surface = (%q, %q), want (labs, home)", dockName, bayID)
	}
}

func TestCurrentContext_HomeTmuxIdentityWinsOverWorktreeCWD(t *testing.T) {
	eng, _ := testEngine(t)

	worktreeBay, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := os.MkdirAll(worktreeBay.Path, 0o755); err != nil {
		t.Fatalf("mkdir worktree path: %v", err)
	}
	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}

	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock("labs")
	home := dock.FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 1 || home.Surfaces[0].Tmux == nil {
		t.Fatalf("home = %+v, want one tmux surface", home)
	}
	homeSurface := home.Surfaces[0]

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(worktreeBay.Path); err != nil {
		t.Fatalf("chdir worktree: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	mockTmux := eng.Tmux.(*tmux.Mock)
	mockTmux.SetCurrentSession("labs")
	mockTmux.SetCurrentWindowID(homeSurface.Tmux.WindowID)
	mockTmux.SetCurrentPaneID(homeSurface.Tmux.PaneID)

	dockName, bayID, err := eng.ResolveSelf()
	if err != nil {
		t.Fatalf("ResolveSelf: %v", err)
	}
	if dockName != "labs" || bayID != manifest.HomeBayID {
		t.Fatalf("ResolveSelf = (%q, %q), want (labs, home)", dockName, bayID)
	}

	ctx, err := eng.CurrentContext()
	if err != nil {
		t.Fatalf("CurrentContext: %v", err)
	}
	if ctx.Dock != "labs" || ctx.BayID != manifest.HomeBayID || ctx.Bay != manifest.HomeBayID {
		t.Fatalf("CurrentContext bay = %#v, want labs/home", ctx)
	}
	if ctx.Surface != homeSurface.Name || ctx.SurfaceID != homeSurface.ID {
		t.Fatalf("CurrentContext surface = (%q, %d), want (%q, %d)", ctx.Surface, ctx.SurfaceID, homeSurface.Name, homeSurface.ID)
	}
	if ctx.Path != config.CanonicalPath(dock.Path) {
		t.Fatalf("CurrentContext path = %q, want %q", ctx.Path, config.CanonicalPath(dock.Path))
	}

	mockTmux.SetCurrentPaneID("%not-recorded")
	ctx, err = eng.CurrentContext()
	if err != nil {
		t.Fatalf("CurrentContext unknown pane: %v", err)
	}
	if ctx.Dock != "labs" || ctx.BayID != manifest.HomeBayID || ctx.Bay != manifest.HomeBayID || ctx.Surface != "" {
		t.Fatalf("CurrentContext unknown pane = %#v, want bay-level home context", ctx)
	}
}

func TestResolveSelf_HomeTmuxIdentityRejectsMalformedHome(t *testing.T) {
	eng, _ := testEngine(t)

	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 1 || home.Surfaces[0].Tmux == nil {
		t.Fatalf("home = %+v, want one tmux surface", home)
	}
	winID := home.Surfaces[0].Tmux.WindowID
	paneID := home.Surfaces[0].Tmux.PaneID

	if err := eng.withManifest(func(m *manifest.Manifest) error {
		home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
		home.Worktree = &manifest.WorktreeAttrs{Branch: "feature/not-home"}
		return nil
	}); err != nil {
		t.Fatalf("mutate manifest: %v", err)
	}

	mockTmux := eng.Tmux.(*tmux.Mock)
	mockTmux.SetCurrentSession("labs")
	mockTmux.SetCurrentWindowID(winID)
	mockTmux.SetCurrentPaneID(paneID)

	_, _, err = eng.ResolveSelf()
	if err == nil || !strings.Contains(err.Error(), "malformed home bay") {
		t.Fatalf("ResolveSelf malformed home error = %v, want malformed home bay", err)
	}
	if _, err := eng.CurrentContext(); err == nil || !strings.Contains(err.Error(), "malformed home bay") {
		t.Fatalf("CurrentContext malformed home error = %v, want malformed home bay", err)
	}
}

func TestSurfaceAdd_HomeClaimsExistingUntaggedSession(t *testing.T) {
	eng, _ := testEngine(t)
	withDeterministicSessionID(t, "home-session-uuid")
	mockTmux := eng.Tmux.(*tmux.Mock)
	if err := mockTmux.NewSession("labs"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs",
		BayName:  manifest.HomeBayID,
		Type:     manifest.SurfaceTypeShell,
		Name:     "shell",
	}); err != nil {
		t.Fatalf("SurfaceAdd(home): %v", err)
	}

	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock("labs")
	if dock.SessionID != "home-session-uuid" {
		t.Fatalf("dock.SessionID = %q, want home-session-uuid", dock.SessionID)
	}
	marker, _ := mockTmux.GetSessionOption("labs", sessionIDOption)
	if marker != "home-session-uuid" {
		t.Fatalf("tmux marker = %q, want home-session-uuid", marker)
	}
	home := dock.FindBayByID(manifest.HomeBayID)
	if home == nil || len(home.Surfaces) != 1 || home.Surfaces[0].Tmux == nil {
		t.Fatalf("home = %+v, want one tmux surface", home)
	}
}

func mustEngineHome(t *testing.T, eng *Engine) *manifest.Bay {
	t.Helper()
	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock("labs")
	if dock == nil {
		t.Fatal("missing labs dock")
	}
	return dock.FindBayByID(manifest.HomeBayID)
}
