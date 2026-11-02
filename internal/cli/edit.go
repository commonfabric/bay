package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/spf13/cobra"
)

func newEditCmd() *cobra.Command {
	var dockScope bool
	var editorFlag, splitDir, bayFlag, dockNameFlag string
	var window, pane bool

	cmd := &cobra.Command{
		Use:   "edit [bay]",
		Short: "Open editor on bay or dock",
		Long: `Open your editor. Default is bay-scoped (current bay).
Terminal editors split the current tmux window by default; use --window
for a new tmux window.

  bay edit                    bay editor as a split pane
  bay edit b1                 specific bay as a split pane
  bay edit --bay home         edit the dock checkout
  bay edit --dock             dock editor (all bays)
  bay edit --dock src/main.go focus dock editor on a file
  bay edit --dock .           focus dock editor on cwd
  bay edit --editor vim       use a specific editor this time
  bay edit --window           terminal editor in a new tmux window

Editor resolution order:
  1. --editor flag (this invocation only)
  2. default_editor in config (set via 'bay config editor <name>')
  3. $VISUAL
  4. $EDITOR
  5. Probe: cursor, code, zed, nvim, vim`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			if dockScope {
				if bayFlag != "" || dockNameFlag != "" {
					return fmt.Errorf("--dock cannot be combined with --bay")
				}
				focusPath := ""
				if len(args) > 0 {
					focusPath = args[0]
				}
				return runEditDock(eng, editorFlag, focusPath)
			}

			sd := resolveSplit(splitDir, window, pane)
			target, err := editTargetFromArgs(args, bayFlag, dockNameFlag)
			if err != nil {
				return err
			}
			return runEditCreate(eng, target, editorFlag, sd)
		},
	}

	cmd.Flags().BoolVar(&dockScope, "dock", false, "dock-scoped editor (all bays)")
	cmd.Flags().StringVar(&bayFlag, "bay", "", "bay ID (defaults to current)")
	cmd.Flags().StringVar(&dockNameFlag, "dock-name", "", "dock name (with --bay to disambiguate)")
	cmd.Flags().StringVar(&editorFlag, "editor", "", "editor command (overrides config for this invocation)")
	cmd.Flags().StringVar(&splitDir, "split", "", "split direction (h or v)")
	cmd.Flags().BoolVar(&pane, "pane", false, "split into current window (default; shorthand for --split v)")
	cmd.Flags().BoolVar(&window, "window", false, "open as a new tmux window")

	return cmd
}

// resolveSplit resolves the split direction. Default is a vertical split.
// --window opts into a new tmux window. --split overrides the direction
// explicitly.
func resolveSplit(splitDir string, window, pane bool) string {
	if splitDir != "" {
		return splitDir
	}
	if pane {
		return "v"
	}
	if window {
		return ""
	}
	return "v"
}

// runEditDock launches or focuses a dock-level editor that covers all
// bays. For terminal editors, creates a tracked surface in a tmux
// window at index 0. For GUI editors, launches fire-and-forget.
func runEditDock(eng *engine.Engine, editorOverride, focusPath string) error {
	dockName, err := eng.Tmux.CurrentSession()
	if err != nil {
		return fmt.Errorf("not in a tmux session")
	}

	editorCmd, isGUI := resolveEditorWithOverride(eng.Config, editorOverride)
	if editorCmd == "" {
		return fmt.Errorf("no editor found; set [editor].command in config, or $VISUAL/$EDITOR")
	}

	// Determine the edit path — worktree parent dir so all bays are visible.
	editPath, err := eng.EditAllParentDir(dockName)
	if err != nil {
		// Fallback: use the first bay path.
		paths, pathErr := eng.EditAll(dockName)
		if pathErr != nil || len(paths) == 0 {
			return fmt.Errorf("no bays to edit in dock %q", dockName)
		}
		editPath = paths[0]
	}

	// Resolve the focus path to an absolute path.
	var absFocusPath string
	if focusPath != "" {
		if filepath.IsAbs(focusPath) {
			absFocusPath = focusPath
		} else {
			cwd, _ := os.Getwd()
			absFocusPath = filepath.Join(cwd, focusPath)
		}
	}

	if isGUI {
		if absFocusPath != "" {
			relPath, relErr := filepath.Rel(editPath, absFocusPath)
			if relErr != nil {
				relPath = absFocusPath
			}
			return fmt.Errorf("opening a specific path is not supported for GUI editors — navigate to %s in the editor", relPath)
		}
		_, err = launchEditor(editorCmd, isGUI, []string{editPath})
		return err
	}

	// Ensure the dock editor surface exists.
	if err := eng.DockEditorAdd(dockName, editorCmd, editPath); err != nil {
		return err
	}

	if absFocusPath != "" {
		// Send the focus command to the terminal editor pane.
		return focusTerminalEditor(eng, dockName, absFocusPath)
	}
	return nil
}

