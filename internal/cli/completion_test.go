package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/spf13/cobra"
)

func TestFindCmd(t *testing.T) {
	root := NewRootCmd("test")

	tests := []struct {
		path    string
		wantNil bool
	}{
		{"new", false},
		{"close", false},
		{"show", false},
		{"dock", false},
		{"dock new", false},
		{"go", false},
		{"nonexistent", true},
		{"surface nonexistent", true},
	}
	for _, tt := range tests {
		cmd := findCmd(root, tt.path)
		if (cmd == nil) != tt.wantNil {
			t.Errorf("findCmd(%q) nil=%v, want nil=%v", tt.path, cmd == nil, tt.wantNil)
		}
	}
}

func TestGoCmd_HasNextWaitingFlag(t *testing.T) {
	root := NewRootCmd("test")
	cmd := findCmd(root, "go")
	if cmd == nil {
		t.Fatal("go command not found")
	}
	f := cmd.Flags().Lookup("next-waiting")
	if f == nil {
		t.Error("bay go missing --next-waiting flag")
	}
}

func TestSurfaceGoCmd_HasNextWaitingFlag(t *testing.T) {
	root := NewRootCmd("test")
	cmd := findCmd(root, "surface go")
	if cmd == nil {
		t.Fatal("surface go command not found")
	}
	f := cmd.Flags().Lookup("next-waiting")
	if f == nil {
		t.Error("bay surface go missing --next-waiting flag")
	}
}

func TestCompletionsRegistered(t *testing.T) {
	root := NewRootCmd("test")

	// These commands should have ValidArgsFunction set on their
	// positional. Commands whose positional is a free-form name (the
	// create-verbs bay new / surface new / shell) intentionally have
	// no ValidArgsFunction — see TestCreateVerbs_PositionalsAreFreeText.
	withCompletions := []string{
		"close", "show", "rename",
		"go",
		"dock close", "dock init", "dock recover", "dock tree",
		// surface verbs (sf X form) — close/show/rename target
		// existing surfaces by name.
		"surface close", "surface show", "surface rename",
		// surface new subcommands with positional completions
		"surface new agent", "surface new edit",
	}
	for _, path := range withCompletions {
		cmd := findCmd(root, path)
		if cmd == nil {
			t.Errorf("command %q not found", path)
			continue
		}
		if cmd.ValidArgsFunction == nil {
			t.Errorf("command %q has no ValidArgsFunction", path)
		}
	}
}

// TestCreateVerbs_PositionalsAreFreeText pins the contract that
// bay new / shell have NO positional completion. Their positional
// names a brand-new thing the user is about to create — completing
// it from existing names would be misleading.
func TestCreateVerbs_PositionalsAreFreeText(t *testing.T) {
	root := NewRootCmd("test")
	for _, path := range []string{"new", "shell"} {
		cmd := findCmd(root, path)
		if cmd == nil {
			t.Errorf("command %q not found", path)
			continue
		}
		if cmd.ValidArgsFunction != nil {
			t.Errorf("command %q has a positional ValidArgsFunction; create-verb positionals must be free text", path)
		}
	}
}

func TestSurfaceCommands_UseSurfaceCompletions(t *testing.T) {
	// surface close/show/rename
	// must use sfCompl, not bayCompl. Set up a manifest with one bay and
	// one surface, and verify the completer returns the surface name.
	dir := t.TempDir()

	m := manifest.New()
	m.Docks = []manifest.Dock{
		{
			Name: "labs",
			Bays: []manifest.Bay{
				{
					Name: "b1",
					Surfaces: []manifest.Surface{
						{Name: "agent", Type: manifest.SurfaceTypeAgent},
					},
				},
			},
		},
	}

	origXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", dir)
	defer os.Setenv("XDG_DATA_HOME", origXDG)
	bayDir := filepath.Join(dir, "bay")
	os.MkdirAll(bayDir, 0o755)
	manifest.Save(filepath.Join(bayDir, "manifest.json"), m)

	root := NewRootCmd("test")

	for _, path := range []string{"surface close", "surface show", "surface rename"} {
		cmd := findCmd(root, path)
		if cmd == nil || cmd.ValidArgsFunction == nil {
			t.Errorf("command %q has no ValidArgsFunction", path)
			continue
		}
		completions, _ := cmd.ValidArgsFunction(cmd, nil, "")
		// Surface completer emits qualified forms only — verify the agent
		// surface appears as b1:agent and labs:b1:agent.
		hasAgent := false
		for _, c := range completions {
			if strings.HasPrefix(c, "b1:agent\t") || strings.HasPrefix(c, "labs:b1:agent\t") {
				hasAgent = true
				break
			}
		}
		if !hasAgent {
			t.Errorf("command %q completions missing 'agent' surface, got: %v", path, completions)
		}
	}
}

