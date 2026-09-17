package engine

import (
	"testing"
	"time"

	"github.com/commonfabric/bay/internal/manifest"
)

// lastActive returns the LastActive timestamp for a bay, or 0 if missing.
func lastActive(t *testing.T, eng *Engine, dockName, bayID string) int64 {
	t.Helper()
	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return 0
	}
	bay := dock.FindBayByID(bayID)
	if bay == nil {
		return 0
	}
	return bay.LastActive
}

// staleBay makes the bay look "old" by zeroing its LastActive,
// so test assertions can verify that a subsequent operation bumped it back.
func staleBay(t *testing.T, eng *Engine, dockName, bayID string) {
	t.Helper()
	err := eng.withManifest(func(m *manifest.Manifest) error {
		m.FindDock(dockName).FindBayByID(bayID).LastActive = 0
		return nil
	})
	if err != nil {
		t.Fatalf("staleBay: %v", err)
	}
}

func TestSurfaceAdd_BumpsLastActive(t *testing.T) {
	eng, _ := testEngine(t)
	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	staleBay(t, eng, "labs", "b1")
	before := time.Now().Unix()

	if err := eng.SurfaceAdd(SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeShell, Name: "shell-2", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}

	if got := lastActive(t, eng, "labs", "b1"); got < before {
		t.Errorf("LastActive = %d, want >= %d (SurfaceAdd should bump)", got, before)
	}
}

func TestSurfaceClose_BumpsLastActive(t *testing.T) {
	eng, _ := testEngine(t)
	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	if err := eng.SurfaceAdd(SurfaceAddOptions{DockName: "labs", BayName: "b1", Type: manifest.SurfaceTypeShell, Name: "shell-2", SplitDir: "v"}); err != nil {
		t.Fatalf("SurfaceAdd: %v", err)
	}
	staleBay(t, eng, "labs", "b1")
	before := time.Now().Unix()

	if err := eng.SurfaceClose("labs", "b1", "shell-2", false); err != nil {
		t.Fatalf("SurfaceClose: %v", err)
	}

	if got := lastActive(t, eng, "labs", "b1"); got < before {
		t.Errorf("LastActive = %d, want >= %d (SurfaceClose should bump)", got, before)
	}
}

func TestSurfaceRename_BumpsLastActive(t *testing.T) {
	eng, _ := testEngine(t)
	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	staleBay(t, eng, "labs", "b1")
	before := time.Now().Unix()

	if err := eng.SurfaceRename("labs", "b1", "shell", "main-shell"); err != nil {
		t.Fatalf("SurfaceRename: %v", err)
	}

	if got := lastActive(t, eng, "labs", "b1"); got < before {
		t.Errorf("LastActive = %d, want >= %d (SurfaceRename should bump)", got, before)
	}
}

func TestEdit_BumpsLastActive(t *testing.T) {
	eng, _ := testEngine(t)
	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	staleBay(t, eng, "labs", "b1")
	before := time.Now().Unix()

	if _, err := eng.Edit("labs", "b1"); err != nil {
		t.Fatalf("Edit: %v", err)
	}

	if got := lastActive(t, eng, "labs", "b1"); got < before {
		t.Errorf("LastActive = %d, want >= %d (Edit should bump)", got, before)
	}
}

func TestBayRename_BumpsLastActive(t *testing.T) {
	eng, _ := testEngine(t)
	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	staleBay(t, eng, "labs", "b1")
	before := time.Now().Unix()

	if err := eng.BayRename("labs", "b1", "renamed"); err != nil {
		t.Fatalf("BayRename: %v", err)
	}

	// Rename changes Name, not ID; lookup by ID continues to work.
	if got := lastActive(t, eng, "labs", "b1"); got < before {
		t.Errorf("LastActive = %d, want >= %d (BayRename should bump)", got, before)
	}
}

func TestSetLastFocused_BumpsLastActive(t *testing.T) {
	// SetLastFocused is the original LastActive bumper from #94. Verify
	// it still works alongside the new bumps in this PR.
	eng, _ := testEngine(t)
	if _, err := eng.BayNew(BayNewOptions{Dock: "labs", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}
	bay, _ := eng.BayShow("labs", "b1")
	staleBay(t, eng, "labs", "b1")
	before := time.Now().Unix()

	if err := eng.SetLastFocused("labs", "b1", bay.Surfaces[0].ID); err != nil {
		t.Fatalf("SetLastFocused: %v", err)
	}

	if got := lastActive(t, eng, "labs", "b1"); got < before {
		t.Errorf("LastActive = %d, want >= %d (SetLastFocused should bump)", got, before)
	}
}
