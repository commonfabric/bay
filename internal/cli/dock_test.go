package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
)

func TestRunDockNew_CreatesHomeShell(t *testing.T) {
	eng, mockTmux, _, dir := testNavEngine(t)
	repoDir := filepath.Join(dir, "repos", "research")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}

	if err := runDockNew(eng, "research", repoDir, "", "", ""); err != nil {
		t.Fatalf("runDockNew: %v", err)
	}

	home, err := eng.BayShow("research", manifest.HomeBayID)
	if err != nil {
		t.Fatalf("BayShow(home): %v", err)
	}
	if home.Type != manifest.BayTypeHome || home.Path != repoDir || len(home.Surfaces) != 1 {
		t.Fatalf("home = %+v, want one home surface at %q", home, repoDir)
	}

	windows, _ := mockTmux.ListWindows("research")
	for _, w := range windows {
		val, _ := mockTmux.GetWindowOption(w.ID, "@bay-placeholder")
		if val == "1" {
			t.Fatalf("explicit dock new should replace placeholder with home, found %s", w.ID)
		}
	}
}

func TestAutoBootstrapBayNewDoesNotCreateHome(t *testing.T) {
	eng, _, mockGit, dir := testNavEngine(t)
	repoDir := filepath.Join(dir, "repos", "auto")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	mockGit.SetRepoRoot(cwd, repoDir)

	dockName, err := autoBootstrap(eng, true)
	if err != nil {
		t.Fatalf("autoBootstrap: %v", err)
	}
	if _, err := eng.BayNew(engine.BayNewOptions{Dock: dockName, Name: "foo", Shell: true}); err != nil {
		t.Fatalf("BayNew: %v", err)
	}

	m, err := eng.LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		t.Fatalf("missing dock %q", dockName)
	}
	if home := dock.FindBayByID(manifest.HomeBayID); home != nil {
		t.Fatalf("auto-bootstrap bay new should not create home, got %+v", home)
	}
}

func TestDefaultDockNameResolvesDotPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "myproject")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Chdir(root)

	if got := defaultDockName("."); got != "myproject" {
		t.Fatalf("defaultDockName(.) = %q, want myproject", got)
	}
}
