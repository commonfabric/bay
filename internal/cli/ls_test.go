package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/git"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/tmux"
)

func testListEngine(t *testing.T) (*engine.Engine, string) {
	t.Helper()
	dir := t.TempDir()

	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"claude": {Command: "claude"},
		},
		Docks: map[string]config.DockConfig{},
	}

	repoDir := filepath.Join(dir, "repos", "labs")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	webDir := filepath.Join(dir, "repos", "web")
	if err := os.MkdirAll(filepath.Join(webDir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	mockTmux := tmux.NewMock()
	mockGit := git.NewMock()
	mockGit.SetGlobalIgnored(true)

	manifestPath := filepath.Join(dir, "manifest.json")
	manifest.Save(manifestPath, &manifest.Manifest{
		Version: manifest.CurrentVersion,
		Docks: []manifest.Dock{
			{Name: "labs", Path: repoDir, Agent: "claude", Bays: []manifest.Bay{}},
			{Name: "web", Path: webDir, Agent: "claude", Bays: []manifest.Bay{}},
		},
	})

	return engine.New(
		cfg,
		filepath.Join(dir, "config.toml"),
		manifestPath,
		filepath.Join(dir, "archive.json"),
		mockTmux,
		mockGit,
	), dir
}

func TestResolveListFocus_DirtySkipsContextualFocus(t *testing.T) {
	eng, _ := testListEngine(t)

	bay, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew failed: %v", err)
	}
	if err := os.MkdirAll(bay.Path, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.Chdir(bay.Path); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir("/")
	})

	focus := resolveListFocus(eng, true)
	if focus.Kind != FocusAll {
		t.Fatalf("dirty listings should ignore contextual focus, got %#v", focus)
	}
}

func TestInferListFocus_BayContext(t *testing.T) {
	eng, _ := testListEngine(t)

	bay, err := eng.BayNew(engine.BayNewOptions{Dock: "labs", Shell: true})
	if err != nil {
		t.Fatalf("BayNew failed: %v", err)
	}
	if err := os.MkdirAll(bay.Path, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.Chdir(bay.Path); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir("/")
	})

	focus := inferListFocus(eng)
	if focus.Kind != FocusBay || focus.Dock != "labs" || focus.BayID != bay.ID {
		t.Fatalf("unexpected focus: %#v", focus)
	}
}

func TestInferListFocus_DockCheckoutContext(t *testing.T) {
	eng, dir := testListEngine(t)

	repoPath := filepath.Join(dir, "repos", "labs")
	if err := os.Chdir(repoPath); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir("/")
	})

	focus := inferListFocus(eng)
	if focus.Kind != FocusDock || focus.Dock != "labs" {
		t.Fatalf("unexpected focus: %#v", focus)
	}
}