// focusTerminalEditor sends an open-file command to the dock editor's
// tmux pane via send-keys.
func focusTerminalEditor(eng *engine.Engine, dockName, absPath string) error {
	winID, found := eng.Tmux.FindDockEditorWindow(dockName)
	if !found {
		return fmt.Errorf("dock editor not found")
	}
	panes, err := eng.Tmux.ListPanes(winID)
	if err != nil || len(panes) == 0 {
		return fmt.Errorf("dock editor pane not found")
	}
	paneID := panes[0].ID

	// Determine the command to send based on whether it's a file or dir.
	info, err := os.Stat(absPath)
	var keys string
	if err == nil && info.IsDir() {
		keys = ":Ex " + absPath
	} else {
		keys = ":e " + absPath
	}

	// Focus the editor window, then send the command.
	_ = eng.Tmux.SelectWindow(winID)
	_ = eng.Tmux.SelectPane(paneID)
	return eng.Tmux.SendKeys(paneID, keys)
}

// runEditCreate launches the editor on a single bay.
// GUI editors launch and return (fire-and-forget). Terminal editors
// create a tracked surface in their own tmux pane via SurfaceAdd.
// Shared by `bay edit` and `bay new edit`.
func runEditCreate(eng *engine.Engine, target, editorOverride, splitDir string) error {
	dockName, bayID, err := resolveTarget(eng, target)
	if err != nil {
		return err
	}
	return runEditCreateAt(eng, dockName, bayID, editorOverride, splitDir)
}

// runEditCreateAt is runEditCreate for callers that already hold a
// resolved dock and bay target.
func runEditCreateAt(eng *engine.Engine, dockName, bayID, editorOverride, splitDir string) error {
	path, err := eng.Edit(dockName, bayID)
	if err != nil {
		return err
	}

	editorCmd, isGUI := resolveEditorWithOverride(eng.Config, editorOverride)
	if editorCmd == "" {
		return fmt.Errorf("no editor found; set [editor].command in config, or $VISUAL/$EDITOR")
	}

	if isGUI {
		// Fire and forget — GUI editors manage their own windows.
		_, err = launchEditor(editorCmd, isGUI, []string{path})
		if err != nil {
			return fmt.Errorf("launching editor: %w", err)
		}
		return nil
	}

	// Terminal editor: create a pane surface running the editor.
	fullCmd := editorCmd + " " + path
	return eng.SurfaceAdd(engine.SurfaceAddOptions{
		DockName: dockName,
		BayName:  bayID,
		Type:     manifest.SurfaceTypeEditor,
		Name:     "editor",
		Command:  fullCmd,
		SplitDir: splitDir,
	})
}

// knownEditors maps CLI command names to whether they're GUI apps.
var knownEditors = map[string]bool{
	"cursor": true,
	"code":   true,
	"zed":    true,
	"nvim":   false,
	"vim":    false,
}

// isGUIEditor checks config overrides first, then built-in knowledge.
func isGUIEditor(cfg *config.Config, cmd string) bool {
	base := baseCommand(cmd)
	if cfg.Editors != nil {
		if ec, ok := cfg.Editors[base]; ok {
			return ec.GUI
		}
	}
	return knownEditors[base]
}

// resolveEditor determines the editor command and whether it's GUI.
func resolveEditorWithOverride(cfg *config.Config, override string) (string, bool) {
	if override != "" {
		return override, isGUIEditor(cfg, override)
	}
	return resolveEditor(cfg)
}

func resolveEditor(cfg *config.Config) (command string, isGUI bool) {
	lookup := func(cmd string) (string, bool) {
		return cmd, isGUIEditor(cfg, cmd)
	}

	if cfg.DefaultEditor != "" {
		return lookup(cfg.DefaultEditor)
	}
	if v := os.Getenv("VISUAL"); v != "" {
		return lookup(v)
	}
	if v := os.Getenv("EDITOR"); v != "" {
		return lookup(v)
	}
	for _, name := range []string{"cursor", "code", "zed", "nvim", "vim"} {
		if _, err := exec.LookPath(name); err == nil {
			return lookup(name)
		}
	}
	return "", false
}

// baseCommand extracts the base command name from a potentially full path or
// command with args (e.g. "/usr/bin/code" -> "code", "nvim -u NONE" -> "nvim").
func baseCommand(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return cmd
	}
	// Take the last path component
	base := parts[0]
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	return base
}

// launchEditor launches the editor with the given paths.
// Returns the PID for GUI editors (0 for terminal editors).
func launchEditor(editorCmd string, isGUI bool, paths []string) (int, error) {
	args := append(strings.Fields(editorCmd), paths...)
	c := exec.Command(args[0], args[1:]...)

	if isGUI {
		// GUI editors: detach, don't wait
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Start(); err != nil {
			return 0, err
		}
		return c.Process.Pid, nil
	}

	// Terminal editors: attach to terminal
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return 0, c.Run()
}