func TestSurfaceCompletions(t *testing.T) {
	dir := t.TempDir()

	m := manifest.New()
	m.Docks = []manifest.Dock{
		{
			Name: "labs",
			Bays: []manifest.Bay{
				{
					Name: "b1",
					Surfaces: []manifest.Surface{
						{Name: "agent", Type: manifest.SurfaceTypeAgent},
						{Name: "shell", Type: manifest.SurfaceTypeShell},
					},
				},
			},
		},
	}

	origXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", dir)
	defer os.Setenv("XDG_DATA_HOME", origXDG)
	bayDir := filepath.Join(dir, "bay")
	os.MkdirAll(bayDir, 0o755)
	manifest.Save(filepath.Join(bayDir, "manifest.json"), m)

	fn := surfaceCompletions()
	completions, directive := fn(nil, nil, "")

	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %d, want NoFileComp", directive)
	}

	hasValue := func(prefix string) bool {
		for _, c := range completions {
			if strings.HasPrefix(c, prefix) {
				return true
			}
		}
		return false
	}

	// Both qualified forms for each surface, plus the self keyword. Bare
	// names ("agent", "shell") are deliberately omitted — they collide
	// across bays and would mislead users.
	for _, expected := range []string{
		"self",
		"b1:agent", "b1:shell",
		"labs:b1:agent", "labs:b1:shell",
	} {
		if !hasValue(expected) {
			t.Errorf("surfaceCompletions missing %q, got: %v", expected, completions)
		}
	}

	// Bare surface names must NOT appear — verify by checking that no entry
	// is exactly "agent" or "shell" (no colon prefix) or starts with
	// "agent\t" / "shell\t".
	for _, c := range completions {
		if c == "agent" || c == "shell" ||
			strings.HasPrefix(c, "agent\t") || strings.HasPrefix(c, "shell\t") {
			t.Errorf("surfaceCompletions should not include bare name %q", c)
		}
	}
}

func TestSurfaceCompletions_NoSelfAfterColon(t *testing.T) {
	// When the user has typed a colon, 'self' is not a meaningful completion
	// (qualified self is always literal, not a keyword).
	dir := t.TempDir()
	m := manifest.New()
	m.Docks = []manifest.Dock{{Name: "labs", Bays: []manifest.Bay{{Name: "b1"}}}}

	origXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", dir)
	defer os.Setenv("XDG_DATA_HOME", origXDG)
	bayDir := filepath.Join(dir, "bay")
	os.MkdirAll(bayDir, 0o755)
	manifest.Save(filepath.Join(bayDir, "manifest.json"), m)

	fn := surfaceCompletions()
	completions, _ := fn(nil, nil, "b1:")

	for _, c := range completions {
		if strings.HasPrefix(c, "self\t") || c == "self" {
			t.Errorf("surfaceCompletions should not include 'self' after a colon, got: %v", completions)
		}
	}
}

func TestSurfaceCompletions_SecondArgReturnsNone(t *testing.T) {
	fn := surfaceCompletions()
	completions, _ := fn(nil, []string{"agent"}, "")
	if len(completions) != 0 {
		t.Errorf("expected no completions for second arg, got %d", len(completions))
	}
}

func TestBayFlagCompletions(t *testing.T) {
	dir := t.TempDir()
	m := manifest.New()
	m.Docks = []manifest.Dock{
		{Name: "labs", Bays: []manifest.Bay{{Name: "b1"}, {Name: "b2"}}},
	}
	origXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", dir)
	defer os.Setenv("XDG_DATA_HOME", origXDG)
	bayDir := filepath.Join(dir, "bay")
	os.MkdirAll(bayDir, 0o755)
	manifest.Save(filepath.Join(bayDir, "manifest.json"), m)

	fn := bayFlagCompletions()
	// Pass non-empty args — flag completers must NOT short-circuit on args.
	completions, directive := fn(nil, []string{"some-positional"}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %d, want NoFileComp", directive)
	}
	if len(completions) == 0 {
		t.Error("bayFlagCompletions returned no completions even with non-empty args")
	}
	// Should contain b1 and b2.
	hasValue := func(prefix string) bool {
		for _, c := range completions {
			if strings.HasPrefix(c, prefix) {
				return true
			}
		}
		return false
	}
	for _, expected := range []string{"b1", "b2", "labs:b1", "labs:b2"} {
		if !hasValue(expected) {
			t.Errorf("bay flag completions missing %q, got: %v", expected, completions)
		}
	}
}

