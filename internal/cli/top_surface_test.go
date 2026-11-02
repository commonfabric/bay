package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
)

// --- runSurfaceNew ---

func TestRunSurfaceNew_Shell(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	err := runSurfaceNew(eng, "labs", "b1", surfaceNewOpts{
		Type:     manifest.SurfaceTypeShell,
		SplitDir: "v",
	})
	if err != nil {
		t.Fatalf("runSurfaceNew: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	// b1 was created with --shell so it has one shell already; the new one
	// should be the second.
	if len(bay.Surfaces) != 2 {
		t.Fatalf("expected 2 surfaces, got %d", len(bay.Surfaces))
	}
	added := bay.Surfaces[1]
	if added.Type != manifest.SurfaceTypeShell {
		t.Errorf("type = %s, want shell", added.Type)
	}
	// uniqueSurfaceName disambiguates against the existing "shell".
	if !strings.HasPrefix(added.Name, "shell") {
		t.Errorf("name = %q, want a shell-prefixed name", added.Name)
	}
}

func TestRunSurfaceNew_HomeCreatesSurface(t *testing.T) {
	eng, mockTmux, _, _ := testNavEngine(t)
	mockTmux.Calls = nil

	err := runSurfaceNew(eng, "labs", manifest.HomeBayID, surfaceNewOpts{
		Type: manifest.SurfaceTypeShell,
	})
	if err != nil {
		t.Fatalf("runSurfaceNew(home): %v", err)
	}
	bay, err := eng.BayShow("labs", manifest.HomeBayID)
	if err != nil {
		t.Fatalf("BayShow(home): %v", err)
	}
	if bay.Type != manifest.BayTypeHome || len(bay.Surfaces) != 1 {
		t.Fatalf("home bay = %+v, want one home surface", bay)
	}
	sawNewWindow := false
	for _, call := range mockTmux.Calls {
		if call.Method == "NewWindow" && len(call.Args) >= 2 && call.Args[1] == manifest.HomeBayID {
			sawNewWindow = true
		}
	}
	if !sawNewWindow {
		t.Fatalf("runSurfaceNew(home) did not create a home window; calls: %+v", mockTmux.Calls)
	}
}

func TestRunSurfaceNew_HomeAgentUsesDockDefault(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)

	err := runSurfaceNew(eng, "labs", manifest.HomeBayID, surfaceNewOpts{
		Type: manifest.SurfaceTypeAgent,
	})
	if err != nil {
		t.Fatalf("runSurfaceNew(home agent): %v", err)
	}
	bay, err := eng.BayShow("labs", manifest.HomeBayID)
	if err != nil {
		t.Fatalf("BayShow(home): %v", err)
	}
	if len(bay.Surfaces) != 1 {
		t.Fatalf("home surfaces = %+v, want one agent", bay.Surfaces)
	}
	added := bay.Surfaces[0]
	if added.Type != manifest.SurfaceTypeAgent {
		t.Fatalf("home surface type = %s, want agent", added.Type)
	}
	if added.Agent == nil || *added.Agent != "claude" {
		t.Fatalf("home agent = %v, want dock default claude", added.Agent)
	}
}

func TestRunSurfaceNew_Agent(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	err := runSurfaceNew(eng, "labs", "b1", surfaceNewOpts{
		Type:     manifest.SurfaceTypeAgent,
		Agent:    "claude",
		SplitDir: "v",
	})
	if err != nil {
		t.Fatalf("runSurfaceNew: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	if len(bay.Surfaces) != 2 {
		t.Fatalf("expected 2 surfaces, got %d", len(bay.Surfaces))
	}
	added := bay.Surfaces[1]
	if added.Type != manifest.SurfaceTypeAgent {
		t.Errorf("type = %s, want agent", added.Type)
	}
	if added.Agent == nil || *added.Agent != "claude" {
		t.Errorf("agent = %v, want claude", added.Agent)
	}
}

func TestRunSurfaceNew_Cmd(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	err := runSurfaceNew(eng, "labs", "b1", surfaceNewOpts{
		Type:     manifest.SurfaceTypeCmd,
		Command:  "tail -f log.txt",
		Name:     "tail",
		SplitDir: "v",
	})
	if err != nil {
		t.Fatalf("runSurfaceNew: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	if len(bay.Surfaces) != 2 {
		t.Fatalf("expected 2 surfaces, got %d", len(bay.Surfaces))
	}
	added := bay.Surfaces[1]
	if added.Type != manifest.SurfaceTypeCmd {
		t.Errorf("type = %s, want cmd", added.Type)
	}
	if added.Name != "tail" {
		t.Errorf("name = %q, want tail (explicit)", added.Name)
	}
	if added.Command == nil || *added.Command != "tail -f log.txt" {
		t.Errorf("command = %v, want tail -f log.txt", added.Command)
	}
}

func TestRunSurfaceNew_AgentDefaultName(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	// No opts.Name — should default to "agent".
	err := runSurfaceNew(eng, "labs", "b1", surfaceNewOpts{
		Type:     manifest.SurfaceTypeAgent,
		Agent:    "claude",
		SplitDir: "v",
	})
	if err != nil {
		t.Fatalf("runSurfaceNew: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	foundAgent := false
	for _, s := range bay.Surfaces {
		if s.Type == manifest.SurfaceTypeAgent && strings.HasPrefix(s.Name, "agent") {
			foundAgent = true
			break
		}
	}
	if !foundAgent {
		t.Error("expected an agent-typed surface with name prefix 'agent'")
	}
}

func TestRunSurfaceNew_CmdDefaultName(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	// No opts.Name — should default to "cmd".
	err := runSurfaceNew(eng, "labs", "b1", surfaceNewOpts{
		Type:     manifest.SurfaceTypeCmd,
		Command:  "tail -f log.txt",
		SplitDir: "v",
	})
	if err != nil {
		t.Fatalf("runSurfaceNew: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	foundCmd := false
	for _, s := range bay.Surfaces {
		if s.Type == manifest.SurfaceTypeCmd && strings.HasPrefix(s.Name, "tail") {
			foundCmd = true
			break
		}
	}
	if !foundCmd {
		t.Error("expected a cmd-typed surface with name derived from command ('tail')")
	}
}

func TestRunSurfaceNew_NameWithColonIsRejected(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	err := runSurfaceNew(eng, "labs", "b1", surfaceNewOpts{
		Type:     manifest.SurfaceTypeShell,
		Name:     "b1:logs",
		SplitDir: "v",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot contain ':'") {
		t.Errorf("expected colon-rejection error, got %v", err)
	}
}

func TestRunEditCreate_HomeCreatesEditorAtDockPath(t *testing.T) {
	eng, mockTmux, _, _ := testNavEngine(t)
	m, _ := eng.LoadManifest()
	dockPath := m.FindDock("labs").Path
	mockTmux.Calls = nil

	if err := runEditCreate(eng, "labs:"+manifest.HomeBayID, "vim", ""); err != nil {
		t.Fatalf("runEditCreate(home): %v", err)
	}

	home, err := eng.BayShow("labs", manifest.HomeBayID)
	if err != nil {
		t.Fatalf("BayShow(home): %v", err)
	}
	if len(home.Surfaces) != 1 || home.Surfaces[0].Type != manifest.SurfaceTypeEditor {
		t.Fatalf("home surfaces = %+v, want one editor", home.Surfaces)
	}
	if home.Surfaces[0].Command == nil || *home.Surfaces[0].Command != "vim "+dockPath {
		t.Fatalf("editor command = %v, want vim %s", home.Surfaces[0].Command, dockPath)
	}

	sawRespawn := false
	for _, call := range mockTmux.Calls {
		if call.Method == "RespawnPane" && len(call.Args) >= 3 && call.Args[1] == dockPath && call.Args[2] == "vim "+dockPath {
			sawRespawn = true
		}
	}
	if !sawRespawn {
		t.Fatalf("home editor should run at dock path %q; calls: %+v", dockPath, mockTmux.Calls)
	}
}

// --- editTargetFromArgs ---

func TestEditTargetFromArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		bay     string
		dock    string
		want    string
		wantErr bool
	}{
		{"no args no flags", nil, "", "", "self", false},
		{"positional only", []string{"auth-fix"}, "", "", "auth-fix", false},
		{"positional with dock prefix", []string{"labs:auth-fix"}, "", "", "labs:auth-fix", false},
		{"bay flag", nil, "b1", "", "b1", false},
		{"bay + dock flags", nil, "b1", "labs", "labs:b1", false},
		{"positional + bay flag", []string{"a"}, "b", "", "", true},
		{"positional + dock flag", []string{"a"}, "", "labs", "", true},
		{"dock flag alone", nil, "", "labs", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := editTargetFromArgs(c.args, c.bay, c.dock)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestResolveSplit_DefaultsToPane(t *testing.T) {
	cases := []struct {
		name     string
		splitDir string
		window   bool
		pane     bool
		want     string
	}{
		{name: "default", want: "v"},
		{name: "window flag", window: true, want: ""},
		{name: "pane flag", pane: true, want: "v"},
		{name: "split h", splitDir: "h", want: "h"},
		{name: "split overrides window", splitDir: "h", window: true, want: "h"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveSplit(c.splitDir, c.window, c.pane); got != c.want {
				t.Errorf("resolveSplit() = %q, want %q", got, c.want)
			}
		})
	}
}

// --- runSurfaceClose ---

func TestRunSurfaceClose_ByName(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeAgent, Name: "agent", Agent: "claude", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	// Use qualified form to avoid depending on tmux ResolveSelf state.
	// force=true bypasses the agent close prompt — the prompt path is
	// covered by dedicated tests below.
	if err := runSurfaceClose(eng, []string{"b1:agent"}, "", "", true); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	for _, s := range bay.Surfaces {
		if s.Name == "agent" {
			t.Errorf("surface 'agent' should have been closed, still present")
		}
	}
}

func TestRunSurfaceClose_NoArgsErrors(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	// Bare invocation: no args, no flags. Should give the friendlier message
	// that mentions the 'self' keyword.
	err := runSurfaceClose(eng, nil, "", "", false)
	if err == nil || !strings.Contains(err.Error(), "specify a surface name") {
		t.Errorf("expected 'specify a surface name' error, got %v", err)
	}
	// Flags set but no positional: more specific message.
	err = runSurfaceClose(eng, nil, "b1", "", false)
	if err == nil || !strings.Contains(err.Error(), "--bay/--dock require") {
		t.Errorf("expected '--bay/--dock require' error with --bay, got %v", err)
	}
	err = runSurfaceClose(eng, nil, "", "labs", false)
	if err == nil || !strings.Contains(err.Error(), "--bay/--dock require") {
		t.Errorf("expected '--bay/--dock require' error with --dock, got %v", err)
	}
}

func TestRunSurfaceClose_CrossBay(t *testing.T) {
	eng := surfaceArgFixture(t)

	// labs:b2 (the "solo" bay) has a uniquely-addressable bare ID.
	// Close its agent surface via the helper using bare bay:surface form.
	if err := runSurfaceClose(eng, []string{"b2:agent"}, "", "", true); err != nil {
		t.Fatalf("runSurfaceClose: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b2")
	for _, s := range bay.Surfaces {
		if s.Name == "agent" {
			t.Errorf("agent should have been closed in labs:b2")
		}
	}
}

// --- self keyword end-to-end ---

// selfFixture (defined in target_test.go) builds a bay with two
// surfaces and pins the tmux mock to the second one. Tests below reuse it.

func TestRunSurfaceClose_Self(t *testing.T) {
	eng := selfFixture(t)
	// Pane is pinned to "second" (a shell). bay close self should remove it.
	if err := runSurfaceClose(eng, []string{"self"}, "", "", true); err != nil {
		t.Fatalf("runSurfaceClose self: %v", err)
	}
	bay, _ := eng.BayShow("labs", "b1")
	for _, s := range bay.Surfaces {
		if s.Name == "second" {
			t.Errorf("surface 'second' should have been closed via 'self'")
		}
	}
}

func TestRunSurfaceClose_LiteralSelfWins(t *testing.T) {
	eng := selfFixture(t)
	// Add a literal surface named "self" — bay close self should target it.
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeShell, Name: "self", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd self: %v", err)
	}

	if err := runSurfaceClose(eng, []string{"self"}, "", "", true); err != nil {
		t.Fatalf("runSurfaceClose self: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	hasLiteralSelf, hasSecond := false, false
	for _, s := range bay.Surfaces {
		if s.Name == "self" {
			hasLiteralSelf = true
		}
		if s.Name == "second" {
			hasSecond = true
		}
	}
	if hasLiteralSelf {
		t.Error("literal 'self' surface should have been closed")
	}
	if !hasSecond {
		t.Error("'second' (the current pane) should NOT have been closed when literal 'self' exists")
	}
}

func TestRunSurfaceShow_Self(t *testing.T) {
	eng := selfFixture(t)
	if err := runSurfaceShow(eng, []string{"self"}, "", ""); err != nil {
		t.Errorf("runSurfaceShow self: %v", err)
	}
}

func TestRunSurfaceRename_Self(t *testing.T) {
	eng := selfFixture(t)
	// Rename "second" (the current pane's surface) to "renamed" via self keyword.
	if err := runSurfaceRename(eng, []string{"self", "renamed"}, "", ""); err != nil {
		t.Fatalf("runSurfaceRename self: %v", err)
	}
	bay, _ := eng.BayShow("labs", "b1")
	foundRenamed, foundSecond := false, false
	for _, s := range bay.Surfaces {
		if s.Name == "renamed" {
			foundRenamed = true
		}
		if s.Name == "second" {
			foundSecond = true
		}
	}
	if !foundRenamed {
		t.Error("renamed surface 'renamed' not found")
	}
	if foundSecond {
		t.Error("'second' should have been renamed away")
	}
}

// --- runSurfaceShow ---

func TestRunSurfaceShow_FoundAndNotFound(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeAgent, Name: "agent", Agent: "claude", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	// Found case: should not error.
	if err := runSurfaceShow(eng, []string{"b1:agent"}, "", ""); err != nil {
		t.Errorf("runSurfaceShow on existing surface: %v", err)
	}

	// Not found case: should error.
	err := runSurfaceShow(eng, []string{"b1:nonexistent"}, "", "")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}
}

// --- runSurfaceRename ---

func TestRunSurfaceRename(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeAgent, Name: "agent", Agent: "claude", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	if err := runSurfaceRename(eng, []string{"b1:agent", "agent2"}, "", ""); err != nil {
		t.Fatalf("runSurfaceRename: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	foundOld, foundNew := false, false
	for _, s := range bay.Surfaces {
		if s.Name == "agent" {
			foundOld = true
		}
		if s.Name == "agent2" {
			foundNew = true
		}
	}
	if foundOld {
		t.Error("surface 'agent' still present after rename")
	}
	if !foundNew {
		t.Error("surface 'agent2' not present after rename")
	}
}

// --- resolveSurfaceBay ---

func TestResolveSurfaceBay_NoFlagsUsesSelf(t *testing.T) {
	eng, mockTmux, _, _ := testNavEngine(t)
	bay, _ := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	mockTmux.SetCurrentWindowID(bay.Surfaces[0].Tmux.WindowID)
	mockTmux.SetCurrentPaneID(bay.Surfaces[0].Tmux.PaneID)

	dock, bayName, err := resolveSurfaceBay(eng, "", "")
	if err != nil {
		t.Fatalf("resolveSurfaceBay: %v", err)
	}
	if dock != "labs" || bayName != "b1" {
		t.Errorf("got (%q,%q), want (labs,b1)", dock, bayName)
	}
}

func TestResolveSurfaceBay_FromHomeTmuxTargetsHome(t *testing.T) {
	eng, mockTmux, _, _ := testNavEngine(t)
	worktreeBay, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := os.MkdirAll(worktreeBay.Path, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	home, err := eng.BayShow("labs", manifest.HomeBayID)
	if err != nil {
		t.Fatalf("BayShow(home): %v", err)
	}
	if len(home.Surfaces) != 1 || home.Surfaces[0].Tmux == nil {
		t.Fatalf("home = %+v, want one tmux surface", home)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(worktreeBay.Path); err != nil {
		t.Fatalf("chdir worktree: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	mockTmux.SetCurrentSession("labs")
	mockTmux.SetCurrentWindowID(home.Surfaces[0].Tmux.WindowID)
	mockTmux.SetCurrentPaneID(home.Surfaces[0].Tmux.PaneID)

	dock, bayName, err := resolveSurfaceBay(eng, "", "")
	if err != nil {
		t.Fatalf("resolveSurfaceBay: %v", err)
	}
	if dock != "labs" || bayName != manifest.HomeBayID {
		t.Fatalf("resolveSurfaceBay = (%q, %q), want (labs, home)", dock, bayName)
	}

	if err := runSurfaceNew(eng, dock, bayName, surfaceNewOpts{Type: manifest.SurfaceTypeShell, SplitDir: "v"}); err != nil {
		t.Fatalf("runSurfaceNew from home target: %v", err)
	}
	home, err = eng.BayShow("labs", manifest.HomeBayID)
	if err != nil {
		t.Fatalf("BayShow(home) after add: %v", err)
	}
	if len(home.Surfaces) != 2 {
		t.Fatalf("home surfaces = %+v, want 2", home.Surfaces)
	}
}

func TestResolveSurfaceBay_BayFlag(t *testing.T) {
	eng := bayArgFixture(t)

	// solo's ID is b2 (second labs bay).
	dock, bayName, err := resolveSurfaceBay(eng, "b2", "")
	if err != nil {
		t.Fatalf("resolveSurfaceBay: %v", err)
	}
	if dock != "labs" || bayName != "b2" {
		t.Errorf("got (%q,%q), want (labs,b2)", dock, bayName)
	}
}

func TestResolveSurfaceBay_DockWithoutBay(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	_, _, err := resolveSurfaceBay(eng, "", "labs")
	if err == nil || !strings.Contains(err.Error(), "--bay") {
		t.Errorf("expected --dock-requires-bay error, got %v", err)
	}
}

// --- command tree wiring ---

func TestRoot_CommandGroupsExist(t *testing.T) {
	root := NewRootCmd("test")

	groups := root.Groups()
	var hasSurface, hasBay bool
	for _, g := range groups {
		if g.ID == "surface" {
			hasSurface = true
		}
		if g.ID == "bay" {
			hasBay = true
		}
	}
	if !hasSurface {
		t.Error("expected 'surface' group to be defined on root")
	}
	if !hasBay {
		t.Error("expected 'bay' group to be defined on root")
	}
}

func TestRoot_TopLevelBayVerbsRegistered(t *testing.T) {
	root := NewRootCmd("test")

	bayVerbs := []string{"new", "close", "show", "rename", "describe", "ls", "go", "next", "prev"}
	for _, name := range bayVerbs {
		c, _, err := root.Find([]string{name})
		if err != nil {
			t.Errorf("could not find top-level command %q: %v", name, err)
			continue
		}
		if c.Name() != name {
			t.Errorf("Find(%q) returned %q", name, c.Name())
		}
		if name == "go" || name == "next" || name == "prev" {
			if c.GroupID != "navigation" {
				t.Errorf("command %q has GroupID %q, want navigation", name, c.GroupID)
			}
			continue
		}
		if c.GroupID != "bay" {
			t.Errorf("command %q has GroupID %q, want bay", name, c.GroupID)
		}
	}
}

func TestRoot_HiddenAliasesResolve(t *testing.T) {
	root := NewRootCmd("test")

	cases := []struct {
		alias  string
		target string
	}{
		{"rm", "close"},
		{"cat", "show"},
		{"mv", "rename"},
	}
	for _, c := range cases {
		cmd, _, err := root.Find([]string{c.alias})
		if err != nil {
			t.Errorf("could not find alias %q: %v", c.alias, err)
			continue
		}
		if cmd.Name() != c.target {
			t.Errorf("alias %q resolved to %q, want %q", c.alias, cmd.Name(), c.target)
		}
	}
}

func TestRoot_SurfaceNewSubcommands(t *testing.T) {
	root := NewRootCmd("test")

	newCmd, _, err := root.Find([]string{"surface", "new"})
	if err != nil {
		t.Fatalf("could not find 'surface new': %v", err)
	}

	want := []string{"shell", "agent", "cmd", "edit"}
	have := map[string]bool{}
	for _, c := range newCmd.Commands() {
		have[c.Name()] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("bay surface new is missing subcommand %q", name)
		}
	}
}

// --- existing surface commands moved to surface group ---

func TestRoot_SurfaceFlavoredCommandsInSurfaceGroup(t *testing.T) {
	root := NewRootCmd("test")

	want := []string{"surface", "shell", "edit"}
	for _, name := range want {
		c, _, err := root.Find([]string{name})
		if err != nil {
			t.Errorf("could not find %q: %v", name, err)
			continue
		}
		if c.GroupID != "surface" {
			t.Errorf("command %q has GroupID %q, want surface", name, c.GroupID)
		}
	}
}

func TestRoot_BayCommandRemoved(t *testing.T) {
	root := NewRootCmd("test")

	_, _, err := root.Find([]string{"bay"})
	if err == nil {
		t.Fatalf("bay command should be removed")
	}
}
