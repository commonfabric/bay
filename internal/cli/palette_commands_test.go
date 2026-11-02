package cli

import (
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/palette"
)

// identityDisplay is the no-decoration display func for picker tests
// that don't exercise profile labeling.
func identityDisplay(name string) string { return name }

func TestBuildAgentPickerItems_FullListAlwaysContainsAllAgents(t *testing.T) {
	mru := []string{"antigravity"}
	available := []string{"antigravity", "claude", "codex"}

	_, values := buildAgentPickerItems(mru, available, identityDisplay)

	// Expected layout: mru first (antigravity), separator (""), then
	// the full sorted list.
	want := []string{"antigravity", "", "antigravity", "claude", "codex"}
	if len(values) != len(want) {
		t.Fatalf("values=%v; want %v", values, want)
	}
	for i, w := range want {
		if values[i] != w {
			t.Errorf("values[%d]=%q; want %q", i, values[i], w)
		}
	}
}

func TestBuildAgentPickerItems_NoMRUOmitsSeparator(t *testing.T) {
	_, values := buildAgentPickerItems(nil, []string{"claude", "codex"}, identityDisplay)
	want := []string{"claude", "codex"}
	if len(values) != len(want) {
		t.Fatalf("values=%v; want %v (no separator when MRU empty)", values, want)
	}
	for i, w := range want {
		if values[i] != w {
			t.Errorf("values[%d]=%q; want %q", i, values[i], w)
		}
	}
}

func TestBuildAgentPickerItems_SeparatorValueIsSentinel(t *testing.T) {
	items, values := buildAgentPickerItems([]string{"codex"}, []string{"claude", "codex"}, identityDisplay)
	// items[1] must be the separator: Value < 0 so picker.Pick returning
	// it is treated as cancellation, and an empty values entry so
	// paletteAgentPick's value lookup also rejects it.
	if !strings.HasPrefix(items[1].Display, "─") {
		t.Fatalf("items[1]=%+v; want separator", items[1])
	}
	if items[1].Value >= 0 {
		t.Errorf("separator Value=%d; must be negative", items[1].Value)
	}
	if values[1] != "" {
		t.Errorf("separator value=%q; must be empty", values[1])
	}
}

func TestBuildAgentPickerItems_ValuesIndexedByItemValue(t *testing.T) {
	// Real items have Value == their index in values (except the
	// separator). This test pins that invariant — paletteAgentPick
	// relies on values[sel] to resolve the chosen agent name even when
	// the display label is decorated.
	decorate := func(name string) string { return name + " (claude)" }
	items, values := buildAgentPickerItems([]string{"codex"}, []string{"antigravity", "claude", "codex"}, decorate)
	for _, it := range items {
		if it.Value < 0 {
			continue // separator
		}
		if it.Value >= len(values) {
			t.Errorf("item Value=%d out of range (len=%d)", it.Value, len(values))
			continue
		}
		if decorate(values[it.Value]) != it.Display {
			t.Errorf("values[%d]=%q does not correspond to item.Display=%q", it.Value, values[it.Value], it.Display)
		}
	}
}

func TestAgentDisplayName_DecoratesProfiles(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents["my-fable"] = config.AgentConfig{Extends: "claude", LaunchArgs: []string{"--model", "fable"}}

	cases := map[string]string{
		"claude":   "claude",            // base client: undecorated
		"codex":    "codex",             // base client: undecorated
		"fable":    "fable (claude)",    // built-in profile
		"opus":     "opus (claude)",     // built-in profile
		"my-fable": "my-fable (claude)", // user profile
	}
	for name, want := range cases {
		if got := agentDisplayName(cfg, name); got != want {
			t.Errorf("agentDisplayName(%q) = %q; want %q", name, got, want)
		}
	}
	if got := agentDisplayName(nil, "fable"); got != "fable" {
		t.Errorf("agentDisplayName(nil cfg) = %q; want undecorated fallback", got)
	}
}

func TestAgentAvailable_AcceptsLegacyGeminiAlias(t *testing.T) {
	cfg := config.DefaultConfig()
	if !agentAvailable(cfg, "gemini") {
		t.Fatal("legacy gemini alias should be available")
	}
	if !agentAvailable(nil, "gemini") {
		t.Fatal("legacy gemini alias should be available without config")
	}
}