func TestDockFlagCompletions(t *testing.T) {
	dir := t.TempDir()
	m := manifest.New()
	m.Docks = []manifest.Dock{
		{Name: "labs", Path: "/p/labs"},
		{Name: "research"},
	}
	origXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", dir)
	defer os.Setenv("XDG_DATA_HOME", origXDG)
	bayDir := filepath.Join(dir, "bay")
	os.MkdirAll(bayDir, 0o755)
	manifest.Save(filepath.Join(bayDir, "manifest.json"), m)

	fn := dockFlagCompletions()
	// Pass non-empty args — flag completers must NOT short-circuit on args.
	completions, _ := fn(nil, []string{"some-positional"}, "")
	if len(completions) != 2 {
		t.Errorf("expected 2 dock completions, got %d: %v", len(completions), completions)
	}
}

func TestFlagCompletions_BayAndDockOnSurfaceVerbs(t *testing.T) {
	// Verify --bay and --dock have completion functions registered on
	// the surface verb commands. surface new and shell are in this
	// list now that their positional is the surface name and the
	// bay target moved to --bay.
	root := NewRootCmd("test")
	for _, path := range []string{
		"surface close", "surface show", "surface rename",
		"shell",
		"surface new shell", "surface new agent", "surface new cmd",
	} {
		cmd := findCmd(root, path)
		if cmd == nil {
			t.Errorf("command %q not found", path)
			continue
		}
		// Cobra exposes flag completions via its internal map. We can't
		// inspect them directly, so just verify the flags exist (a missing
		// flag would mean the wiring loop silently no-op'd on this command).
		if cmd.Flags().Lookup("bay") == nil {
			t.Errorf("command %q missing --bay flag", path)
		}
		if cmd.Flags().Lookup("dock") == nil {
			t.Errorf("command %q missing --dock flag", path)
		}
	}
}

// TestFlagCompletions_DockOnBayNew pins that bay new exposes a
// --dock flag (the replacement for the old positional). The flag
// completion is registered against the same dockFlagCompletions
// helper used elsewhere — there's no public way to inspect cobra's
// flag-completion map, so this test only verifies the flag exists.
func TestFlagCompletions_DockOnBayNew(t *testing.T) {
	root := NewRootCmd("test")
	cmd := findCmd(root, "new")
	if cmd == nil {
		t.Fatal("bay new command not found")
	}
	if cmd.Flags().Lookup("dock") == nil {
		t.Error("bay new missing --dock flag")
	}
}

func TestBayCompletions(t *testing.T) {
	// Set up a manifest with test data in a temp dir
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")

	m := manifest.New()
	m.Docks = []manifest.Dock{
		{
			Name: "labs",
			Bays: []manifest.Bay{
				{
					Name:     "mem-refactor",
					Worktree: &manifest.WorktreeAttrs{Repo: "labs", Branch: "feature/refactor-memory", PR: "234"},
				},
				{
					Name: "b2",
				},
			},
		},
	}
	manifest.Save(manifestPath, m)

	// Override the default data dir via env
	origXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", dir)
	defer os.Setenv("XDG_DATA_HOME", origXDG)

	// Copy to bay/manifest.json (where DefaultPaths looks)
	bayDir := filepath.Join(dir, "bay")
	os.MkdirAll(bayDir, 0o755)
	data, _ := os.ReadFile(manifestPath)
	os.WriteFile(filepath.Join(bayDir, "manifest.json"), data, 0o644)

	fn := bayCompletions()
	completions, directive := fn(nil, nil, "")

	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %d, want ShellCompDirectiveNoFileComp(%d)", directive, cobra.ShellCompDirectiveNoFileComp)
	}

	// Should contain bay names, qualified names, and "self"
	hasValue := func(prefix string) bool {
		for _, c := range completions {
			if strings.HasPrefix(c, prefix) {
				return true
			}
		}
		return false
	}

	// Strict resolver: only IDs (and dock:ID) are completion values;
	// Names appear only in descriptions.
	for _, expected := range []string{"b1", "b2", "self", "labs:b1", "labs:b2"} {
		if !hasValue(expected) {
			t.Errorf("completions missing %q, got: %v", expected, completions)
		}
	}
	// Names like "mem-refactor" must NOT be candidate values.
	for _, notExpected := range []string{"mem-refactor\t", "labs:mem-refactor"} {
		if hasValue(notExpected) {
			t.Errorf("Name %q should not be a candidate value under strict resolution; got: %v", notExpected, completions)
		}
	}

	// Branch/PR should NOT be in bay completions.
	for _, notExpected := range []string{"feature/refactor-memory", "#234"} {
		if hasValue(notExpected) {
			t.Errorf("bay completions should not include %q (only in goCompletions)", notExpected)
		}
	}
}

