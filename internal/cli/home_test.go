package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/commonfabric/bay/internal/manifest"
)

func TestRunHomeOutsideTmuxCreatesDockSession(t *testing.T) {
	eng, mockTmux, mockGit, dir := testNavEngine(t)
	t.Setenv("TMUX", "")

	repoDir := filepath.Join(dir, "repos", "labs")
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	actualCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd after chdir: %v", err)
	}
	mockGit.SetRepoRoot(actualCwd, actualCwd)
	mockTmux.Calls = nil

	if err := runHome(eng); err != nil {
		t.Fatalf("runHome: %v", err)
	}

	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock("labs")
	if dock == nil {
		t.Fatal("missing labs dock")
	}
	if dock.SessionID == "" {
		t.Fatal("home did not persist the newly created dock session ID")
	}
	home := dock.FindBayByID(manifest.HomeBayID)
	if home == nil || home.Type != manifest.BayTypeHome || len(home.Surfaces) != 1 {
		t.Fatalf("home = %+v, want one materialized home surface", home)
	}
	if home.Surfaces[0].Tmux == nil || home.Surfaces[0].Tmux.WindowID == "" {
		t.Fatalf("home surface missing tmux attrs: %+v", home.Surfaces[0])
	}

	sawNewSession := false
	sawHomeWindow := false
	for _, call := range mockTmux.Calls {
		if call.Method == "NewSession" && len(call.Args) == 1 && call.Args[0] == "labs" {
			sawNewSession = true
		}
		if call.Method == "NewWindow" && len(call.Args) >= 3 &&
			call.Args[0] == "labs" && call.Args[1] == manifest.HomeBayID && call.Args[2] == dock.Path {
			sawHomeWindow = true
		}
	}
	if !sawNewSession {
		t.Fatalf("runHome outside tmux should create the dock session; calls: %+v", mockTmux.Calls)
	}
	if !sawHomeWindow {
		t.Fatalf("runHome should create home at dock path %q; calls: %+v", dock.Path, mockTmux.Calls)
	}
}
