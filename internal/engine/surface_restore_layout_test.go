package engine

import (
	"strings"
	"testing"

	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/tmux"
)

// assertLayout fails the test if the window's tmux layout doesn't match
// want. The layout-string syntax: pane IDs as leaves, "/" for vertical
// splits, "|" for horizontal, parens around nested internal nodes.
// Example: "%1/(%2|%3)" — %1 stacked above a horizontal split of %2 and %3.
func assertLayout(t *testing.T, mock *tmux.Mock, windowID, want string) {
	t.Helper()
	got, err := mock.LayoutString(windowID)
	if err != nil {
		t.Fatalf("LayoutString(%s): %v", windowID, err)
	}
	if got != want {
		t.Errorf("layout mismatch for window %s\n  got:  %s\n  want: %s", windowID, got, want)
	}
}

// addShell is a tiny helper for the layout-sensitive tests below: adds
// a shell surface with the given name and split direction, and returns
// the new surface's pane ID for use in expected layout strings.
func addShell(t *testing.T, eng *Engine, bay, name, splitDir string) string {
	t.Helper()
	if err := eng.SurfaceAdd(SurfaceAddOptions{
		DockName: "labs", BayName: bay,
		Type: manifest.SurfaceTypeShell, Name: name, SplitDir: splitDir,
	}); err != nil {
		t.Fatalf("SurfaceAdd %s: %v", name, err)
	}
	bayView, _ := eng.BayShow("labs", bay)
	return bayView.FindSurface(name).Tmux.PaneID
}

// threePaneFixture is the shared prologue for the layout tests: creates
// bay "b1" with three vertical panes (root + middle + bottom),
// returning the engine's mock view, the window ID, and each pane's ID.
type threePaneFixture struct {
	eng                          *Engine
	mock                         *tmux.Mock
	winID                        string
	rootName                     string
	rootPane, middlePane, bottom string
}

func setupThreePaneWindow(t *testing.T) threePaneFixture {
	t.Helper()
	eng, _ := testEngine(t)
	mock := eng.Tmux.(*tmux.Mock)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	bayView, _ := eng.BayShow("labs", "b1")
	root := bayView.Surfaces[0]
	middle := addShell(t, eng, "b1", "middle", "v")
	bottom := addShell(t, eng, "b1", "bottom", "v")
	return threePaneFixture{
		eng:        eng,
		mock:       mock,
		winID:      root.Tmux.WindowID,
		rootName:   root.Name,
		rootPane:   root.Tmux.PaneID,
		middlePane: middle,
		bottom:     bottom,
	}
}

// TestSurfaceRestore_LayoutCloseLastRestoresAtEnd is the regression test
// for "delete last of three, restore goes in middle." With layout-string
// assertions the failure mode is unambiguous.
func TestSurfaceRestore_LayoutCloseLastRestoresAtEnd(t *testing.T) {
	f := setupThreePaneWindow(t)

	// Three vertical panes — tmux's binary-split model nests:
	// new splits replace the target leaf with a {target, new} split.
	assertLayout(t, f.mock, f.winID, f.rootPane+"/("+f.middlePane+"/"+f.bottom+")")

	if err := f.eng.SurfaceClose("labs", "b1", "bottom", false); err != nil {
		t.Fatalf("SurfaceClose bottom: %v", err)
	}
	assertLayout(t, f.mock, f.winID, f.rootPane+"/"+f.middlePane)

	if _, err := f.eng.SurfaceRestore("labs"); err != nil {
		t.Fatalf("SurfaceRestore: %v", err)
	}
	// The restored pane lands as a sibling of the last surface (middle),
	// nested in middle's region — visually appended to the bottom.
	bayView, _ := f.eng.BayShow("labs", "b1")
	restored := bayView.FindSurface("bottom").Tmux.PaneID
	assertLayout(t, f.mock, f.winID, f.rootPane+"/("+f.middlePane+"/"+restored+")")
}

// TestSurfaceRestore_LayoutRootPaneLandsAtRoot exercises the
// `split-window -fb` path: closing the root pane and restoring it must
// place the new pane at the root level of the window's layout, wrapping
// the surviving siblings.
func TestSurfaceRestore_LayoutRootPaneLandsAtRoot(t *testing.T) {
	f := setupThreePaneWindow(t)

	if err := f.eng.SurfaceClose("labs", "b1", f.rootName, false); err != nil {
		t.Fatalf("SurfaceClose root: %v", err)
	}
	// After root is gone, the surviving inner split collapses out of the
	// root spot — layout is just the middle/bottom pair.
	assertLayout(t, f.mock, f.winID, f.middlePane+"/"+f.bottom)

	if _, err := f.eng.SurfaceRestore("labs"); err != nil {
		t.Fatalf("SurfaceRestore: %v", err)
	}
	// The restored root must wrap the surviving panes, not nest inside
	// one of them. With -fb the layout becomes {restored, prev_layout}.
	bayView, _ := f.eng.BayShow("labs", "b1")
	restored := bayView.FindSurface(f.rootName).Tmux.PaneID
	assertLayout(t, f.mock, f.winID, restored+"/("+f.middlePane+"/"+f.bottom+")")
}