func TestDockCompletions(t *testing.T) {
	dir := t.TempDir()

	// Write a manifest file with docks
	m := manifest.New()
	m.Docks = []manifest.Dock{
		{Name: "dev", Path: "/p/labs", Agent: "claude", Bays: []manifest.Bay{}},
		{Name: "research", Agent: "claude", Bays: []manifest.Bay{}},
	}

	origXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", dir)
	defer os.Setenv("XDG_DATA_HOME", origXDG)

	bayDir := filepath.Join(dir, "bay")
	os.MkdirAll(bayDir, 0o755)
	manifest.Save(filepath.Join(bayDir, "manifest.json"), m)

	fn := dockCompletions()
	completions, _ := fn(nil, nil, "")

	if len(completions) != 2 {
		t.Errorf("expected 2 completions, got %d: %v", len(completions), completions)
	}

	hasValue := func(prefix string) bool {
		for _, c := range completions {
			if strings.HasPrefix(c, prefix) {
				return true
			}
		}
		return false
	}
	if !hasValue("dev") {
		t.Errorf("completions missing 'dev', got: %v", completions)
	}
	if !hasValue("research") {
		t.Errorf("completions missing 'research', got: %v", completions)
	}
}

func TestSplitCompletions(t *testing.T) {
	completions, _ := splitCompletions(nil, nil, "")
	if len(completions) != 2 {
		t.Errorf("expected 2 split completions, got %d", len(completions))
	}
}

// TestAgentCandidates_IncludesBuiltinsProfilesAndConfig pins the fix for
// empty `bay agent <TAB>`: built-in clients and model profiles must be
// offered even with no [agents.*] config, configured agents appear with their
// command, profiles are labeled with their base client, and a disabled agent
// is excluded.
func TestAgentCandidates_IncludesBuiltinsProfilesAndConfig(t *testing.T) {
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"my-agent": {Command: "my-cli"},
			"codex":    {Disabled: true}, // turned off — must not be offered
		},
	}

	got := agentCandidates(cfg)
	desc := map[string]string{}
	count := map[string]int{}
	for _, c := range got {
		val, d, _ := strings.Cut(c, "\t")
		desc[val] = d
		count[val]++
	}

	// Built-in clients are offered without any config entry.
	for _, name := range []string{"claude", "antigravity"} {
		if _, ok := desc[name]; !ok {
			t.Errorf("built-in client %q missing from completions: %v", name, got)
		}
	}
	// Built-in model profiles are offered and labeled with their base client.
	if desc["fable"] != "fable (claude)" {
		t.Errorf("fable desc = %q, want %q", desc["fable"], "fable (claude)")
	}
	if _, ok := desc["opus"]; !ok {
		t.Errorf("built-in profile opus missing from completions: %v", got)
	}
	// A configured custom agent shows its command.
	if desc["my-agent"] != "my-cli" {
		t.Errorf("my-agent desc = %q, want my-cli", desc["my-agent"])
	}
	// A disabled agent is not offered at all.
	if _, ok := desc["codex"]; ok {
		t.Errorf("disabled agent codex should not be offered: %v", got)
	}
	// No duplicate candidates.
	for name, n := range count {
		if n != 1 {
			t.Errorf("agent %q appears %d times; want 1", name, n)
		}
	}
}

// TestAgentCandidates_NilConfigDoesNotPanic guards the completion path when
// config can't be loaded; built-ins should still be offered.
func TestAgentCandidates_NilConfigDoesNotPanic(t *testing.T) {
	got := agentCandidates(nil)
	if len(got) == 0 {
		t.Error("nil config: expected built-in candidates, got none")
	}
}