func TestDetectScope(t *testing.T) {
	cases := []struct {
		name string
		ctx  *engine.Context
		want palette.Scope
	}{
		{"nil ctx", nil, palette.ScopeAnywhere},
		{"empty ctx", &engine.Context{}, palette.ScopeAnywhere},
		{"dock only", &engine.Context{Dock: "bay"}, palette.ScopeInDock},
		{"bay", &engine.Context{Dock: "bay", Bay: "auth-fix"}, palette.ScopeInBay},
		{"surface", &engine.Context{Dock: "bay", Bay: "w", Surface: "shell"}, palette.ScopeInBay},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectScope(c.ctx); got != c.want {
				t.Errorf("detectScope(%+v)=%v; want %v", c.ctx, got, c.want)
			}
		})
	}
}

func TestBuildPaletteEntries_HasExpected30Entries(t *testing.T) {
	// Pin the entry count + unique-ID invariant. Anything that adds or
	// removes a command should touch this test intentionally.
	env := testPaletteEnv()
	entries := buildPaletteEntries(env, palette.ModeWindow)

	const want = 30
	if len(entries) != want {
		t.Errorf("buildPaletteEntries returned %d entries; want %d", len(entries), want)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.ID == "" {
			t.Errorf("entry %q has empty ID", e.Title)
		}
		if seen[e.ID] {
			t.Errorf("duplicate ID %q", e.ID)
		}
		seen[e.ID] = true
	}
}

func TestBuildPaletteEntries_HomeEntriesAreDockScoped(t *testing.T) {
	env := testPaletteEnv()
	env.Hotkeys = fakeHotkeys(map[string]string{
		"bay home":             "Enter",
		"bay shell --bay home": "s",
		"bay edit --bay home":  "e",
		"bay agent --bay home": "a",
	})
	entries := buildPaletteEntries(env, palette.ModePane)

	want := map[string]struct {
		title  string
		hotkey string
	}{
		"go-home":         {"Go to home", "Enter"},
		"home-shell":      {"Home shell", "s"},
		"home-editor":     {"Home editor", "e"},
		"home-agent":      {"Home agent", "a"},
		"home-agent-pick": {"Home agent...", ""},
	}
	for id, w := range want {
		entry := findEntry(t, entries, id)
		if entry.Title != w.title {
			t.Errorf("%s title = %q; want %q", id, entry.Title, w.title)
		}
		if entry.Needs != palette.ScopeInDock {
			t.Errorf("%s Needs = %v; want ScopeInDock", id, entry.Needs)
		}
		if entry.Hotkey != w.hotkey {
			t.Errorf("%s Hotkey = %q; want %q", id, entry.Hotkey, w.hotkey)
		}
	}
}

func TestBuildPaletteEntries_ModeSensitiveHotkeysFlip(t *testing.T) {
	// Install a fake hotkey map so we can assert that the "New shell"
	// entry's Hotkey swaps between window/pane modes as the user Tabs.
	env := testPaletteEnv()
	env.Hotkeys = fakeHotkeys(map[string]string{
		"bay shell --pane":   "M-s",
		"bay shell --window": "M-S",
	})

	win := findEntry(t, buildPaletteEntries(env, palette.ModeWindow), "new-shell")
	pane := findEntry(t, buildPaletteEntries(env, palette.ModePane), "new-shell")

	if win.Hotkey != "M-S" {
		t.Errorf("window-mode new-shell hotkey=%q; want M-S", win.Hotkey)
	}
	if pane.Hotkey != "M-s" {
		t.Errorf("pane-mode new-shell hotkey=%q; want M-s", pane.Hotkey)
	}
}

func TestBuildPaletteEntries_AgentPickEntriesSupportBoundRecents(t *testing.T) {
	env := testPaletteEnv()
	env.Engine.Config = &config.Config{
		Agents: map[string]config.AgentConfig{
			"local": {Command: "local-agent"},
		},
	}
	entries := buildPaletteEntries(env, palette.ModeWindow)

	for _, id := range []string{"new-agent-pick", "new-bay-agent-pick", "home-agent-pick"} {
		entry := findEntry(t, entries, id)
		if entry.ActionWithParam == nil {
			t.Fatalf("%s ActionWithParam is nil; bound recents would reopen the picker", id)
		}
		if entry.ParamValid == nil {
			t.Fatalf("%s ParamValid is nil; stale agent recents would stay visible", id)
		}
		if !entry.ParamValid("codex") {
			t.Fatalf("%s ParamValid(codex)=false; built-in agent should be valid", id)
		}
		if !entry.ParamValid("local") {
			t.Fatalf("%s ParamValid(local)=false; configured agent should be valid", id)
		}
		if entry.ParamValid("retired") {
			t.Fatalf("%s ParamValid(retired)=true; unknown agent should be filtered", id)
		}
	}
}

