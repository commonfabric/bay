package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
)

// twoDockFixture extends testNavEngine with a second checkout and dock
// "labs2" so tests can exercise cross-dock ambiguity and dock-qualified names.
func twoDockFixture(t *testing.T) *engine.Engine {
	t.Helper()
	eng, _, _, dir := testNavEngine(t)

	labs2Dir := filepath.Join(dir, "repos", "labs2")
	if err := os.MkdirAll(filepath.Join(labs2Dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir labs2 checkout: %v", err)
	}
	if err := eng.DockNew("labs2", labs2Dir, "", "claude", ""); err != nil {
		t.Fatalf("DockNew labs2: %v", err)
	}
	return eng
}

// --- parseSurfaceArg ---

func TestParseSurfaceArg(t *testing.T) {
	cases := []struct {
		in                 string
		dock, bay, surface string
		wantErr            bool
	}{
		{"agent", "", "", "agent", false},
		{"b1:agent", "", "b1", "agent", false},
		{"labs:b1:agent", "labs", "b1", "agent", false},
		{"a:b:c:d", "", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			dock, bay, surface, err := parseSurfaceArg(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if dock != c.dock || bay != c.bay || surface != c.surface {
				t.Errorf("got (%q,%q,%q), want (%q,%q,%q)",
					dock, bay, surface, c.dock, c.bay, c.surface)
			}
		})
	}
}

// --- parseBayArg ---

func TestParseBayArg(t *testing.T) {
	cases := []struct {
		in        string
		dock, bay string
		wantErr   bool
	}{
		{"b1", "", "b1", false},
		{"labs:b1", "labs", "b1", false},
		{"a:b:c", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			dock, bay, err := parseBayArg(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if dock != c.dock || bay != c.bay {
				t.Errorf("got (%q,%q), want (%q,%q)", dock, bay, c.dock, c.bay)
			}
		})
	}
}

// --- resolveSurfaceArg ---

// surfaceArgFixture creates two docks each with a bay and an "agent"
// surface, so we can test bare/qualified resolution and conflicts.
func surfaceArgFixture(t *testing.T) *engine.Engine {
	t.Helper()
	eng := twoDockFixture(t)

	// labs:b1 with default shell + extra agent surface
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew labs:b1: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeAgent, Name: "agent", Agent: "claude", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd labs:b1:agent: %v", err)
	}

	// labs2:b1 with the same surface — bare bay name "b1" is now ambiguous.
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs2", Shell: true}); err != nil {
		t.Fatalf("BayNew labs2:b1: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs2", BayName: "b1", Type: manifest.SurfaceTypeAgent, Name: "agent", Agent: "claude", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd labs2:b1:agent: %v", err)
	}

	// Also create a uniquely-named bay so bare resolution works.
	// Bay's ID is b2 (auto-assigned, since solo is the second labs bay).
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Name: "solo", Shell: true}); err != nil {
		t.Fatalf("BayNew labs:solo: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b2", Type: manifest.SurfaceTypeAgent, Name: "agent", Agent: "claude", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd labs:b2:agent: %v", err)
	}

	return eng
}

func TestResolveSurfaceArg_FullyQualified(t *testing.T) {
	eng := surfaceArgFixture(t)

	dock, bay, surface, err := resolveSurfaceArg(eng, "labs:b1:agent", "", "")
	if err != nil {
		t.Fatalf("resolveSurfaceArg: %v", err)
	}
	if dock != "labs" || bay != "b1" || surface != "agent" {
		t.Errorf("got (%q,%q,%q), want (labs,b1,agent)", dock, bay, surface)
	}
}

func TestResolveSurfaceArg_BayQualified(t *testing.T) {
	eng := surfaceArgFixture(t)

	// b2 is uniquely owned by labs, so bare ID lookup succeeds.
	dock, bay, surface, err := resolveSurfaceArg(eng, "b2:agent", "", "")
	if err != nil {
		t.Fatalf("resolveSurfaceArg: %v", err)
	}
	if dock != "labs" || bay != "b2" || surface != "agent" {
		t.Errorf("got (%q,%q,%q), want (labs,b2,agent)", dock, bay, surface)
	}
}