// TestBayCandidates_EmitsIDAndName covers the Phase 4 behavior: each
// bay contributes both ID and Name forms (plus dock-qualified versions),
// so users can complete from either prefix. Descriptions cross-reference the
// other identifier.
func TestBayCandidates_EmitsIDAndName(t *testing.T) {
	m := manifest.New()
	m.Docks = []manifest.Dock{{
		Name: "labs",
		Bays: []manifest.Bay{{
			ID:       "b1",
			Name:     "auth-fix",
			Worktree: &manifest.WorktreeAttrs{Repo: "labs", Branch: "fix/auth"},
		}},
	}}

	got := bayCandidates(m)

	// Under strict resolution, only IDs (and dock:IDs) are candidate
	// values. The friendly Name is woven into descriptions.
	want := map[string]string{
		"b1":      "labs auth-fix fix/auth",
		"labs:b1": "auth-fix fix/auth",
	}
	gotMap := map[string]string{}
	for _, c := range got {
		val, desc, _ := strings.Cut(c, "\t")
		gotMap[val] = desc
	}
	for val, desc := range want {
		if gotMap[val] != desc {
			t.Errorf("candidate %q: desc = %q, want %q", val, gotMap[val], desc)
		}
	}
	// Names should not appear as candidate values.
	for _, name := range []string{"auth-fix", "labs:auth-fix"} {
		if _, present := gotMap[name]; present {
			t.Errorf("Name %q should not be emitted as a candidate value", name)
		}
	}
}

// TestBayCandidates_DedupesWhenIDMatchesName covers the auto-named case
// where the bay's ID and Name are identical (e.g. b1) — only one
// candidate per form should appear, not two duplicates.
func TestBayCandidates_DedupesWhenIDMatchesName(t *testing.T) {
	m := manifest.New()
	m.Docks = []manifest.Dock{{
		Name: "labs",
		Bays: []manifest.Bay{{
			ID:   "b1",
			Name: "b1",
		}},
	}}
	got := bayCandidates(m)
	count := 0
	for _, c := range got {
		val, _, _ := strings.Cut(c, "\t")
		if val == "b1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one b1 candidate (deduped); got %d in %v", count, got)
	}
}

func TestBayCandidates_HomeDoesNotExposeWorktreeMetadata(t *testing.T) {
	m := manifest.New()
	m.Docks = []manifest.Dock{{
		Name: "labs",
		Bays: []manifest.Bay{{
			ID:       manifest.HomeBayID,
			Name:     manifest.HomeBayID,
			Type:     manifest.BayTypeHome,
			Worktree: &manifest.WorktreeAttrs{Branch: "feature/not-home", PR: "99"},
		}},
	}}

	got := bayCandidates(m)
	gotMap := map[string]string{}
	for _, c := range got {
		val, desc, _ := strings.Cut(c, "\t")
		gotMap[val] = desc
	}
	if gotMap[manifest.HomeBayID] != "labs home" {
		t.Fatalf("home candidate desc = %q, want dock/name only; all=%v", gotMap[manifest.HomeBayID], got)
	}
	if strings.Contains(strings.Join(got, "\n"), "feature/not-home") || strings.Contains(strings.Join(got, "\n"), "#99") {
		t.Fatalf("home completion exposed worktree metadata: %v", got)
	}
}

// TestBayCandidates_HandlesEmptyName covers bays without a Name
// (sticky-once-set hasn't fired yet): only ID candidates are emitted, no
// empty-string Name candidates.
func TestBayCandidates_HandlesEmptyName(t *testing.T) {
	m := manifest.New()
	m.Docks = []manifest.Dock{{
		Name: "labs",
		Bays: []manifest.Bay{{
			ID:   "b7",
			Name: "",
		}},
	}}
	got := bayCandidates(m)
	hasID := false
	for _, c := range got {
		val, _, _ := strings.Cut(c, "\t")
		if val == "b7" {
			hasID = true
		}
		if val == "" {
			t.Errorf("empty-Name bay should not produce empty candidate; got %v", got)
		}
	}
	if !hasID {
		t.Errorf("expected b7 candidate from empty-Name bay; got %v", got)
	}
}

func TestBayCompletions_SecondArgReturnsNone(t *testing.T) {
	fn := bayCompletions()
	completions, _ := fn(nil, []string{"b1"}, "")
	if len(completions) != 0 {
		t.Errorf("expected no completions for second arg, got %d", len(completions))
	}
}
