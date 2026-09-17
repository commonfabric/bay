package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/nav"
	"github.com/commonfabric/bay/internal/picker"
	"github.com/spf13/cobra"
)

func newSurfaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "surface",
		Aliases: []string{"sf"},
		Short:   "Manage surfaces (agent, shell, cmd panes within a bay)",
	}

	cmd.AddCommand(
		newSurfaceNewCmd(),
		newSurfaceCloseCmd(),
		newSurfaceRestoreCmd(),
		newSurfaceLsCmd(),
		newSurfaceShowCmd(),
		newSurfaceRenameCmd(),
		newSurfaceGoCmd(),
		newSurfaceNextCmd(),
		newSurfacePrevCmd(),
	)

	return cmd
}

func newSurfaceNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new <kind>",
		Short: "Add a surface to a bay",
		Long: `Add a surface to a bay.

  bay sf new shell                       split a shell
  bay sf new shell logs --window         new tmux window named "logs"
  bay sf new agent claude                start an agent surface
  bay sf new agent codex --bay b1        agent in another bay
  bay sf new cmd "npm test" tests        run a command
  bay sf new edit                        open the editor`,
	}

	cmd.AddCommand(
		newTopNewShellCmd(),
		newTopNewAgentCmd(),
		newTopNewCmdCmd(),
		newTopNewEditCmd(),
	)

	return cmd
}

// surfaceNewOpts collects the options accepted by all surface-creation
// commands. Type is required; Name defaults to a per-type label.
// SplitDir is "h", "v", or "" (new tmux window).
// CLI callers default this to "v" unless --window is passed.
type surfaceNewOpts struct {
	Type     manifest.SurfaceType
	Agent    string // for SurfaceTypeAgent
	Command  string // for SurfaceTypeCmd
	Name     string // surface display name; defaults derived from Type
	SplitDir string
}

// validateSurfaceName rejects names containing a colon. Users who type
// `bay surface new shell b1:logs` expecting colon-path semantics would
// otherwise land a surface literally named "b1:logs" in the current bay.
func validateSurfaceName(name string) error {
	if strings.Contains(name, ":") {
		return fmt.Errorf("surface name %q cannot contain ':' (use --bay/--dock to specify a bay)", name)
	}
	return nil
}

// defaultCmdName derives a surface name from a command string.
// Uses the first word, and if it looks like a path, just the base name.
// Falls back to "cmd" if the command is empty.
func defaultCmdName(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "cmd"
	}
	return filepath.Base(fields[0])
}

// runSurfaceNew creates a new surface in (dockName, bayName). The caller is
// responsible for resolving (dockName, bayName) and setting opts.Type.
func runSurfaceNew(eng *engine.Engine, dockName, bayName string, opts surfaceNewOpts) error {
	if err := validateSurfaceName(opts.Name); err != nil {
		return err
	}

	agent := opts.Agent
	if opts.Type == manifest.SurfaceTypeAgent && agent == "" {
		agent = eng.DefaultAgent(dockName)
		if agent == "" {
			return fmt.Errorf("no agent specified and dock %q has no default agent", dockName)
		}
	}

	name := opts.Name
	if name == "" {
		switch opts.Type {
		case manifest.SurfaceTypeAgent:
			name = "agent"
		case manifest.SurfaceTypeCmd:
			name = defaultCmdName(opts.Command)
		default:
			name = "shell"
		}
	}
	return eng.SurfaceAdd(engine.SurfaceAddOptions{
		DockName: dockName,
		BayName:  bayName,
		Type:     opts.Type,
		Name:     name,
		Agent:    agent,
		Command:  opts.Command,
		SplitDir: opts.SplitDir,
	})
}