func TestResolveSurfaceArg_AmbiguousBay(t *testing.T) {
	eng := surfaceArgFixture(t)

	// "b1" exists in both labs and labs2.
	_, _, _, err := resolveSurfaceArg(eng, "b1:agent", "", "")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected ambiguous error, got %v", err)
	}
}

func TestResolveSurfaceArg_FlagsResolveAmbiguity(t *testing.T) {
	eng := surfaceArgFixture(t)

	// Use --dock to disambiguate the bare "b1".
	dock, bay, surface, err := resolveSurfaceArg(eng, "b1:agent", "", "labs2")
	if err != nil {
		t.Fatalf("resolveSurfaceArg: %v", err)
	}
	if dock != "labs2" || bay != "b1" || surface != "agent" {
		t.Errorf("got (%q,%q,%q), want (labs2,b1,agent)", dock, bay, surface)
	}
}

func TestResolveSurfaceArg_FlagsAlone(t *testing.T) {
	eng := surfaceArgFixture(t)

	// Bare surface name + --bay + --dock.
	dock, bay, surface, err := resolveSurfaceArg(eng, "agent", "b1", "labs2")
	if err != nil {
		t.Fatalf("resolveSurfaceArg: %v", err)
	}
	if dock != "labs2" || bay != "b1" || surface != "agent" {
		t.Errorf("got (%q,%q,%q), want (labs2,b1,agent)", dock, bay, surface)
	}
}

func TestResolveSurfaceArg_DockFlagConflict(t *testing.T) {
	eng := surfaceArgFixture(t)

	// --dock conflicts with dock prefix in the positional.
	_, _, _, err := resolveSurfaceArg(eng, "labs:b1:agent", "", "labs2")
	if err == nil || !strings.Contains(err.Error(), "--dock") {
		t.Errorf("expected --dock conflict, got %v", err)
	}
}

func TestResolveSurfaceArg_BayFlagConflict(t *testing.T) {
	eng := surfaceArgFixture(t)

	// --bay conflicts with bay prefix in the positional.
	_, _, _, err := resolveSurfaceArg(eng, "b1:agent", "other", "")
	if err == nil || !strings.Contains(err.Error(), "--bay") {
		t.Errorf("expected --bay conflict, got %v", err)
	}
}

func TestResolveSurfaceArg_DockFlagWithoutBay(t *testing.T) {
	eng := surfaceArgFixture(t)

	// --dock without --bay or bay prefix is meaningless.
	_, _, _, err := resolveSurfaceArg(eng, "agent", "", "labs")
	if err == nil {
		t.Errorf("expected error when --dock has no bay context")
	}
}

func TestResolveSurfaceArg_TooManyParts(t *testing.T) {
	eng := surfaceArgFixture(t)

	_, _, _, err := resolveSurfaceArg(eng, "a:b:c:d", "", "")
	if err == nil {
		t.Errorf("expected parse error for 4-part input")
	}
}

// --- resolveBayArg ---

func bayArgFixture(t *testing.T) *engine.Engine {
	t.Helper()
	eng := twoDockFixture(t)

	// Ambiguous bare bay name.
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew labs:b1: %v", err)
	}
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs2", Shell: true}); err != nil {
		t.Fatalf("BayNew labs2:b1: %v", err)
	}

	// Unique bay name for bare resolution.
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Name: "solo", Shell: true}); err != nil {
		t.Fatalf("BayNew labs:solo: %v", err)
	}

	return eng
}

func TestResolveBayArg_BareUnique(t *testing.T) {
	eng := bayArgFixture(t)

	// b2 is unique to labs (labs has ID b1 and b2; labs2 has only b1).
	dock, bay, err := resolveBayArg(eng, "b2", "")
	if err != nil {
		t.Fatalf("resolveBayArg: %v", err)
	}
	if dock != "labs" || bay != "b2" {
		t.Errorf("got (%q,%q), want (labs,b2)", dock, bay)
	}
}

func TestResolveBayArg_DockQualified(t *testing.T) {
	eng := bayArgFixture(t)

	dock, bay, err := resolveBayArg(eng, "labs2:b1", "")
	if err != nil {
		t.Fatalf("resolveBayArg: %v", err)
	}
	if dock != "labs2" || bay != "b1" {
		t.Errorf("got (%q,%q), want (labs2,b1)", dock, bay)
	}
}

