// Package cli implements the bay CLI using cobra.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	gitpkg "github.com/commonfabric/bay/internal/git"
	"github.com/commonfabric/bay/internal/picker"
	tmuxpkg "github.com/commonfabric/bay/internal/tmux"
	"github.com/spf13/cobra"
)

// cfgPath is set by cobra's --config persistent flag binding.
// Package-level var is required by cobra's StringVar API.
var cfgPath string

// cliVersion is the version string passed by cmd/bay. It must be set by
// NewRootCmd before monitor status assessment runs, so embedded callers
// should construct the CLI root before invoking monitor-status helpers.
var cliVersion = "dev"

// defaultPicker is the picker implementation used by navigation commands.
// Can be replaced for testing or to swap in an alternative (e.g. fzf).
var defaultPicker picker.Interface = &picker.Builtin{}

// bayPaths returns the standard paths, respecting the --config flag.
func bayPaths() config.Paths {
	p := config.DefaultPaths()
	if cfgPath != "" {
		p.ConfigFile = cfgPath
	}
	return p
}

// newEngine creates an Engine from the loaded config and real implementations.
// If no config file exists, returns an engine with an empty default config.
func newEngine() (*engine.Engine, error) {
	p := bayPaths()
	cfg, err := config.Load(p.ConfigFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg = config.DefaultConfig()
		} else {
			return nil, fmt.Errorf("loading config: %w", err)
		}
	}
	errs := cfg.Validate()
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "config warning: %s\n", e)
		}
	}

	t := tmuxpkg.NewReal()
	g := gitpkg.NewReal()

	return engine.New(cfg, p.ConfigFile, p.ManifestFile, p.ArchiveFile, t, g), nil
}

const bayHelpTemplate = `Bay — bay management for tmux and git worktrees.

Quick start:
  bay new                 create a bay
  bay surface new shell   create a shell surface
  bay agent               launch dock's default agent
  bay home                focus/create dock checkout shell
  bay go [query]          jump to a bay
  bay surface go [query]  jump to a surface
  bay ls                  list bays in this dock
  bay tree                see everything
  bay close [id]          close a bay
  bay edit                open editor

Run 'bay help' for all commands, or 'bay help <command>' for details.
`

// NewRootCmd creates the root bay command.
func NewRootCmd(version string) *cobra.Command {
	cliVersion = version
	root := &cobra.Command{
		Use:           "bay",
		Short:         "Multi-session bay management for tmux and git worktrees",
		SilenceUsage:  true,
		SilenceErrors: true,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprint(cmd.OutOrStdout(), bayHelpTemplate)
		},
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", "", "config file path (default ~/.config/bay/config.toml)")

	// Define command groups
	root.AddGroup(
		&cobra.Group{ID: "bay", Title: "Bays:"},
		&cobra.Group{ID: "surface", Title: "Surfaces:"},
		&cobra.Group{ID: "navigation", Title: "Navigation:"},
		&cobra.Group{ID: "infra", Title: "Infrastructure:"},
		&cobra.Group{ID: "other", Title: "Other:"},
	)

	// Bay commands
	bayNewCmd := newBayNewCmd()
	bayNewCmd.GroupID = "bay"
	bayCloseCmd := newBayCloseCmd()
	bayCloseCmd.GroupID = "bay"
	bayCleanReviewCmd := newBayCleanReviewCmd()
	bayCleanReviewCmd.GroupID = "bay"
	bayTidyCmd := newBayTidyCmd()
	bayTidyCmd.GroupID = "bay"
	bayShowCmd := newBayShowCmd()
	bayShowCmd.GroupID = "bay"
	bayRenameCmd := newBayRenameCmd()
	bayRenameCmd.GroupID = "bay"
	bayDescribeCmd := newTopDescribeCmd()
	bayDescribeCmd.GroupID = "bay"
	homeCmd := newHomeCmd()
	homeCmd.GroupID = "bay"
	bayLsCmd := newBayLsCmd()
	bayLsCmd.GroupID = "bay"
	prepareCmd := newPrepareCmd()
	prepareCmd.GroupID = "bay"
	bayGoCmd := newBayGoCmd()
	bayGoCmd.GroupID = "navigation"
	bayNextCmd := newBayNextCmd()
	bayNextCmd.GroupID = "navigation"
	bayPrevCmd := newBayPrevCmd()
	bayPrevCmd.GroupID = "navigation"

	// Surface commands (namespace + verbs + utility)
	surfaceCmd := newSurfaceCmd()
	surfaceCmd.GroupID = "surface"
	shellCmd := newShellCmd()
	shellCmd.GroupID = "surface"
	agentCmd := newAgentCmd()
	agentCmd.GroupID = "surface"
	editCmd := newEditCmd()
	editCmd.GroupID = "surface"
	topRestoreCmd := newTopRestoreCmd()
	topRestoreCmd.GroupID = "surface"

	// Navigation commands
	treeCmd := newTreeCmd()
	treeCmd.GroupID = "navigation"
	pwdCmd := newPwdCmd()
	pwdCmd.GroupID = "navigation"
	statusLineCmd := newStatusLineCmd()
	statusLineCmd.GroupID = "navigation"

	// Infrastructure commands
	dockCmd := newDockCmd()
	dockCmd.GroupID = "infra"
	setupCmd := newSetupCmd()
	setupCmd.GroupID = "infra"
	recoverCmd := newRecoverCmd()
	recoverCmd.GroupID = "infra"
	doctorCmd := newDoctorCmd()
	doctorCmd.GroupID = "infra"
	monitorCmd := newMonitorCmd()
	monitorCmd.GroupID = "infra"

	// Other commands.
	versionCmd := newVersionCmd(version)
	versionCmd.GroupID = "other"

	configCmd := newConfigCmd()
	configCmd.GroupID = "infra"
	prepareWorkerCmd := newPrepareWorkerCmd()
	describeWorkerCmd := newDescribeWorkerCmd()

	root.AddCommand(
		bayNewCmd,
		bayCloseCmd,
		bayCleanReviewCmd,
		bayTidyCmd,
		bayShowCmd,
		bayRenameCmd,
		bayDescribeCmd,
		homeCmd,
		bayLsCmd,
		prepareCmd,
		bayGoCmd,
		bayNextCmd,
		bayPrevCmd,
		surfaceCmd,
		shellCmd,
		agentCmd,
		editCmd,
		topRestoreCmd,
		treeCmd,
		pwdCmd,
		statusLineCmd,
		dockCmd,
		configCmd,
		setupCmd,
		recoverCmd,
		doctorCmd,
		monitorCmd,
		versionCmd,
		newPaletteCmd(), // hidden, for M-p keybinding
		prepareWorkerCmd,
		describeWorkerCmd,
	)

	agentGuideCmd := newAgentGuideCmd()
	agentGuideCmd.GroupID = "other"
	root.AddCommand(agentGuideCmd)

	RegisterCompletion(root)

	// Auto-start the background monitor on every command unless the
	// command (or one of its ancestors) opts out via the no-autostart
	// annotation. See monitor_autostart.go.
	installMonitorAutostart(root)

	return root
}