func newSurfaceCloseCmd() *cobra.Command {
	var bayFlag, dockFlag string
	var force bool

	cmd := &cobra.Command{
		Use:     "close <name>",
		Aliases: []string{"rm"},
		Short:   "Close a surface",
		Long: `Close a surface by name. Use 'self' to target the current surface.

  bay sf close monitor              close "monitor" in the current bay
  bay sf close b1:monitor           close "monitor" in bay b1
  bay sf close labs:b1:monitor      fully-qualified
  bay sf close monitor --bay b1     same as b1:monitor
  bay sf close self                 close the current pane's surface
  bay sf rm shell-2                 same thing with the rm alias

When invoked non-interactively, as from the Option+w tmux keybinding,
closing the last surface in a bay requires a quick second close
attempt. Interactive command-line invocations close the last surface on
the first command.

Closing an agent surface prompts for confirmation when stdin is a
terminal — agents carry valuable conversation context. Closing the final
home surface dismisses the dock UI/session after the same quick second
attempt. Use --force to skip close confirmations.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return runSurfaceClose(eng, args, bayFlag, dockFlag, force)
		},
	}

	cmd.Flags().StringVar(&bayFlag, "bay", "", "bay ID (disambiguates with --dock)")
	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (only valid with --bay or a bay prefix)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip close confirmations")

	return cmd
}

// runSurfaceClose closes a named surface. Requires an explicit name —
// `close` is destructive and we don't want a bare invocation to silently
// close the current pane. Use `bay sf close self` to target the current
// surface.
//
// Non-interactive last-surface closes require a quick second invocation to
// protect keybinding users from accidental bay teardown. Agent surfaces
// prompt for confirmation when stdin is a TTY (unless force is true). Other
// surface types close without prompting.
func runSurfaceClose(eng *engine.Engine, args []string, bayFlag, dockFlag string, force bool) error {
	if len(args) == 0 {
		if bayFlag != "" || dockFlag != "" {
			return fmt.Errorf("--bay/--dock require a surface name")
		}
		return fmt.Errorf("specify a surface name (or 'self' to close the current surface)")
	}

	// Handle dock:name syntax for dock-level surfaces.
	if sfName, ok := isDockSurface(args[0]); ok {
		session, err := eng.Tmux.CurrentSession()
		if err != nil {
			return fmt.Errorf("not in a tmux session")
		}
		return eng.DockSurfaceClose(session, sfName)
	}

	dockName, bayName, sName, err := resolveSurfaceArgOrSelf(eng, args[0], bayFlag, dockFlag)
	if err != nil {
		return err
	}

	// bayName == "" means this resolved to a dock surface (via self).
	if bayName == "" {
		return eng.DockSurfaceClose(dockName, sName)
	}

	// Two protections, neither of which fires with --force:
	//   - last-surface double-tap: for non-interactive invocations (tmux
	//     run-shell keybindings), closing the only surface in a bay
	//     tears down the visible pane (and triggers the orphan grace timer).
	//     Easy to fat-finger via M-w; require a second close attempt within
	//     a short window to confirm. Interactive CLI closes are allowed on the
	//     first command.
	//   - agent y/N: agent surfaces carry conversation context worth
	//     protecting on its own. Skipped when the last-surface check has
	//     already fired — one confirmation is enough.
	closeForce := force
	if !force {
		bay, err := eng.BayShow(dockName, bayName)
		if err != nil {
			return err
		}
		switch s := bay.FindSurface(sName); {
		case s == nil:
			// Surface not in manifest; let SurfaceClose surface the error.
		case bay.Type == manifest.BayTypeHome:
			dismisses, err := eng.SurfaceCloseWouldDismissDock(dockName, bayName, sName)
			if err != nil {
				return err
			}
			if dismisses {
				if !confirmLastHomeClose(homeCloseFlash(eng), dockName) {
					return nil
				}
				closeForce = true
			} else if s.Type == manifest.SurfaceTypeAgent {
				if !confirmAgentClose(s.Name) {
					fmt.Fprintln(os.Stderr, "not closing.")
					return nil
				}
			}
		case len(bay.Surfaces) == 1 && shouldConfirmLastSurfaceClose():
			if msg, err := lastSurfaceCloseRefusal(eng, bay); err != nil {
				return err
			} else if msg != "" {
				notifyFor(eng, msg, 5000)
				return nil
			}
			if !confirmLastSurfaceClose(eng.Tmux.DisplayMessage, dockName, bayName) {
				return nil
			}
		case s.Type == manifest.SurfaceTypeAgent:
			if !confirmAgentClose(s.Name) {
				fmt.Fprintln(os.Stderr, "not closing.")
				return nil
			}
		}
	}

	return eng.SurfaceClose(dockName, bayName, sName, closeForce)
}

func lastSurfaceCloseRefusal(eng *engine.Engine, bay *manifest.Bay) (string, error) {
	if bay.Type != manifest.BayTypeWorktree || bay.Path == "" {
		return "", nil
	}
	if _, statErr := os.Stat(bay.Path); statErr != nil {
		return "", nil
	}

	dirty, err := eng.HasBlockingDirtyChanges(bay)
	if err != nil {
		return "", err
	}
	label := engine.BayCompactLabel(bay)
	if dirty {
		return fmt.Sprintf("%s: bay kept (local changes may be work in progress). Option+Shift+W discards changes and closes.", label), nil
	}

	unpushed, err := eng.HasUnlandedCommits(bay)
	if err != nil {
		return "", fmt.Errorf("bay %q: could not verify push status: %w (use --force to override)", label, err)
	}
	if unpushed {
		return fmt.Sprintf("%s: bay kept (unlanded commits). Option+Shift+W closes anyway (commits stay on the branch).", label), nil
	}
	return "", nil
}

func newSurfaceRestoreCmd() *cobra.Command {
	var list bool

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore the most recently closed surface (undo-close)",
		Long: `Restore the most recently closed surface in the current dock.

bay keeps a per-dock LRU queue of the last 10 bay-initiated closes
and sync-discovered pane exits (for up to 1 hour). 'bay sf restore'
(or Option+Z in tmux) pops the most recent entry and recreates the
surface in its parent bay.

  bay sf restore             restore the most recent close
  bay sf restore --list      show the queue`,
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

// runSurfaceRestore handles `bay sf restore` and `bay restore`. Shared by
// the CLI and the Option+Z keybinding.
//
// Feedback is delivered via both tmux display-message (visible when invoked
// from run-shell, which detaches stderr) and stderr (visible when invoked
// from a CLI terminal; tmux's display-message call fails gracefully outside
// tmux). This dual channel is why close/restore feel symmetric — without
// the tmux toast, an Option+Z press that finds an empty queue would be a
// silent no-op and the user would keep pressing.
func runSurfaceRestore(eng *engine.Engine, list bool) error {
	dockName, err := resolveCurrentDock(eng)
	if err != nil {
		return err
	}

	entries, err := eng.ListClosedEntries(dockName)
	if err != nil {
		return err
	}
	// Sync so panes the user just exited (Ctrl-D, kill-pane) are stripped
	// and can be discovered for immediate Option+Z. Keep entries from this
	// sync below the queue entries bay already knew about: a sync-discovered
	// exit has no reliable close timestamp, so it must not jump known closes.
	discovered := eng.SyncAllForRestore()
	if len(discovered) > 0 {
		if err := eng.MoveClosedEntriesBelowKnown(dockName, discovered, entries); err != nil {
			return err
		}
	}

	if list {
		return printClosedQueue(eng, dockName)
	}

	_, restoreErr := eng.SurfaceRestore(dockName)
	switch {
	case restoreErr == nil:
		// No success toast: the restored pane appearing is the user-visible
		// feedback. A tmux display-message fired here (even async with
		// detached stdio) correlates with a visible stall in the new pane's
		// content rendering — tmux appears to defer pane redraws while a
		// status-line message is displaying. The empty-queue case below
		// still needs a toast because there's nothing visible otherwise.
		return nil
	case errors.Is(restoreErr, engine.ErrNothingToRestore):
		notify(eng, fmt.Sprintf("Nothing to restore in dock %q.", dockName))
		return nil
	default:
		return restoreErr
	}
}

// notify sends a message via tmux display-message AND stderr, so the user
// sees it whether they invoked the command via a keybinding (run-shell,
// detached stderr) or an interactive terminal (no tmux context).
//
// Uses DisplayMessageAsync so bay doesn't block on the tmux RPC — from a
// run-shell keybinding that has also just created a pane, a synchronous
// DisplayMessage delays run-shell's exit, which delays tmux's redraw of
// the freshly-created pane. Advisory toasts must never block visible work.
func notify(eng *engine.Engine, msg string) {
	notifyFor(eng, msg, 2500)
}

func notifyFor(eng *engine.Engine, msg string, durationMs int) {
	_ = eng.Tmux.DisplayMessageAsync(msg, durationMs)
	fmt.Fprintln(os.Stderr, msg)
}

func homeCloseFlash(eng *engine.Engine) flashFunc {
	return func(msg string, durationMs int) error {
		_ = eng.Tmux.DisplayMessage(msg, durationMs)
		fmt.Fprintln(os.Stderr, msg)
		return nil
	}
}

func printClosedQueue(eng *engine.Engine, dockName string) error {
	entries, err := eng.ListClosedEntries(dockName)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Printf("No recent closes in dock %q.\n", dockName)
		return nil
	}
	now := time.Now().Unix()
	for i, e := range entries {
		age := formatClosedAge(now - e.ClosedAt)
		switch e.Kind {
		case manifest.ClosedKindSurface:
			if e.Surface == nil {
				continue
			}
			s := e.Surface
			extra := ""
			if s.Agent != "" {
				extra = " agent=" + s.Agent
			} else if s.Command != "" {
				extra = " cmd=" + engine.TruncateTabName(s.Command, 40)
			}
			fmt.Printf("%d. %s surface %s/%s (%s)%s\n", i+1, age, s.Bay, s.Name, s.Type, extra)
		default:
			fmt.Printf("%d. %s %s\n", i+1, age, e.Kind)
		}
	}
	return nil
}

func formatClosedAge(secs int64) string {
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds ago", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm ago", secs/60)
	default:
		return fmt.Sprintf("%dh ago", secs/3600)
	}
}

func newSurfaceGoCmd() *cobra.Command {
	var nextWaiting bool

	cmd := &cobra.Command{
		Use:   "go [query]",
		Short: "Navigate to a surface within the current bay",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return surfaceGo(eng, args, nextWaiting)
		},
	}

	cmd.Flags().BoolVar(&nextWaiting, "next-waiting", false, "jump to next waiting surface")

	return cmd
}

func newSurfaceNextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "next",
		Short: "Switch to the next surface in the current bay",
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return surfaceCycle(eng, true)
		},
	}
}

func newSurfacePrevCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prev",
		Short: "Switch to the previous surface in the current bay",
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return surfaceCycle(eng, false)
		},
	}
}

func newSurfaceLsCmd() *cobra.Command {
	var jsonOutput bool
	var shortOutput bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List surfaces in the current bay",
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			dockName, bayName, err := eng.ResolveSelf()
			if err != nil {
				return fmt.Errorf("not in a bay")
			}

			docks, err := eng.List()
			if err != nil {
				return err
			}
			var surfaces []engine.SurfaceInfo
			for _, d := range docks {
				if d.Name != dockName {
					continue
				}
				for _, bay := range d.Bays {
					if bay.ID == bayName {
						surfaces = bay.Surfaces
						break
					}
				}
				break
			}

			if jsonOutput {
				data, err := json.MarshalIndent(surfaces, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			if len(surfaces) == 0 {
				fmt.Println("No surfaces in this bay.")
				return nil
			}

			currentSurface := ""
			if ctx, err := eng.CurrentContext(); err == nil && ctx.Dock == dockName && ctx.BayID == bayName {
				currentSurface = ctx.Surface
			}
			fmt.Print(FormatSurfaceList(surfaces, currentSurface, shortOutput))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	cmd.Flags().BoolVarP(&shortOutput, "short", "s", false, "compact output without labels or key names")

	return cmd
}

func newSurfaceShowCmd() *cobra.Command {
	var bayFlag, dockFlag string

	cmd := &cobra.Command{
		Use:     "show [name]",
		Aliases: []string{"cat"},
		Short:   "Show surface details (default: current)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return runSurfaceShow(eng, args, bayFlag, dockFlag)
		},
	}

	cmd.Flags().StringVar(&bayFlag, "bay", "", "bay ID (disambiguates with --dock)")
	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (only valid with --bay or a bay prefix)")

	return cmd
}

// runSurfaceShow prints details for a named surface. Accepts the `self`
// keyword, and a bare no-arg invocation defaults to the current pane's
// surface.
func runSurfaceShow(eng *engine.Engine, args []string, bayFlag, dockFlag string) error {
	target := "self"
	if len(args) > 0 {
		target = args[0]
	}
	dockName, bayName, sName, err := resolveSurfaceArgOrSelf(eng, target, bayFlag, dockFlag)
	if err != nil {
		return err
	}

	eng.SyncAll()
	bay, err := eng.BayShow(dockName, bayName)
	if err != nil {
		return err
	}

	s := bay.FindSurface(sName)
	if s == nil {
		return fmt.Errorf("surface %q not found in bay %q", sName, bayName)
	}

	rows := []showRow{
		{"surface", s.Name},
		{"type", string(s.Type)},
	}
	if s.Tmux != nil {
		if s.Tmux.WindowID != "" {
			rows = append(rows, showRow{"window", s.Tmux.WindowID})
		}
		if s.Tmux.PaneID != "" {
			rows = append(rows, showRow{"pane", s.Tmux.PaneID})
		}
	}
	if s.Agent != nil && *s.Agent != "" {
		rows = append(rows, showRow{"agent", *s.Agent})
	}
	if s.Command != nil && *s.Command != "" {
		rows = append(rows, showRow{"command", *s.Command})
	}
	printAlignedRows(rows)
	return nil
}

func newSurfaceRenameCmd() *cobra.Command {
	var bayFlag, dockFlag string

	cmd := &cobra.Command{
		Use:     "rename [old] <new>",
		Aliases: []string{"mv"},
		Short:   "Rename a surface (defaults to current)",
		Long: `Rename a surface. With one arg, renames the current surface.

  bay sf rename agent2                    rename current surface
  bay sf rename agent agent2              rename in current bay
  bay sf rename b1:agent agent2           rename agent in bay b1
  bay sf rename agent agent2 --bay b1     same as b1:agent`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				args = []string{"self", args[0]}
			}
			return runSurfaceRename(eng, args, bayFlag, dockFlag)
		},
	}

	cmd.Flags().StringVar(&bayFlag, "bay", "", "bay ID (disambiguates with --dock)")
	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (only valid with --bay or a bay prefix)")

	return cmd
}

// runSurfaceRename renames a surface. The first arg is the old name (which
// may include a bay prefix or be the `self` keyword); the second is the
// new name.
//
// Precondition: args must contain exactly two elements. Cobra's ExactArgs(2)
// enforces this in production; callers from tests should pass the same.
func runSurfaceRename(eng *engine.Engine, args []string, bayFlag, dockFlag string) error {
	dockName, bayName, oldName, err := resolveSurfaceArgOrSelf(eng, args[0], bayFlag, dockFlag)
	if err != nil {
		return err
	}
	return eng.SurfaceRename(dockName, bayName, oldName, args[1])
}

// surfaceGo implements the surface picker / direct jump logic.
func surfaceGo(eng *engine.Engine, args []string, nextWaiting bool) error {
	dockName, bayName, err := eng.ResolveSelf()
	if err != nil {
		return fmt.Errorf("not in a bay")
	}

	bay, err := eng.BayShow(dockName, bayName)
	if err != nil {
		return err
	}

	currentPaneID, _ := eng.Tmux.CurrentPaneID()
	waitingWindows, _ := eng.Tmux.WaitingOrBellWindowIDs(dockName)
	entries := nav.CollectSurfaces(bay, currentPaneID, waitingWindows)

	if len(entries) == 0 {
		return nil
	}

	if nextWaiting {
		target, _ := nav.NextWaitingSurface(entries)
		if target == nil {
			return nil
		}
		return focusSurface(eng, target, dockName, bayName)
	}

	// Filter by query.
	query := ""
	if len(args) > 0 {
		query = args[0]
	}
	if query != "" {
		entries = filterSurfaceEntries(entries, query)
	}

	switch len(entries) {
	case 0:
		return nil
	case 1:
		if entries[0].Current {
			return nil // already here
		}
		return focusSurface(eng, &entries[0], dockName, bayName)
	default:
		return pickSurface(eng, entries, dockName, bayName)
	}
}

// surfaceCycle moves to next/prev surface in the current bay and
// flashes the new position via the cycling indicator.
func surfaceCycle(eng *engine.Engine, forward bool) error {
	dockName, bayName, err := eng.ResolveSelf()
	if err != nil {
		return fmt.Errorf("not in a bay")
	}

	bay, err := eng.BayShow(dockName, bayName)
	if err != nil {
		return err
	}

	currentPaneID, _ := eng.Tmux.CurrentPaneID()
	waitingWindows, _ := eng.Tmux.WaitingOrBellWindowIDs(dockName)
	entries := nav.CollectSurfaces(bay, currentPaneID, waitingWindows)

	if len(entries) < 2 {
		return nil
	}

	var target *nav.SurfaceEntry
	if forward {
		target, _ = nav.NextSurface(entries)
	} else {
		target, _ = nav.PrevSurface(entries)
	}
	if err := focusSurface(eng, target, dockName, bayName); err != nil {
		return err
	}
	return nil
}

// focusSurface switches focus to a surface and records it as last-focused.
func focusSurface(eng *engine.Engine, entry *nav.SurfaceEntry, dockName, bayName string) error {
	if entry.WindowID != "" {
		if err := eng.Tmux.SelectWindow(entry.WindowID); err != nil {
			return err
		}
	}
	if entry.PaneID != "" {
		if err := eng.Tmux.SelectPane(entry.PaneID); err != nil {
			return err
		}
	}
	_ = eng.SetLastFocused(dockName, bayName, entry.ID)
	return nil
}

// pickSurface shows the built-in picker for surface selection.
func pickSurface(eng *engine.Engine, entries []nav.SurfaceEntry, dockName, bayName string) error {
	items := formatSurfaceItems(entries)
	currentIdx := 0
	for i, e := range entries {
		if e.Current {
			currentIdx = i
		}
	}

	selected, err := defaultPicker.Pick(items, picker.Options{Prompt: "surface> ", Selected: currentIdx})
	if err != nil || selected < 0 {
		return nil // cancelled
	}
	return focusSurface(eng, &entries[selected], dockName, bayName)
}

// formatSurfaceItems formats surface entries with aligned columns.
func formatSurfaceItems(entries []nav.SurfaceEntry) []picker.Item {
	maxName, maxType := 0, 0
	for _, e := range entries {
		if len(e.Name) > maxName {
			maxName = len(e.Name)
		}
		if len(e.Type) > maxType {
			maxType = len(e.Type)
		}
	}
	items := make([]picker.Item, len(entries))
	for i, e := range entries {
		s := fmt.Sprintf("%-*s  %-*s", maxName, e.Name, maxType, e.Type)
		if e.Current {
			s += "  *"
		}
		if e.Waiting {
			s += "  WAITING"
		}
		items[i] = picker.Item{Display: s, Value: i}
	}
	return items
}

// filterSurfaceEntries filters surfaces by name or type substring.
func filterSurfaceEntries(entries []nav.SurfaceEntry, query string) []nav.SurfaceEntry {
	q := strings.ToLower(query)
	var result []nav.SurfaceEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), q) ||
			strings.Contains(strings.ToLower(e.Type), q) {
			result = append(result, e)
		}
	}
	return result
}
