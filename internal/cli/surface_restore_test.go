package cli

import (
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
)

// TestRunSurfaceRestore_EmptyQueueEmitsTmuxToast regresses the first
// local-testing bug: Option+W → Option+Z on an empty queue printed to
// stderr only, which is invisible from tmux run-shell. The fix routes
// the message through tmux display-message, which stays visible.
func TestRunSurfaceRestore_EmptyQueueEmitsTmuxToast(t *testing.T) {
	eng, mockTmux, _, _ := testNavEngine(t)
	mockTmux.SetCurrentSession("labs")
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")

	if err := runSurfaceRestore(eng, false); err != nil {
		t.Fatalf("runSurfaceRestore: %v", err)
	}

	msgs := mockTmux.DisplayMessages()
	if len(msgs) == 0 {
		t.Fatal("expected a tmux display-message on empty queue; got none")
	}
	if !strings.Contains(msgs[len(msgs)-1], "Nothing to restore") {
		t.Errorf("last tmux message = %q; want substring %q", msgs[len(msgs)-1], "Nothing to restore")
	}
}

// TestRunSurfaceRestore_SuccessEmitsNoToast regression-tests the opposite
// of the "empty queue needs feedback" test: on successful restore we
// must NOT emit a tmux display-message. The restored pane appearing is
// the user-visible feedback, and a status-line message here correlates
// with a visible stall in the new pane's content rendering.
func TestRunSurfaceRestore_SuccessEmitsNoToast(t *testing.T) {
	eng, mockTmux, _, _ := testNavEngine(t)
	mockTmux.SetCurrentSession("labs")
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")

	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeShell, Name: "logs", SplitDir: "v",
	}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}
	if err := eng.SurfaceClose("labs", "b1", "logs", false); err != nil {
		t.Fatalf("SurfaceClose: %v", err)
	}

	before := len(mockTmux.DisplayMessages())
	if err := runSurfaceRestore(eng, false); err != nil {
		t.Fatalf("runSurfaceRestore: %v", err)
	}

	if len(mockTmux.DisplayMessages()) != before {
		t.Errorf("successful restore must not emit a tmux display-message; new messages: %v",
			mockTmux.DisplayMessages()[before:])
	}
}

func TestRunSurfaceRestore_DiscoversDeadSurfaceBeforeRestore(t *testing.T) {
	eng, mockTmux, _, _ := testNavEngine(t)
	mockTmux.SetCurrentSession("labs")
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")

	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(engine.SurfaceAddOptions{
		DockName: "labs", BayName: "b1",
		Type: manifest.SurfaceTypeShell, Name: "logs", SplitDir: "v",
	}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	logs := bay.FindSurface("logs")
	if logs == nil || logs.Tmux == nil {
		t.Fatalf("logs surface missing tmux attrs: %+v", bay.Surfaces)
	}
	if err := mockTmux.KillPane(logs.Tmux.PaneID); err != nil {
		t.Fatalf("KillPane: %v", err)
	}

	if err := runSurfaceRestore(eng, false); err != nil {
		t.Fatalf("runSurfaceRestore: %v", err)
	}

	bay, _ = eng.BayShow("labs", "b1")
	if bay.FindSurface("logs") == nil {
		t.Fatal("expected runSurfaceRestore to discover and restore dead surface")
	}
	entries, err := eng.ListClosedEntries("labs")
	if err != nil {
		t.Fatalf("ListClosedEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected queue drained after restore, got %d entries", len(entries))
	}
}

func TestRunSurfaceRestore_KeepsQueuedCloseBlockAheadOfDiscoveredDeadSurface(t *testing.T) {
	eng, mockTmux, _, _ := testNavEngine(t)
	mockTmux.SetCurrentSession("labs")
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")

	if _, err := eng.BayNew(engine.BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	for _, name := range []string{"logs", "notes", "scratch"} {
		if err := eng.SurfaceAdd(engine.SurfaceAddOptions{
			DockName: "labs", BayName: "b1",
			Type: manifest.SurfaceTypeShell, Name: name, SplitDir: "v",
		}); err != nil {
			t.Fatalf("SurfaceAdd %s: %v", name, err)
		}
	}
	if err := eng.SurfaceClose("labs", "b1", "logs", false); err != nil {
		t.Fatalf("SurfaceClose logs: %v", err)
	}
	if err := eng.SurfaceClose("labs", "b1", "notes", false); err != nil {
		t.Fatalf("SurfaceClose notes: %v", err)
	}

	bay, _ := eng.BayShow("labs", "b1")
	scratch := bay.FindSurface("scratch")
	if scratch == nil || scratch.Tmux == nil {
		t.Fatalf("scratch surface missing tmux attrs: %+v", bay.Surfaces)
	}
	if err := mockTmux.KillPane(scratch.Tmux.PaneID); err != nil {
		t.Fatalf("KillPane scratch: %v", err)
	}

	for i, want := range []string{"notes", "logs", "scratch"} {
		if err := runSurfaceRestore(eng, false); err != nil {
			t.Fatalf("runSurfaceRestore %d: %v", i+1, err)
		}
		bay, _ = eng.BayShow("labs", "b1")
		if bay.FindSurface(want) == nil {
			t.Fatalf("restore %d restored wrong surface; expected %q in %+v", i+1, want, bay.Surfaces)
		}
	}
}
