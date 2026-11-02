package cli

import (
	"fmt"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/spf13/cobra"
)

// topNewCmdSpec describes the differences between the shell/agent/cmd
// subcommands of `bay surface new`. Everything else (flags, RunE shape, bay
// resolution) is identical and handled by newTopNewSurfaceCmd.
type topNewCmdSpec struct {
	use       string
	short     string
	long      string
	args      cobra.PositionalArgs
	buildOpts func(args []string, splitDir string, window, pane bool) (surfaceNewOpts, error)
}

func newTopNewSurfaceCmd(spec topNewCmdSpec) *cobra.Command {
	var bayFlag, dockFlag, splitDir string
	var window, pane bool

	cmd := &cobra.Command{
		Use:   spec.use,
		Short: spec.short,
		Long:  spec.long,
		Args:  spec.args,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := spec.buildOpts(args, splitDir, window, pane)
			if err != nil {
				return err
			}
			eng, err := newEngine()
			if err != nil {
				return err
			}
			dockName, bayName, err := resolveSurfaceBay(eng, bayFlag, dockFlag)
			if err != nil {
				return err
			}
			return runSurfaceNew(eng, dockName, bayName, opts)
		},
	}

	cmd.Flags().StringVar(&bayFlag, "bay", "", "bay ID (defaults to current)")
	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (with --bay to disambiguate)")
	cmd.Flags().StringVar(&splitDir, "split", "", "split direction (h or v)")
	cmd.Flags().BoolVar(&pane, "pane", false, "split into current window (default; shorthand for --split v)")
	cmd.Flags().BoolVar(&window, "window", false, "open as a new tmux window")

	return cmd
}

// shellSurfaceOpts builds surfaceNewOpts for shell-creation commands. Shared
// by `bay surface new shell` and the top-level `bay shell` shortcut.
func shellSurfaceOpts(args []string, splitDir string, window, pane bool) (surfaceNewOpts, error) {
	opts := surfaceNewOpts{
		Type:     manifest.SurfaceTypeShell,
		SplitDir: resolveSplit(splitDir, window, pane),
	}
	if len(args) > 0 {
		if err := validateSurfaceName(args[0]); err != nil {
			return opts, err
		}
		opts.Name = args[0]
	}
	return opts, nil
}

// agentSurfaceOpts builds surfaceNewOpts for agent-creation commands. Shared
// by `bay surface new agent` and the top-level `bay agent` shortcut.
func agentSurfaceOpts(args []string, splitDir string, window, pane bool) (surfaceNewOpts, error) {
	opts := surfaceNewOpts{
		Type:     manifest.SurfaceTypeAgent,
		SplitDir: resolveSplit(splitDir, window, pane),
	}
	if len(args) > 0 {
		opts.Agent = args[0]
	}
	if len(args) > 1 {
		if err := validateSurfaceName(args[1]); err != nil {
			return opts, err
		}
		opts.Name = args[1]
	}
	return opts, nil
}

func newTopNewShellCmd() *cobra.Command {
	return newTopNewSurfaceCmd(topNewCmdSpec{
		use:       "shell [name]",
		short:     "Create a shell surface",
		args:      cobra.MaximumNArgs(1),
		buildOpts: shellSurfaceOpts,
	})
}

func newTopNewAgentCmd() *cobra.Command {
	return newTopNewSurfaceCmd(topNewCmdSpec{
		use:   "agent [type] [name]",
		short: "Create an agent surface",
		long: `Create an agent surface. The agent type is optional — if omitted,
uses the dock's default agent.

  bay surface new agent                        dock's default agent as a split pane
  bay surface new agent claude                 specific agent
  bay surface new agent codex codex-debug      specific agent with custom name
  bay surface new agent --bay b1               in another bay
  bay surface new agent --window               as a new tmux window`,
		args:      cobra.MaximumNArgs(2),
		buildOpts: agentSurfaceOpts,
	})
}

func newTopNewCmdCmd() *cobra.Command {
	return newTopNewSurfaceCmd(topNewCmdSpec{
		use:   `cmd "<command>" [name]`,
		short: "Create a cmd surface running a command",
		long: `Create a surface that runs an arbitrary command.

  bay surface new cmd "npm test"
  bay surface new cmd "npm test" tests
  bay surface new cmd "tail -f log.txt" --window`,
		args: cobra.RangeArgs(1, 2),
		buildOpts: func(args []string, splitDir string, window, pane bool) (surfaceNewOpts, error) {
			opts := surfaceNewOpts{
				Type:     manifest.SurfaceTypeCmd,
				Command:  args[0],
				SplitDir: resolveSplit(splitDir, window, pane),
			}
			if len(args) > 1 {
				if err := validateSurfaceName(args[1]); err != nil {
					return opts, err
				}
				opts.Name = args[1]
			}
			return opts, nil
		},
	})
}

