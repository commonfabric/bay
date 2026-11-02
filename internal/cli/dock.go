package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	"github.com/spf13/cobra"
)

func newDockCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "dock",
		Aliases: []string{"dk"},
		Short:   "Manage docks (tmux session groups)",
	}

	cmd.AddCommand(
		newDockNewCmd(),
		newDockInitCmd(),
		newDockLsCmd(),
		newDockShowCmd(),
		newDockTreeCmd(),
		newDockRenameCmd(),
		newDockCloseCmd(),
		newDockRecoverCmd(),
		newDockSyncCmd(),
	)

	return cmd
}

func newDockInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init [name]",
		Short: "Set up bay awareness for a dock",
		Long: `Set up bay awareness for a dock.

Writes a CLAUDE.md pointing agents at 'bay agent-guide' into the dock's
worktree directory — Claude Code reads CLAUDE.md from ancestor
directories, so every bay picks it up automatically, with no bay files
inside the worktrees themselves.

Also appends the same pointer to agent project files in the dock
checkout (e.g. CLAUDE.local.md), but only when the repo already
gitignores them. Bay never edits .gitignore or leaves files git would
report as untracked or modified. Docks whose checkout is not a git
repository are left untouched.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			var name string
			if len(args) > 0 {
				name = args[0]
			} else {
				name, err = resolveCurrentDock(eng)
				if err != nil {
					return err
				}
			}

			initialized, err := eng.DockInit(name)
			if err != nil {
				return err
			}
			if !initialized {
				fmt.Printf("Dock %q checkout is not a git repository — nothing to set up.\n", name)
				return nil
			}
			fmt.Printf("Initialized bay awareness for dock %q.\n", name)
			return nil
		},
	}
}

func newDockNewCmd() *cobra.Command {
	var path, worktreeDir, agent, terminal string

	cmd := &cobra.Command{
		Use:   "new [name]",
		Short: "Create a new dock",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			return runDockNew(eng, name, path, worktreeDir, agent, terminal)
		},
	}

	cmd.Flags().StringVar(&path, "path", "", "checkout path (default: current directory)")
	cmd.Flags().StringVar(&worktreeDir, "worktree-dir", "", "worktree directory (default: <path>-worktrees)")
	cmd.Flags().StringVar(&agent, "agent", "", "default agent type")
	cmd.Flags().StringVar(&terminal, "terminal", "", "host terminal app (e.g. ghostty, iterm2)")

	return cmd
}

func runDockNew(eng *engine.Engine, name, path, worktreeDir, agent, terminal string) error {
	if name == "" {
		name = defaultDockName(path)
	}
	if err := eng.DockNew(name, path, worktreeDir, agent, terminal); err != nil {
		return err
	}
	if _, initErr := eng.DockInit(name); initErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: dock checkout setup failed: %v\n", initErr)
	}
	if err := eng.Home(name); err != nil {
		return fmt.Errorf("dock created, but home shell failed: %w", err)
	}
	fmt.Printf("Dock %q created.\n", name)
	return nil
}

func defaultDockName(path string) string {
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		return filepath.Base(cwd)
	}
	expanded := config.ExpandPath(path)
	if abs, err := filepath.Abs(expanded); err == nil {
		expanded = abs
	}
	return filepath.Base(expanded)
}

func newDockLsCmd() *cobra.Command {
	var jsonOutput bool
	var rowsOutput bool
	var shortOutput bool

	cmd := &cobra.Command{
		Use:     "ls [name]",
		Aliases: []string{"list"},
		Short:   "List docks and their bays (defaults to current dock)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			docks, err := eng.List()
			if err != nil {
				return err
			}

			focus := ListFocus{Kind: FocusAll}
			if len(args) > 0 {
				dock := findDock(docks, args[0])
				if dock == nil {
					return fmt.Errorf("dock %q not found", args[0])
				}
				focus = ListFocus{Kind: FocusDock, Dock: dock.Name}
			} else if sess, sessionErr := eng.Tmux.CurrentSession(); sessionErr == nil {
				if dock := findDock(docks, sess); dock != nil {
					focus = ListFocus{Kind: FocusDock, Dock: dock.Name}
				}
			}

			view := BuildListView(docks, ListViewOptions{Focus: focus})
			view.SetCurrentContext(eng)

			if jsonOutput {
				return printListViewJSON(view, rowsOutput)
			}

			fmt.Print(FormatListView(view, false, shortOutput))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	cmd.Flags().BoolVar(&rowsOutput, "rows", false, "output JSON as denormalized rows")
	cmd.Flags().BoolVarP(&shortOutput, "short", "s", false, "compact output without labels or key names")

	return cmd
}

func findDock(docks []engine.DockInfo, name string) *engine.DockInfo {
	for i := range docks {
		if docks[i].Name == name {
			return &docks[i]
		}
	}
	return nil
}

func newDockShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show [name]",
		Aliases: []string{"cat"},
		Short:   "Show dock details (defaults to current dock)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			var name string
			if len(args) > 0 {
				name = args[0]
			} else {
				session, err := eng.Tmux.CurrentSession()
				if err != nil {
					return fmt.Errorf("not in a tmux session — pass a dock name")
				}
				name = session
			}

			m, _ := eng.LoadManifest()
			if m == nil {
				return fmt.Errorf("dock %q not found", name)
			}
			mDock := m.FindDock(name)
			if mDock == nil {
				return fmt.Errorf("dock %q not found", name)
			}

			var rows []showRow
			rows = append(rows, showRow{"dock", name})
			if mDock.Path != "" {
				rows = append(rows, showRow{"path", mDock.Path})
				rows = append(rows, showRow{"worktree dir", mDock.EffectiveWorktreeDir()})
			}
			if agent := eng.Config.ResolvedDockAgent(name, mDock.Agent); agent != "" {
				rows = append(rows, showRow{"agent", agent})
				if agentArgs := eng.Config.ResolvedAgentArgs(name, agent, mDock.AgentArgs); len(agentArgs) > 0 {
					rows = append(rows, showRow{"agent args", strings.Join(agentArgs, " ")})
				}
				if launchArgs := eng.Config.ResolvedAgentLaunchArgs(name, agent); len(launchArgs) > 0 {
					rows = append(rows, showRow{"agent launch args", strings.Join(launchArgs, " ")})
				}
			}
			if terminal := eng.Config.ResolvedDockTerminal(name); terminal != "" {
				rows = append(rows, showRow{"terminal", terminal})
			}
			exists, _ := eng.Tmux.HasSession(name)
			if exists {
				rows = append(rows, showRow{"session", "running"})
			} else {
				rows = append(rows, showRow{"session", "not running"})
			}

			// Bay summary
			total := len(mDock.Bays)
			baySummary := fmt.Sprintf("%d", total)
			if total > 0 {
				merged := 0
				for _, bay := range mDock.Bays {
					if bay.IsMerged() {
						merged++
					}
				}
				if merged > 0 {
					baySummary += fmt.Sprintf(" (%d merged)", merged)
				}
			}
			rows = append(rows, showRow{"bays", baySummary})

			printAlignedRows(rows)
			return nil
		},
	}
}

func newDockTreeCmd() *cobra.Command {
	var longOutput bool

	cmd := &cobra.Command{
		Use:   "tree [name]",
		Short: "Tree view for a dock (default: current)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			var dockName string
			if len(args) > 0 {
				dockName = args[0]
			} else {
				sess, tmuxErr := eng.Tmux.CurrentSession()
				if tmuxErr != nil {
					return fmt.Errorf("not in a tmux session — pass a dock name")
				}
				dockName = sess
			}

			docks, err := eng.List()
			if err != nil {
				return err
			}

			m, _ := eng.LoadManifest()
			if m != nil {
				if dock := m.FindDock(dockName); dock == nil {
					return fmt.Errorf("dock %q not found", dockName)
				}
			}

			view := BuildListView(docks, ListViewOptions{
				Focus:     ListFocus{Kind: FocusDock, Dock: dockName},
				Recursive: true,
			})

			fmt.Print(FormatListView(view, longOutput, false))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&longOutput, "long", "l", false, "show extended details such as tmux IDs")

	return cmd
}

func newDockRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rename [old] <new>",
		Aliases: []string{"mv"},
		Short:   "Rename a dock (defaults to current)",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			var oldName, newName string
			if len(args) == 1 {
				dockName, _, err := eng.ResolveSelf()
				if err != nil {
					return fmt.Errorf("cannot determine current dock: %w", err)
				}
				oldName = dockName
				newName = args[0]
			} else {
				oldName = args[0]
				newName = args[1]
			}
			return eng.DockRename(oldName, newName)
		},
	}
}

func newDockCloseCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "close <name>",
		Aliases: []string{"rm"},
		Short:   "Close all bays in a dock",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return eng.DockClose(args[0], force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "force close even if bays are dirty")

	return cmd
}

func newDockRecoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recover <name>",
		Short: "Recover a single dock",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			result, err := eng.DockRecover(args[0])
			if err != nil {
				return err
			}
			if len(result.Recovered) == 0 {
				fmt.Printf("Dock %q: nothing to recover.\n", args[0])
			} else {
				fmt.Printf("Dock %q: recovered %s\n", args[0], strings.Join(result.Recovered, ", "))
			}
			printRecoveryWarnings(result.Warnings)
			fmt.Printf("Attach with: tmux attach -t %s\n", args[0])
			return nil
		},
	}
}

func newDockSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync [name]",
		Short: "Sync dock checkout files to existing bay worktrees",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			var name string
			if len(args) > 0 {
				name = args[0]
			} else {
				name, err = resolveCurrentDock(eng)
				if err != nil {
					return err
				}
			}

			count, err := eng.DockSync(name)
			if err != nil {
				return err
			}
			fmt.Printf("Synced %d worktree(s) for dock %q.\n", count, name)
			return nil
		},
	}
}