// TestSurfaceRestore_LayoutStackedRestoreReproducesOriginal confirms the
// LIFO close-then-restore composition: closing every pane and restoring
// in order rebuilds the original layout structure.
func TestSurfaceRestore_LayoutStackedRestoreReproducesOriginal(t *testing.T) {
	f := setupThreePaneWindow(t)

	// Close every pane in order: bottom, middle, root. Closing the last
	// surface kills the tmux window, so the post-restore window is a
	// freshly created one — looked up from the bay state below.
	for _, name := range []string{"bottom", "middle", f.rootName} {
		if err := f.eng.SurfaceClose("labs", "b1", name, false); err != nil {
			t.Fatalf("SurfaceClose %s: %v", name, err)
		}
	}
	for i := range 3 {
		if _, err := f.eng.SurfaceRestore("labs"); err != nil {
			t.Fatalf("SurfaceRestore (%d): %v", i, err)
		}
	}

	bayView, _ := f.eng.BayShow("labs", "b1")
	rootPane := bayView.FindSurface(f.rootName).Tmux.PaneID
	middlePane := bayView.FindSurface("middle").Tmux.PaneID
	bottomPane := bayView.FindSurface("bottom").Tmux.PaneID
	winID := bayView.FindSurface(f.rootName).Tmux.WindowID

	got, err := f.mock.LayoutString(winID)
	if err != nil {
		t.Fatalf("LayoutString: %v", err)
	}
	// Strip parens for an order-only check, since the binary nesting
	// shape is an implementation detail of the restore sequence.
	flat := strings.NewReplacer("(", "", ")", "").Replace(got)
	want := rootPane + "/" + middlePane + "/" + bottomPane
	if flat != want {
		t.Errorf("stacked-restore visual order wrong\n  got (flattened): %s\n  raw:             %s\n  want:            %s", flat, got, want)
	}
}

func TestSurfaceRestore_LayoutDiscoveredWholeWindowDeathRestoresOriginalOrder(t *testing.T) {
	f := setupThreePaneWindow(t)

	if err := f.mock.KillWindow(f.winID); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}
	f.eng.SyncAll()

	for i := range 3 {
		if _, err := f.eng.SurfaceRestore("labs"); err != nil {
			t.Fatalf("SurfaceRestore (%d): %v", i, err)
		}
	}

	bayView, _ := f.eng.BayShow("labs", "b1")
	rootPane := bayView.FindSurface(f.rootName).Tmux.PaneID
	middlePane := bayView.FindSurface("middle").Tmux.PaneID
	bottomPane := bayView.FindSurface("bottom").Tmux.PaneID
	winID := bayView.FindSurface(f.rootName).Tmux.WindowID

	got, err := f.mock.LayoutString(winID)
	if err != nil {
		t.Fatalf("LayoutString: %v", err)
	}
	flat := strings.NewReplacer("(", "", ")", "").Replace(got)
	want := rootPane + "/" + middlePane + "/" + bottomPane
	if flat != want {
		t.Errorf("discovered whole-window restore visual order wrong\n  got (flattened): %s\n  raw:             %s\n  want:            %s", flat, got, want)
	}
}

// TestSurfaceRestore_LayoutMixedAxisStaysIntact ensures the layout-tree
// mock correctly handles a mixed-axis layout: a horizontal split on top
// with a vertical split inside its right half. Closing and restoring
// either pane should preserve nesting structure.
func TestSurfaceRestore_LayoutMixedAxisStaysIntact(t *testing.T) {
	eng, _ := testEngine(t)
	mock := eng.Tmux.(*tmux.Mock)

	if _, err := eng.BayNew(BayNewOptions{Dock: "labs"}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	bayView, _ := eng.BayShow("labs", "b1")
	rootPane := bayView.Surfaces[0].Tmux.PaneID
	winID := bayView.Surfaces[0].Tmux.WindowID

	right := addShell(t, eng, "b1", "right", "h")
	rightBottom := addShell(t, eng, "b1", "right-bottom", "v")

	// Split shape: root | (right / right-bottom).
	// Splits-against-last logic puts right-bottom under "right" since
	// "right" is the most-recently-added surface in layout group 1.
	assertLayout(t, mock, winID, rootPane+"|("+right+"/"+rightBottom+")")

	// Close the bottom of the right column, restore. It should land
	// back as a vertical split with "right" — no axis confusion.
	if err := eng.SurfaceClose("labs", "b1", "right-bottom", false); err != nil {
		t.Fatalf("SurfaceClose: %v", err)
	}
	assertLayout(t, mock, winID, rootPane+"|"+right)

	if _, err := eng.SurfaceRestore("labs"); err != nil {
		t.Fatalf("SurfaceRestore: %v", err)
	}
	bayView, _ = eng.BayShow("labs", "b1")
	restored := bayView.FindSurface("right-bottom").Tmux.PaneID
	assertLayout(t, mock, winID, rootPane+"|("+right+"/"+restored+")")
}