func newTopNewEditCmd() *cobra.Command {
	var bayFlag, dockFlag, editorFlag, splitDir string
	var window, pane bool

	cmd := &cobra.Command{
		Use:   "edit [bay]",
		Short: "Launch a bay-scoped editor surface",
		Long: `Launch the editor on a specific bay directory (creates a tracked
editor surface). For a dock-scoped editor covering all bays,
use 'bay edit' instead.

  bay surface new edit                       current bay
  bay surface new edit b1                    specific bay
  bay surface new edit --bay b1              same thing with a flag
  bay surface new edit --editor vim          use a specific editor this time
  bay surface new edit --window              open as a new tmux window`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			target, err := editTargetFromArgs(args, bayFlag, dockFlag)
			if err != nil {
				return err
			}
			return runEditCreate(eng, target, editorFlag, resolveSplit(splitDir, window, pane))
		},
	}

	cmd.Flags().StringVar(&bayFlag, "bay", "", "bay ID (defaults to current)")
	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (with --bay to disambiguate)")
	cmd.Flags().StringVar(&editorFlag, "editor", "", "editor command (overrides config for this invocation)")
	cmd.Flags().StringVar(&splitDir, "split", "", "split direction (h or v)")
	cmd.Flags().BoolVar(&pane, "pane", false, "split into current window (default; shorthand for --split v)")
	cmd.Flags().BoolVar(&window, "window", false, "open as a new tmux window")

	return cmd
}

// editTargetFromArgs builds a resolveTarget-compatible bay target string from
// a positional arg plus --bay/--dock flags. Positional and flags are mutually
// exclusive (each is a way to name a bay); --dock without --bay (or without a
// positional) is an error.
func editTargetFromArgs(args []string, bayFlag, dockFlag string) (string, error) {
	if len(args) > 0 {
		if bayFlag != "" || dockFlag != "" {
			return "", fmt.Errorf("cannot combine positional bay with --bay/--dock")
		}
		return args[0], nil
	}
	if bayFlag == "" {
		if dockFlag != "" {
			return "", fmt.Errorf("--dock requires --bay or a positional bay")
		}
		return "self", nil
	}
	if dockFlag != "" {
		return dockFlag + ":" + bayFlag, nil
	}
	return bayFlag, nil
}

// resolveSurfaceBay resolves the (dock, bay) for a surface-creation
// command. With no flags it uses the current bay; with --bay it looks up the
// bay by ID (with --dock to disambiguate).
func resolveSurfaceBay(eng *engine.Engine, bayFlag, dockFlag string) (string, string, error) {
	if bayFlag == "" && dockFlag == "" {
		return eng.ResolveSelf()
	}
	if bayFlag == "" {
		return "", "", fmt.Errorf("--dock requires --bay")
	}
	if dockFlag != "" {
		return eng.ResolveBay(dockFlag + ":" + bayFlag)
	}
	return resolveBareBay(eng, bayFlag)
}

// newTopRestoreCmd is `bay restore` — top-level alias for `bay sf restore`.
func newTopRestoreCmd() *cobra.Command {
	var list bool

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore the most recently closed surface (undo-close)",
		Long: `Restore the most recently closed surface in the current dock.

bay keeps a per-dock LRU queue of the last 10 bay-initiated closes
and sync-discovered pane exits (for up to 1 hour). 'bay restore'
(or Option+Z in tmux) pops the most recent entry and recreates the
surface in its parent bay.

  bay restore             restore the most recent close
  bay restore --list      show the queue`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return runSurfaceRestore(eng, list)
		},
	}

	cmd.Flags().BoolVar(&list, "list", false, "show the undo-close queue without restoring")

	return cmd
}

func newTopDescribeCmd() *cobra.Command {
	var dockFlag string
	var clear, edit bool

	cmd := &cobra.Command{
		Use:   "describe [id] [<description>]",
		Short: "Read or set the current bay's description",
		Long: `Read or set a free-form description for a bay. The first line
is a short label (cap 80) shown in the picker, bay ls, bay tree, and
the M-/ flash. Optional trailing lines (separated from the first by a
blank line, commit-message style) are context notes surfaced via the
M-? popup — useful for returning to a bay after working elsewhere.

  bay describe                          print current bay's description
  bay describe "Login flow fixes"       set current bay's description
  bay describe b1 "Login fixes"         set by bay ID
  bay describe --edit                   open $EDITOR to edit the description
  bay describe --clear                  clear current bay's description`,
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDescribe(args, dockFlag, clear, edit)
		},
	}

	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (disambiguates a bare bay ID)")
	cmd.Flags().BoolVar(&clear, "clear", false, "clear the description")
	cmd.Flags().BoolVar(&edit, "edit", false, "open $EDITOR to edit the description (for multi-line bodies)")

	return cmd
}