func TestPaletteGoHomeMaterializesHomeFromDockScope(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	env := testPaletteEnv()
	env.Engine = eng
	env.Scope = palette.ScopeInDock
	env.Ctx = &engine.Context{Dock: "labs"}

	entry := findEntry(t, buildPaletteEntries(env, palette.ModePane), "go-home")
	if _, err := entry.Action(); err != nil {
		t.Fatalf("go-home action: %v", err)
	}

	home, err := eng.BayShow("labs", manifest.HomeBayID)
	if err != nil {
		t.Fatalf("BayShow(home): %v", err)
	}
	if home.Type != manifest.BayTypeHome || len(home.Surfaces) != 1 {
		t.Fatalf("home = %+v, want one materialized home surface", home)
	}
}

func TestPaletteHomeSurfaceActionsTargetHomeNotCurrentBay(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	eng.Config.DefaultEditor = "vim"
	current, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	env := testPaletteEnv()
	env.Engine = eng
	env.Ctx = &engine.Context{
		Dock:    "labs",
		BayID:   current.ID,
		Bay:     current.Name,
		Surface: "shell",
	}

	entries := buildPaletteEntries(env, palette.ModePane)
	for _, id := range []string{"home-shell", "home-editor", "home-agent"} {
		entry := findEntry(t, entries, id)
		if _, err := entry.Action(); err != nil {
			t.Fatalf("%s action: %v", id, err)
		}
	}

	gotCurrent, err := eng.BayShow("labs", current.ID)
	if err != nil {
		t.Fatalf("BayShow(current): %v", err)
	}
	if len(gotCurrent.Surfaces) != 1 {
		t.Fatalf("current bay surfaces = %+v, want unchanged single surface", gotCurrent.Surfaces)
	}

	home, err := eng.BayShow("labs", manifest.HomeBayID)
	if err != nil {
		t.Fatalf("BayShow(home): %v", err)
	}
	if len(home.Surfaces) != 3 {
		t.Fatalf("home surfaces = %+v, want shell/editor/agent", home.Surfaces)
	}
	types := map[manifest.SurfaceType]bool{}
	agents := map[string]bool{}
	for _, s := range home.Surfaces {
		types[s.Type] = true
		if s.Agent != nil {
			agents[*s.Agent] = true
		}
	}
	for _, typ := range []manifest.SurfaceType{
		manifest.SurfaceTypeShell,
		manifest.SurfaceTypeEditor,
		manifest.SurfaceTypeAgent,
	} {
		if !types[typ] {
			t.Fatalf("home surfaces missing type %s: %+v", typ, home.Surfaces)
		}
	}
	if !agents["claude"] {
		t.Fatalf("home agent surface = %v, want dock default claude", agents)
	}
}

func TestPaletteHomeAgentPickCreatesConfiguredAgentInHome(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	eng.Config.Agents["local"] = config.AgentConfig{Command: "local-agent"}
	env := testPaletteEnv()
	env.Engine = eng
	env.Scope = palette.ScopeInDock
	env.Ctx = &engine.Context{Dock: "labs"}

	entry := findEntry(t, buildPaletteEntries(env, palette.ModePane), "home-agent-pick")
	if entry.ParamValid == nil || !entry.ParamValid("local") {
		t.Fatal("home-agent-pick should validate configured agent local")
	}
	if entry.ParamValid("retired") {
		t.Fatal("home-agent-pick accepted unknown agent retired")
	}
	param, err := entry.ActionWithParam("local")
	if err != nil {
		t.Fatalf("home-agent-pick ActionWithParam(local): %v", err)
	}
	if param != "local" {
		t.Fatalf("ActionWithParam returned param %q; want local", param)
	}
	if _, err := entry.ActionWithParam("retired"); err == nil || !strings.Contains(err.Error(), `unknown agent "retired"`) {
		t.Fatalf("ActionWithParam(retired) err = %v; want unknown-agent error", err)
	}

	home, err := eng.BayShow("labs", manifest.HomeBayID)
	if err != nil {
		t.Fatalf("BayShow(home): %v", err)
	}
	if len(home.Surfaces) != 1 || home.Surfaces[0].Agent == nil || *home.Surfaces[0].Agent != "local" {
		t.Fatalf("home surfaces = %+v, want one local agent", home.Surfaces)
	}
}