func TestResolveBayArg_BareAmbiguous(t *testing.T) {
	eng := bayArgFixture(t)

	_, _, err := resolveBayArg(eng, "b1", "")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected ambiguous error, got %v", err)
	}
}

func TestResolveBayArg_DockFlagDisambiguates(t *testing.T) {
	eng := bayArgFixture(t)

	dock, bay, err := resolveBayArg(eng, "b1", "labs2")
	if err != nil {
		t.Fatalf("resolveBayArg: %v", err)
	}
	if dock != "labs2" || bay != "b1" {
		t.Errorf("got (%q,%q), want (labs2,b1)", dock, bay)
	}
}

func TestResolveBayArg_DockFlagConflict(t *testing.T) {
	eng := bayArgFixture(t)

	_, _, err := resolveBayArg(eng, "labs:b1", "labs2")
	if err == nil || !strings.Contains(err.Error(), "--dock") {
		t.Errorf("expected --dock conflict, got %v", err)
	}
}

// chdirTo enters the given bay path so CurrentContext resolves to
// its dock. Restores cwd on test cleanup.
func chdirTo(t *testing.T, eng *engine.Engine, dockName, bayName string) {
	t.Helper()
	bay, err := eng.BayShow(dockName, bayName)
	if err != nil {
		t.Fatalf("BayShow %s:%s: %v", dockName, bayName, err)
	}
	if err := os.MkdirAll(bay.Path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", bay.Path, err)
	}
	orig, _ := os.Getwd()
	if err := os.Chdir(bay.Path); err != nil {
		t.Fatalf("Chdir %s: %v", bay.Path, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func TestResolveBayArg_BareAmbiguousPrefersCurrentDock(t *testing.T) {
	// Both labs and labs2 have a bay with ID "b1". From inside
	// labs:b2 (the "solo" bay), a bare `b1` should resolve to labs:b1,
	// not error.
	eng := bayArgFixture(t)
	chdirTo(t, eng, "labs", "b2")

	dock, bay, err := resolveBayArg(eng, "b1", "")
	if err != nil {
		t.Fatalf("resolveBayArg: %v", err)
	}
	if dock != "labs" || bay != "b1" {
		t.Errorf("got (%q,%q), want (labs,b1)", dock, bay)
	}
}

func TestResolveBayArg_BareFallsThroughWhenCurrentDockLacksIt(t *testing.T) {
	// labs2 has no b2. From inside labs2:b1, a bare `b2` should still
	// resolve — the current-dock shortcut falls through to the all-dock
	// search when it misses.
	eng := bayArgFixture(t)
	chdirTo(t, eng, "labs2", "b1")

	dock, bay, err := resolveBayArg(eng, "b2", "")
	if err != nil {
		t.Fatalf("resolveBayArg: %v", err)
	}
	if dock != "labs" || bay != "b2" {
		t.Errorf("got (%q,%q), want (labs,b2)", dock, bay)
	}
}

func TestResolveBayArg_BareAmbiguousWhenCurrentDockLacksIt(t *testing.T) {
	// Without a current-dock context (cwd is outside every bay),
	// a bare `b1` should fall through to the manifest-wide search and
	// surface the cross-dock ambiguity (labs:b1 and labs2:b1).
	eng := bayArgFixture(t)
	// Make sure CurrentContext won't pick up any ambient bay.
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	_, _, err := resolveBayArg(eng, "b1", "")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected ambiguous error, got %v", err)
	}
}

func TestResolveBayArg_BareHomeUsesCurrentDock(t *testing.T) {
	eng := bayArgFixture(t)
	chdirTo(t, eng, "labs", "b2")

	dock, bay, err := resolveBayArg(eng, manifest.HomeBayID, "")
	if err != nil {
		t.Fatalf("resolveBayArg(home): %v", err)
	}
	if dock != "labs" || bay != manifest.HomeBayID {
		t.Fatalf("got (%q, %q), want (labs, home)", dock, bay)
	}
}

func TestResolveBayArg_BareHomeWithoutContextFailsClearly(t *testing.T) {
	eng := bayArgFixture(t)
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	_, _, err := resolveBayArg(eng, manifest.HomeBayID, "")
	if err == nil || !strings.Contains(err.Error(), "dock context") {
		t.Fatalf("resolveBayArg(home) error = %v, want dock-context guidance", err)
	}
}

// --- resolveSurfaceArgOrSelf ---

// selfFixture builds a bay with two surfaces and pins the tmux mock to
// the second one. The "current pane" therefore maps to surface "second".
func selfFixture(t *testing.T) *engine.Engine {
	t.Helper()
	eng, mockTmux, _, _ := testNavEngine(t)

	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeShell, Name: "second", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd second: %v", err)
	}
	bay, _ := eng.BayShow("labs", "b1")
	// Pin tmux context to the "second" surface (index 1).
	mockTmux.SetCurrentWindowID(bay.Surfaces[1].Tmux.WindowID)
	mockTmux.SetCurrentPaneID(bay.Surfaces[1].Tmux.PaneID)

	return eng
}

func TestResolveSurfaceArgOrSelf_VirtualSelf(t *testing.T) {
	eng := selfFixture(t)

	dock, bay, surface, err := resolveSurfaceArgOrSelf(eng, "self", "", "")
	if err != nil {
		t.Fatalf("resolveSurfaceArgOrSelf: %v", err)
	}
	if dock != "labs" || bay != "b1" || surface != "second" {
		t.Errorf("got (%q,%q,%q), want (labs,b1,second)", dock, bay, surface)
	}
}

func TestResolveSurfaceArgOrSelf_LiteralWinsOverVirtual(t *testing.T) {
	eng := selfFixture(t)
	// Add a literal surface named "self" — should win over the virtual lookup.
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeShell, Name: "self", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd self: %v", err)
	}

	_, _, surface, err := resolveSurfaceArgOrSelf(eng, "self", "", "")
	if err != nil {
		t.Fatalf("resolveSurfaceArgOrSelf: %v", err)
	}
	if surface != "self" {
		t.Errorf("surface = %q, want literal 'self'", surface)
	}
}

