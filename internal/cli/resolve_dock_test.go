package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/commontoolsinc/bay/internal/engine"
	"github.com/commontoolsinc/bay/internal/manifest"
	"github.com/commontoolsinc/bay/internal/tmux"
)

// TestResolveCurrentDock_TmuxSession verifies the primary path: if
// the user is in a tmux pane whose session matches a bay dock, that
// dock is returned.
func TestResolveCurrentDock_TmuxSession(t *testing.T) {
	eng, mockTmux, _, _ := testNavEngine(t)
	mockTmux.SetCurrentSession("labs")

	// Simulate being inside tmux.
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")

	dock, err := resolveCurrentDock(eng)
	if err != nil {
		t.Fatalf("resolveCurrentDock: %v", err)
	}
	if dock != "labs" {
		t.Errorf("dock = %q, want labs", dock)
	}
}

// TestResolveCurrentDock_IgnoresTmuxOutsidePane verifies that a
// CurrentSession error — what the tmux layer returns outside tmux,
// see TestReal_CurrentAccessors_OutsideTmux — is not papered over
// with some other dock.
func TestResolveCurrentDock_IgnoresTmuxOutsidePane(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	// No SetCurrentSession: the mock reports "not in a tmux session",
	// matching Real outside tmux.

	_, err := resolveCurrentDock(eng)
	if err == nil {
		t.Error("expected error: should not resolve a dock when not inside tmux")
	}
}

// TestResolveCurrentDock_CWDFallback verifies the fallback: if the
// user is NOT in a tmux session but their CWD is inside a known
// checkout, the matching dock is returned.
func TestResolveCurrentDock_CWDFallback(t *testing.T) {
	eng, _, mockGit, dir := testNavEngine(t)
	// Not in tmux — no SetCurrentSession call.

	// Simulate CWD inside the repo directory. Use the actual path
	// from Getwd after chdir — on macOS, TempDir paths may differ
	// from what Getwd returns due to symlinks (/tmp → /private/tmp).
	repoDir := dir + "/repos/labs"
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	actualCwd, _ := os.Getwd()
	mockGit.SetRepoRoot(actualCwd, actualCwd)

	dock, err := resolveCurrentDock(eng)
	if err != nil {
		t.Fatalf("resolveCurrentDock: %v", err)
	}
	if dock != "labs" {
		t.Errorf("dock = %q, want labs", dock)
	}
}

// TestResolveCurrentDock_NeitherErrors verifies that if neither tmux
// nor CWD resolves to a dock, an error is returned.
func TestResolveCurrentDock_NeitherErrors(t *testing.T) {
	eng, _, _, _ := testNavEngine(t)
	// Not in tmux, CWD is not a known repo.

	_, err := resolveCurrentDock(eng)
	if err == nil {
		t.Error("expected error when neither tmux nor CWD resolves to a dock")
	}
}

// setupCWDvsSession builds an engine with the testNavEngine "labs" dock
// plus a second "otherdock" that has its own checkout, then chdirs into
// otherdock's checkout and points the git mock at it. Tests then attach
// (via TMUX/TMUX_PANE + SetCurrentSession) to a *different* session to
// exercise the CWD-vs-session precedence.
func setupCWDvsSession(t *testing.T) (*engine.Engine, *tmux.Mock) {
	t.Helper()
	eng, mockTmux, mockGit, dir := testNavEngine(t)

	otherRepo := filepath.Join(dir, "repos", "otherdock")
	if err := os.MkdirAll(filepath.Join(otherRepo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	m, err := manifest.Load(manifestPath)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	m.Docks = append(m.Docks, manifest.Dock{
		Name: "otherdock", Path: otherRepo, Agent: "claude", Bays: []manifest.Bay{},
	})
	if err := manifest.Save(manifestPath, m); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	// Stand in otherdock's checkout. Use the actual path from Getwd
	// after chdir (macOS symlinks /tmp → /private/tmp).
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(oldWd) })
	if err := os.Chdir(otherRepo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	actualCwd, _ := os.Getwd()
	mockGit.SetRepoRoot(actualCwd, actualCwd)

	return eng, mockTmux
}

// TestResolveCurrentDock_CWDBeatsTmuxSession is the regression test for
// the bug where `bay new` in one dock's checkout created the bay in the
// dock of the current tmux session instead. In a real interactive pane
// (TMUX_PANE set), standing in a known checkout must win over the
// ambient session.
func TestResolveCurrentDock_CWDBeatsTmuxSession(t *testing.T) {
	eng, mockTmux := setupCWDvsSession(t)

	// Attached to "labs", in an interactive pane, but CWD is otherdock.
	mockTmux.SetCurrentSession("labs")
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%1")

	dock, err := resolveCurrentDock(eng)
	if err != nil {
		t.Fatalf("resolveCurrentDock: %v", err)
	}
	if dock != "otherdock" {
		t.Errorf("dock = %q, want otherdock (CWD must beat session in an interactive pane)", dock)
	}
}

// TestResolveCurrentDock_KeybindingPrefersSession is the regression test
// for the inverse bug: a tmux keybinding runs bay via run-shell, where
// TMUX is set but TMUX_PANE is not and the CWD is the tmux server's
// directory (here, otherdock's checkout). The session the user is
// looking at must win, so Option+c creates bays in the current dock —
// not wherever the server happens to sit.
func TestResolveCurrentDock_KeybindingPrefersSession(t *testing.T) {
	eng, mockTmux := setupCWDvsSession(t)

	// Attached to "labs"; run-shell context has no TMUX_PANE even though
	// the CWD sits in otherdock's checkout.
	mockTmux.SetCurrentSession("labs")
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "")

	dock, err := resolveCurrentDock(eng)
	if err != nil {
		t.Fatalf("resolveCurrentDock: %v", err)
	}
	if dock != "labs" {
		t.Errorf("dock = %q, want labs (session must beat CWD from a run-shell keybinding)", dock)
	}
}
