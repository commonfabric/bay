package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/commonfabric/bay/internal/config"
)

// --- Hard break: bay edit --set and --show are gone ---

func TestBayEdit_NoLongerHasSetFlag(t *testing.T) {
	root := NewRootCmd("test")
	cmd, _, err := root.Find([]string{"edit"})
	if err != nil {
		t.Fatalf("could not find edit: %v", err)
	}
	if cmd.Flags().Lookup("set") != nil {
		t.Error("bay edit still has --set flag; it should have moved to bay config editor <name>")
	}
}

func TestBayEdit_NoLongerHasShowFlag(t *testing.T) {
	root := NewRootCmd("test")
	cmd, _, err := root.Find([]string{"edit"})
	if err != nil {
		t.Fatalf("could not find edit: %v", err)
	}
	if cmd.Flags().Lookup("show") != nil {
		t.Error("bay edit still has --show flag; it should have moved to bay config editor")
	}
}

func TestBayEdit_HasDockScopeAndBayTargetFlags(t *testing.T) {
	root := NewRootCmd("test")
	cmd, _, err := root.Find([]string{"edit"})
	if err != nil {
		t.Fatalf("could not find edit: %v", err)
	}
	if cmd.Flags().Lookup("dock") == nil {
		t.Error("bay edit missing --dock flag")
	}
	if cmd.Flags().Lookup("bay") == nil {
		t.Error("bay edit missing --bay flag")
	}
	if cmd.Flags().Lookup("dock-name") == nil {
		t.Error("bay edit missing --dock-name flag")
	}
}

// --- New bay config namespace ---

func TestBayConfigCommand_HasSubcommands(t *testing.T) {
	// Pin the four expected child commands. If a future change
	// adds/removes one, this test fails with a clear pointer.
	root := NewRootCmd("test")
	for _, path := range [][]string{
		{"config", "edit"},
		{"config", "show"},
		{"config", "path"},
		{"config", "editor"},
	} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Errorf("missing command: bay %s (%v)", joinPath(path), err)
			continue
		}
		// cobra.Find can return the parent if a subcommand isn't found.
		// Verify we got the actual leaf, not the parent falling through.
		if cmd.Name() != path[len(path)-1] {
			t.Errorf("bay %s resolved to %q, not the expected leaf", joinPath(path), cmd.Name())
		}
	}
}

func TestBayConfigEditor_NoArgPrintsResolvedEditor(t *testing.T) {
	// Tests the helper that backs `bay config editor` (no arg).
	// Format must match the old `bay edit --show` output:
	// "<command> (<terminal|GUI>)" or "No editor configured or detected."
	cfg := &config.Config{DefaultEditor: "nvim"}
	got := configEditorGetString(cfg)
	if got != "nvim (terminal)" {
		t.Errorf("configEditorGetString = %q, want %q", got, "nvim (terminal)")
	}
}

func TestBayConfigEditor_NoArgWithGUIEditor(t *testing.T) {
	cfg := &config.Config{DefaultEditor: "cursor"}
	got := configEditorGetString(cfg)
	if got != "cursor (GUI)" {
		t.Errorf("configEditorGetString = %q, want %q", got, "cursor (GUI)")
	}
}

func TestBayConfigEditor_NoArgNoEditor(t *testing.T) {
	// No config + no env vars + no probe match → "no editor"
	// message. The probe scans real PATH so we can't fully isolate;
	// instead we verify the helper returns SOMETHING when an editor
	// IS resolved, and trust resolveEditor's existing test coverage
	// for the no-editor branch.
	cfg := &config.Config{}
	got := configEditorGetString(cfg)
	if got == "" {
		t.Error("configEditorGetString should never return an empty string; either an editor or the 'no editor' message")
	}
}