func TestResolveSurfaceArgOrSelf_QualifiedSelfIsAlwaysLiteral(t *testing.T) {
	eng := selfFixture(t)
	// Qualified form must NOT silently substitute the current pane's surface.
	// It should return the literal "self" name regardless of whether such a
	// surface exists; the engine call would error later if not.
	_, _, surface, err := resolveSurfaceArgOrSelf(eng, "b1:self", "", "")
	if err != nil {
		t.Fatalf("resolveSurfaceArgOrSelf: %v", err)
	}
	if surface != "self" {
		t.Errorf("surface = %q, want literal 'self' (no virtual fallback for qualified)", surface)
	}
}

func TestResolveSurfaceArgOrSelf_FlagDisablesSelfFallback(t *testing.T) {
	eng := selfFixture(t)
	// --bay set means "self" is a literal name, not the virtual keyword.
	_, _, surface, err := resolveSurfaceArgOrSelf(eng, "self", "b1", "")
	if err != nil {
		t.Fatalf("resolveSurfaceArgOrSelf: %v", err)
	}
	if surface != "self" {
		t.Errorf("surface = %q, want literal 'self' (flags disable virtual fallback)", surface)
	}
}

func TestResolveSurfaceArgOrSelf_NonSelfDelegates(t *testing.T) {
	eng := selfFixture(t)
	// Any non-"self" positional should behave exactly like resolveSurfaceArg.
	dock, bay, surface, err := resolveSurfaceArgOrSelf(eng, "b1:second", "", "")
	if err != nil {
		t.Fatalf("resolveSurfaceArgOrSelf: %v", err)
	}
	if dock != "labs" || bay != "b1" || surface != "second" {
		t.Errorf("got (%q,%q,%q), want (labs,b1,second)", dock, bay, surface)
	}
}

// TestResolveBayArg_NameRejectedWithHint covers the strict resolver's
// user-facing behavior: typing a friendly Name produces an error that
// names the canonical ID, so the user can copy it directly.
func TestResolveBayArg_NameRejectedWithHint(t *testing.T) {
	eng := bayArgFixture(t)
	// "solo" is the Name the user might type; its ID is b2 in labs.
	_, _, err := resolveBayArg(eng, "solo", "")
	if err == nil {
		t.Fatal("expected error: Name lookup should fail under strict resolver")
	}
	if !strings.Contains(err.Error(), `did you mean "b2"`) {
		t.Errorf("error should hint at canonical ID; got %v", err)
	}
}