func TestPaletteCloseSurface_LastHomeUsesDoubleTapConfirmation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	eng, mockTmux, _, _ := testNavEngine(t)
	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	winID := home.Surfaces[0].Tmux.WindowID

	env := testPaletteEnv()
	env.Engine = eng
	env.Ctx = &engine.Context{
		Dock:    "labs",
		BayID:   manifest.HomeBayID,
		Bay:     manifest.HomeBayID,
		Surface: "shell",
	}
	entry := findEntry(t, buildPaletteEntries(env, palette.ModeWindow), "close-surface")

	if _, err := entry.Action(); err != nil {
		t.Fatalf("close-surface first: %v", err)
	}
	if exists, _ := mockTmux.WindowExists(winID); !exists {
		t.Fatalf("first palette close killed home window %s", winID)
	}
	msgs := mockTmux.DisplayMessages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "dismiss dock") {
		t.Fatalf("first palette close messages = %v, want dismissal guidance", msgs)
	}

	if _, err := entry.Action(); err != nil {
		t.Fatalf("close-surface second: %v", err)
	}
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("tmux session still exists after confirmed palette close")
	}
	m2, _ := eng.LoadManifest()
	if home := m2.FindDock("labs").FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("home persisted after confirmed palette close: %+v", home)
	}
}

func TestPaletteCloseBay_LastHomeUsesDoubleTapConfirmation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	eng, mockTmux, _, _ := testNavEngine(t)
	if err := eng.Home("labs"); err != nil {
		t.Fatalf("Home: %v", err)
	}
	m, _ := eng.LoadManifest()
	home := m.FindDock("labs").FindBayByID(manifest.HomeBayID)
	winID := home.Surfaces[0].Tmux.WindowID

	env := testPaletteEnv()
	env.Engine = eng
	env.Ctx = &engine.Context{
		Dock:  "labs",
		BayID: manifest.HomeBayID,
		Bay:   manifest.HomeBayID,
	}
	entry := findEntry(t, buildPaletteEntries(env, palette.ModeWindow), "close-bay")

	if _, err := entry.Action(); err != nil {
		t.Fatalf("close-bay first: %v", err)
	}
	if exists, _ := mockTmux.WindowExists(winID); !exists {
		t.Fatalf("first palette close-bay killed home window %s", winID)
	}
	msgs := mockTmux.DisplayMessages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "dismiss dock") {
		t.Fatalf("first palette close-bay messages = %v, want dismissal guidance", msgs)
	}

	if _, err := entry.Action(); err != nil {
		t.Fatalf("close-bay second: %v", err)
	}
	if has, _ := mockTmux.HasSession("labs"); has {
		t.Fatal("tmux session still exists after confirmed palette close-bay")
	}
	m2, _ := eng.LoadManifest()
	if home := m2.FindDock("labs").FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("home persisted after confirmed palette close-bay: %+v", home)
	}
}

// testPaletteEnv returns a minimal env suitable for exercising the
// build-entry logic. Engine is non-nil (nil would NPE in closures) but
// actions aren't invoked in these tests — we only check metadata.
func testPaletteEnv() *paletteEnv {
	return &paletteEnv{
		Engine:  &engine.Engine{},
		Ctx:     &engine.Context{Dock: "testdock", Bay: "testbay", Surface: "testsf"},
		Scope:   palette.ScopeInBay,
		Recents: palette.LoadRecents(""),
		Hotkeys: fakeHotkeys(nil),
	}
}

// fakeHotkeys builds a palette.Hotkeys directly from a lookup map,
// bypassing the tmux list-keys subprocess.
func fakeHotkeys(m map[string]string) *palette.Hotkeys {
	// Synthesize a tmux list-keys block so the parser sees what we want.
	// Tests that don't care about hotkeys can pass nil → empty block.
	var b strings.Builder
	for cmd, key := range m {
		b.WriteString("bind-key -T root ")
		b.WriteString(key)
		b.WriteString(" run-shell '")
		b.WriteString(cmd)
		b.WriteString(" || true'\n")
	}
	return palette.ParseHotkeysForTest(b.String())
}

func findEntry(t *testing.T, entries []palette.Entry, id string) palette.Entry {
	t.Helper()
	for _, e := range entries {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("entry %q not found", id)
	return palette.Entry{}
}