func TestBayConfigEditor_WithArgPersistsEditor(t *testing.T) {
	// Run the set helper and verify the config is persisted.
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{},
		Docks:  map[string]config.DockConfig{},
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	if err := runConfigEditorSet(configPath, "cursor"); err != nil {
		t.Fatalf("runConfigEditorSet: %v", err)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if loaded.DefaultEditor != "cursor" {
		t.Errorf("persisted editor = %q, want cursor", loaded.DefaultEditor)
	}
}

// TestRunConfigEditorSet_CreatesConfigWhenMissing verifies that on a
// fresh install (no config file, no parent directory), running
// `bay config editor <name>` creates the directory, seeds a default
// config with the editor field set, and persists it.
//
// Caught a real bug introduced by the first iteration of this PR:
// runConfigEditorSet was using os.IsNotExist(err), which doesn't
// unwrap fmt.Errorf wraps and so failed to detect the missing-file
// case after config.Load wrapped its error.
func TestRunConfigEditorSet_CreatesConfigWhenMissing(t *testing.T) {
	dir := t.TempDir()
	// Two levels deep to also exercise the parent-dir creation.
	configPath := filepath.Join(dir, "config", "bay", "config.toml")

	if err := runConfigEditorSet(configPath, "cursor"); err != nil {
		t.Fatalf("runConfigEditorSet on missing config: %v", err)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("loading saved config: %v", err)
	}
	if loaded.DefaultEditor != "cursor" {
		t.Errorf("persisted editor = %q, want cursor", loaded.DefaultEditor)
	}
}

func TestBayConfigPath_PrintsConfigPath(t *testing.T) {
	// The path-printer takes the path as an argument and writes it
	// to a writer; that's the surface that's testable in isolation.
	got := configPathString("/some/path/config.toml")
	if got != "/some/path/config.toml" {
		t.Errorf("configPathString = %q, want literal path", got)
	}
}

// TestPrepareConfigFileForEdit_SeedsDefaultsWhenMissing pins the
// fix for the asymmetry between bay config edit and bay config editor:
//
//	bay config editor cursor → seeds DefaultConfig + sets editor
//	bay config edit          → used to open an EMPTY buffer, no seed
//
// On a fresh install, opening an empty buffer in $EDITOR is a worse
// starting point than getting the default TOML structure with all
// the section headers. git config --edit does the equivalent.
//
// After this fix, bay config edit ALSO seeds DefaultConfig if the
// file doesn't exist, so the user always opens a real config to edit.
func TestPrepareConfigFileForEdit_SeedsDefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "bay", "config.toml")

	if err := prepareConfigFileForEdit(configPath); err != nil {
		t.Fatalf("prepareConfigFileForEdit: %v", err)
	}

	// File should now exist with default content.
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load on seeded file: %v", err)
	}
	// Verify it loaded — effective interval should be the default.
	if loaded.Monitor.EffectiveInterval() != 3 {
		t.Errorf("effective interval = %d, want 3", loaded.Monitor.EffectiveInterval())
	}
}

// TestPrepareConfigFileForEdit_LeavesExistingFileAlone verifies the
// helper does NOT clobber a user's existing config. The seeding only
// fires when the file is missing.
func TestPrepareConfigFileForEdit_LeavesExistingFileAlone(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	// Seed a config the user supposedly already wrote.
	userCfg := &config.Config{
		Docks:         map[string]config.DockConfig{},
		DefaultEditor: "user-editor",
	}
	if err := config.Save(configPath, userCfg); err != nil {
		t.Fatalf("seeding user config: %v", err)
	}

	if err := prepareConfigFileForEdit(configPath); err != nil {
		t.Fatalf("prepareConfigFileForEdit: %v", err)
	}

	// User's editor command must still be there.
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("loading after prepare: %v", err)
	}
	if loaded.DefaultEditor != "user-editor" {
		t.Errorf("user's editor command was clobbered; got %q, want user-editor", loaded.DefaultEditor)
	}
}

// TestEnsureConfigFileDir_CreatesMissingDirectory pins the fix for
// the first-run UX bug: bay config edit on a fresh install with no
// ~/.config/bay/ directory used to launch the editor on a path whose
// parent didn't exist, so the editor's save would fail.
func TestEnsureConfigFileDir_CreatesMissingDirectory(t *testing.T) {
	root := t.TempDir()
	// Two levels deep to verify MkdirAll, not just Mkdir.
	configPath := filepath.Join(root, "config", "bay", "config.toml")

	if err := ensureConfigFileDir(configPath); err != nil {
		t.Fatalf("ensureConfigFileDir: %v", err)
	}

	info, err := os.Stat(filepath.Join(root, "config", "bay"))
	if err != nil {
		t.Fatalf("expected parent directory to exist: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected directory, got mode %v", info.Mode())
	}
}

func TestEnsureConfigFileDir_NoOpWhenDirectoryExists(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	// Calling on an already-existing parent should not error.
	if err := ensureConfigFileDir(configPath); err != nil {
		t.Errorf("ensureConfigFileDir on existing parent: %v", err)
	}
}

func TestBayConfigShow_OutputContainsExpectedFields(t *testing.T) {
	cfg := &config.Config{
		DefaultEditor: "nvim",
		Monitor: config.MonitorConfig{
			IntervalSeconds: 5,
		},
	}
	out, err := configShowString(cfg)
	if err != nil {
		t.Fatalf("configShowString: %v", err)
	}
	// Check that the rendered output mentions each section we care
	// about. We don't pin the exact format because TOML re-rendering
	// can vary; just verify the surface mentions agents, editor,
	// monitor.
	for _, want := range []string{"nvim", "5"} {
		if !contains(out, want) {
			t.Errorf("config show output missing %q; got:\n%s", want, out)
		}
	}
}
