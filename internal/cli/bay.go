package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/nav"
	"github.com/commonfabric/bay/internal/picker"
	"github.com/commonfabric/bay/internal/prepare"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newBayNewCmd() *cobra.Command {
	var opts engine.BayNewOptions
	var shell bool
	var quiet bool
	var dockFlag string

	cmd := &cobra.Command{
		Use:   "new [name]",
		Short: "Create a new bay",
		Long: `Create a new bay in a dock.

  bay new                          create in current dock, auto-named
  bay new login-bug                create with display name "login-bug"
  bay new --branch fix/login-bug   checkout or create a git branch
  bay new --dock labs              create in a specific dock`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			// Positional is the bay's display name.
			if len(args) > 0 {
				opts.Name = args[0]
			}
			if manifest.IsReservedBayID(opts.Name) {
				return fmt.Errorf("home is reserved for the dock checkout; use `bay home`")
			}

			// Resolve dock: explicit --dock > current checkout/session > auto-bootstrap.
			// resolveCurrentDock prefers the CWD's dock over the tmux
			// session, so `bay new` in dock Y's checkout targets Y even
			// when attached to dock X's session.
			opts.Dock = dockFlag
			if opts.Dock == "" {
				if dockName, resolveErr := resolveCurrentDock(eng); resolveErr == nil {
					opts.Dock = dockName
				}
			}
			if opts.Dock == "" {
				dockName, bootstrapErr := autoBootstrap(eng, quiet)
				if bootstrapErr != nil {
					return bootstrapErr
				}
				opts.Dock = dockName
			}

			// Shell-first default: if neither --agent nor --shell was
			// explicitly passed, default to shell mode.
			if !cmd.Flags().Changed("agent") && !cmd.Flags().Changed("shell") {
				opts.Shell = true
			} else {
				opts.Shell = shell
			}

			// Bare --agent: use dock's default agent.
			// --agent <name>: use that specific agent.
			opts.RequireAgent = cmd.Flags().Changed("agent")
			if opts.Agent == "default" {
				opts.Agent = "" // resolve to dock/global default
			}

			bay, err := eng.BayNew(opts)
			if err != nil {
				return err
			}
			if !quiet {
				fmt.Printf("Created %s:%s\n", opts.Dock, engine.BayCompactLabel(bay))
				printPrepareStartedSummary(eng, opts.Dock, bay.ID)
				currentSession, tmuxErr := eng.Tmux.CurrentSession()
				if tmuxErr != nil {
					fmt.Printf("\nAttach with:\n  tmux attach -t %s\n", opts.Dock)
				} else if currentSession != opts.Dock {
					fmt.Printf("\nSwitch with:\n  tmux switch-client -t %s\n", opts.Dock)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (defaults to current checkout, else tmux session)")
	cmd.Flags().StringVar(&opts.Dir, "dir", "", "external directory (creates external bay)")
	cmd.Flags().StringVar(&opts.Agent, "agent", "", "agent type (bare --agent uses dock default; use --agent=TYPE for a specific type)")
	cmd.Flags().BoolVar(&shell, "shell", false, "open shell instead of agent")
	cmd.Flags().StringVar(&opts.Branch, "branch", "", "git branch to checkout (creates it if new)")
	cmd.Flags().StringVar(&opts.Description, "description", "", "short description shown in picker/ls/tree (~40 chars)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress output")
	cmd.Flags().Lookup("agent").NoOptDefVal = "default"

	return cmd
}

func printPrepareStartedSummary(eng *engine.Engine, dockName, bayID string) {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	plan, err := eng.PreparePlan(dockName, bayID)
	if err != nil || len(plan.Steps) == 0 {
		return
	}
	fmt.Printf("prepare started: %s; follow with 'bay prepare --log', stop with 'bay prepare --kill'\n", strings.Join(prepare.StepNames(plan.Steps), ","))
}

func newBayCloseCmd() *cobra.Command {
	var force, tap, clean, done, all, dryRun bool
	var dockFlag string

	cmd := &cobra.Command{
		Use:     "close [id]",
		Aliases: []string{"rm"},
		Short:   "Close a bay and all its surfaces",
		Long: `Close a bay and all its windows. Pass "self" to close the current
bay, or use --done/--clean/--all to batch-close bays.

  bay close b1               close a specific bay
  bay close labs:b1          dock-qualified
  bay close b1 --dock labs   same, with flag
  bay close self             close the current bay
  bay close home             close home surfaces; final home dismisses dock UI after confirmation
  bay close --done           close bays that are not dirty or pending
  bay close --clean          close all non-dirty bays
  bay close --all            close all bays, then home after confirmation if final
  bay close --done --dry-run preview what --done would close

The Option+Shift+W tmux keybinding runs 'bay close self --force --tap':
--tap holds the close until a quick second invocation, so the keybinding
needs a deliberate double-tap before uncommitted changes are discarded.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			batchFlags := 0
			for _, enabled := range []bool{clean, done, all} {
				if enabled {
					batchFlags++
				}
			}
			if batchFlags > 1 {
				return fmt.Errorf("use only one of --done, --clean, or --all")
			}
			if batchFlags > 0 && len(args) > 0 {
				return fmt.Errorf("batch close flags do not take a bay ID; use bay close <id> or bay close --done/--clean/--all")
			}
			if tap && batchFlags > 0 {
				return fmt.Errorf("--tap applies to a single bay close, not --done/--clean/--all")
			}

			eng, err := newEngine()
			if err != nil {
				return err
			}

			if clean || done || all {
				dockName := dockFlag
				if dockName == "" {
					sess, tmuxErr := eng.Tmux.CurrentSession()
					if tmuxErr == nil {
						m, _ := eng.LoadManifest()
						if m != nil && m.FindDock(sess) != nil {
							dockName = sess
						}
					}
				}

				// Exclude the current bay so batch close doesn't kill
				// the session the user is running from.
				var exclude []string
				if ctx, ctxErr := eng.CurrentContext(); ctxErr == nil && ctx.BayID != "" {
					exclude = append(exclude, ctx.BayID)
				}

				var closed, skipped []string
				var closeErr error
				if all {
					closed, skipped, closeErr = runBayCloseAll(eng, dockName, force, dryRun)
				} else if done {
					closed, skipped, closeErr = eng.BayCloseDone(dockName, force, dryRun, exclude...)
				} else {
					closed, skipped, closeErr = eng.BayCloseClean(dockName, force, dryRun, exclude...)
				}

				verb := "Closed"
				if dryRun {
					verb = "Would close"
				}
				for _, c := range closed {
					fmt.Printf("%s %s\n", verb, c)
				}
				for _, s := range skipped {
					fmt.Printf("Skipped %s\n", s)
				}
				if len(skipped) > 0 && !force && !dryRun {
					fmt.Println("\nUse --force to close dirty bays.")
				}
				return closeErr
			}

			if len(args) == 0 {
				return fmt.Errorf("specify a bay to close (bay close <id>), or use --done / --clean / --all")
			}

			dockName, bayID, err := resolveBayArg(eng, args[0], dockFlag)
			if err != nil {
				return err
			}

			return runBayClose(eng, dockName, bayID, force, tap)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "force close even if dirty; skip home dismissal confirmation")
	cmd.Flags().BoolVar(&tap, "tap", false, "hold the close until a quick second invocation (keybinding double-tap guard)")
	cmd.Flags().BoolVar(&done, "done", false, "close bays that are not dirty or pending")
	cmd.Flags().BoolVar(&clean, "clean", false, "close all non-dirty bays")
	cmd.Flags().BoolVar(&all, "all", false, "close all bays, then home after confirmation if final")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview what --done/--clean/--all would close")
	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (disambiguates a bare bay ID)")

	return cmd
}

func runBayClose(eng *engine.Engine, dockName, bayID string, force, tap bool) error {
	if tap && !confirmBayCloseTap(homeCloseFlash(eng), dockName, bayID, func() string {
		return bayCloseTapMessage(eng, dockName, bayID, force)
	}) {
		return nil
	}
	closeForce := force
	if !force {
		dismisses, err := eng.BayCloseWouldDismissDock(dockName, bayID)
		if err != nil {
			return err
		}
		if dismisses {
			if !confirmLastHomeClose(homeCloseFlash(eng), dockName) {
				return nil
			}
			closeForce = true
		}
	}
	return eng.BayClose(dockName, bayID, closeForce)
}

// bayCloseTapMessage names what a confirmed close will do, so the
// first tap of Option+Shift+W states the blast radius before anything
// is destroyed. Probes are best-effort: on error it falls back to
// generic wording — the tap gate itself still applies.
func bayCloseTapMessage(eng *engine.Engine, dockName, bayID string, force bool) string {
	bay, err := eng.BayShow(dockName, bayID)
	if err != nil {
		return fmt.Sprintf("press again to close %q", bayID)
	}
	if manifest.IsHomeBay(bay) {
		return homeCloseConfirmMessage(dockName)
	}
	label := engine.BayCompactLabel(bay)
	if force && bay.Path != "" {
		if _, statErr := os.Stat(bay.Path); statErr == nil {
			if dirty, err := eng.HasBlockingDirtyChanges(bay); err == nil && dirty {
				return fmt.Sprintf("press again to close %q and discard its uncommitted changes", label)
			}
			if unpushed, err := eng.HasUnlandedCommits(bay); err == nil && unpushed {
				return fmt.Sprintf("press again to close %q — unlanded commits stay on the local branch", label)
			}
		}
	}
	return fmt.Sprintf("press again to close %q", label)
}

func runBayCloseAll(eng *engine.Engine, dockName string, force, dryRun bool) ([]string, []string, error) {
	if dockName == "" {
		return nil, nil, fmt.Errorf("bay close --all requires a dock context or --dock")
	}
	homeDismissForce := force
	if !force && !dryRun {
		dismisses, err := eng.BayCloseAllWouldDismissDock(dockName)
		if err != nil {
			return nil, nil, err
		}
		if dismisses {
			if !confirmLastHomeClose(homeCloseFlash(eng), dockName) {
				return nil, nil, nil
			}
			homeDismissForce = true
		}
	}
	return eng.BayCloseAll(dockName, force, homeDismissForce, dryRun)
}

func newBayCleanReviewCmd() *cobra.Command {
	var dockFlag string

	cmd := &cobra.Command{
		Use:    "clean-review [id]",
		Hidden: true,
		Short:  "Clear review changes from a bay",
		Long: `Clear dirty review changes from a bay only when bay can verify the
whole worktree exactly matches a recoverable git ref.

  bay clean-review self
  bay clean-review b1
  bay clean-review b1 --dock labs`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			var dockName, bayID string
			if len(args) == 0 {
				dockName, bayID, err = eng.ResolveSelf()
			} else {
				dockName, bayID, err = resolveBayArg(eng, args[0], dockFlag)
			}
			if err != nil {
				return err
			}

			ref, err := eng.BayCleanReview(dockName, bayID)
			if err != nil {
				return err
			}
			if ref == "" {
				fmt.Printf("%s:%s already clean\n", dockName, bayID)
			} else {
				fmt.Printf("Cleaned review changes in %s:%s (matched %s)\n", dockName, bayID, ref)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (disambiguates a bare bay ID)")
	return cmd
}

func newBayTidyCmd() *cobra.Command {
	var dockFlag string

	cmd := &cobra.Command{
		Use:   "tidy [id]",
		Short: "Detach a merged bay back to a clean base",
		Long: `Return a bay's worktree to a clean detached HEAD at origin/<default>
and delete its local branch. Run it after the bay's PR has merged to reset
the worktree for reuse — it detaches at the remote ref rather than checking
out the default branch (which the dock root already holds), so it never hits
the "branch is already checked out" error.

  bay tidy                 tidy the current bay
  bay tidy b1              tidy a specific bay
  bay tidy b1 --dock labs  dock-qualified

Refuses if the worktree is dirty or has commits that are neither pushed nor
merged, so no work is lost.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			var dockName, bayID string
			if len(args) == 0 {
				dockName, bayID, err = eng.ResolveSelf()
			} else {
				dockName, bayID, err = resolveBayArg(eng, args[0], dockFlag)
			}
			if err != nil {
				return err
			}

			res, err := eng.BayTidy(dockName, bayID)
			if err != nil {
				return err
			}
			if res.AlreadyDetached {
				fmt.Printf("%s:%s already detached at origin/%s\n", dockName, bayID, res.DefaultBranch)
				return nil
			}
			msg := fmt.Sprintf("Tidied %s:%s — detached at origin/%s", dockName, bayID, res.DefaultBranch)
			if res.DeletedBranch != "" {
				msg += fmt.Sprintf(", deleted branch %s", res.DeletedBranch)
			}
			fmt.Println(msg)
			return nil
		},
	}
	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (disambiguates a bare bay ID)")
	return cmd
}

func newBayShowCmd() *cobra.Command {
	var jsonOutput, short, plain, flash, popup, popupBody bool
	var dockFlag string

	cmd := &cobra.Command{
		Use:     "show [id]",
		Aliases: []string{"cat"},
		Short:   "Show bay details (default: current)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			target := "self"
			if len(args) > 0 {
				target = args[0]
			}
			dockName, bayID, err := resolveBayArg(eng, target, dockFlag)
			if err != nil {
				if short || flash || popup || popupBody {
					// Swallow the error so the keybindings leave the
					// status bar clean instead of flashing a traceback
					// when invoked outside a bay.
					return nil
				}
				return err
			}

			if popup {
				// Orchestrator side: open a tmux popup that renders the
				// full description via --popup-body. Resolving the
				// bay now (rather than inside the popup) lets the
				// popup inherit an explicit dock/name and avoids
				// re-resolving against a possibly-different CWD.
				return eng.Tmux.DisplayPopup(fmt.Sprintf("bay show %s --popup-body --dock %s", bayID, dockName))
			}

			if popupBody {
				bay, bayErr := eng.BayShow(dockName, bayID)
				if bayErr != nil {
					return nil
				}
				_ = eng.MarkPRCheckStale(dockName, bayID)
				renderPopupBody(bay)
				return nil
			}

			if short || flash {
				bay, bayErr := eng.BayShow(dockName, bayID)
				if bayErr != nil {
					return nil
				}
				// User interest signal: if they're looking at this
				// bay and its PR is still missing, ask the monitor
				// to re-check on its next tick (~3s) rather than waiting
				// for the full PR TTL to elapse.
				_ = eng.MarkPRCheckStale(dockName, bayID)
				out := formatBayShort(bay)
				if flash {
					// Route via tmux display-message so the status bar
					// updates synchronously on keypress. Using tmux's
					// `#(cmd)` format cache (the earlier approach)
					// meant the first keypress showed empty and later
					// keypresses showed stale cached output.
					return eng.Tmux.DisplayMessage(stripANSI(out), 5000)
				}
				if plain {
					out = stripANSI(out)
				}
				fmt.Println(out)
				return nil
			}

			eng.SyncAll()
			bayInfo, err := eng.BayInfoByName(dockName, bayID)
			if err != nil {
				return err
			}
			// If the full read still has no PR after SyncAll, mark stale
			// so the next monitor tick re-queries bypassing the TTL.
			if bayInfo.PR == "" && bayInfo.Branch != "" {
				_ = eng.MarkPRCheckStale(dockName, bayID)
			}
			m, _ := eng.LoadManifest()
			if m != nil {
				if dock := m.FindDock(dockName); dock != nil && bayInfo.DefaultAgent == "" {
					bayInfo.DefaultAgent = dock.Agent
				}
			}

			if jsonOutput {
				out := map[string]interface{}{
					"name":          bayInfo.Name,
					"description":   bayInfo.Description,
					"dock":          dockName,
					"type":          bayInfo.Type,
					"path":          bayInfo.Path,
					"branch":        bayInfo.Branch,
					"pr":            bayInfo.PR,
					"dirty":         bayInfo.Dirty,
					"pending":       bayInfo.Pending,
					"sync_status":   bayInfo.SyncStatus,
					"default_agent": bayInfo.DefaultAgent,
					"surfaces":      bayInfo.Surfaces,
				}
				data, err := json.MarshalIndent(out, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Print(FormatBayShow(dockName, bayInfo, false))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	cmd.Flags().BoolVarP(&short, "short", "s", false, "one-line bay summary (name — description — branch — #PR)")
	cmd.Flags().BoolVar(&plain, "plain", false, "plain-text output (no ANSI colors); useful with --short for tmux display-message")
	cmd.Flags().BoolVar(&flash, "flash", false, "send the short summary to tmux display-message for 5s (used by the M-/ binding)")
	cmd.Flags().BoolVar(&popup, "popup", false, "open a tmux popup showing the full description (used by the M-? binding)")
	cmd.Flags().BoolVar(&popupBody, "popup-body", false, "render the popup body and wait for keypress (invoked inside the popup)")
	_ = cmd.Flags().MarkHidden("popup-body")
	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (disambiguates a bare bay ID)")

	return cmd
}

// formatBayShort builds a one-line bay summary:
// `compact-label — description — branch — #PR`, skipping empty fields.
// The branch is omitted when it equals the bay name — bay
// auto-derives bay names from branches, so in the common case
// they match and showing both just duplicates the identifier.
// Used by `bay show --short` (and the M-/ flash binding); the
// description is truncated to its first line, since this output
// must fit on a single tmux status row.
func formatBayShort(bay *manifest.Bay) string {
	if bay == nil {
		return ""
	}
	var parts []string
	label := engine.BayCompactLabel(bay)
	if label != "" {
		parts = append(parts, label)
	}
	if desc := engine.DescriptionFirstLine(bay.Description); desc != "" {
		parts = append(parts, desc)
	}
	if !manifest.IsHomeBay(bay) && bay.Worktree != nil {
		if bay.Worktree.Branch != "" && bay.Worktree.Branch != bay.Name && bay.Worktree.Branch != label {
			parts = append(parts, bay.Worktree.Branch)
		}
		if bay.Worktree.PR != "" {
			parts = append(parts, "PR#"+bay.Worktree.PR)
		}
	}
	return strings.Join(parts, dim(" — "))
}

// renderPopupBody prints the bay's full description (including body)
// inside a tmux popup and waits for a keypress to dismiss. Word-wraps body
// text to the pty width; if the rendered body would overflow, clipBody
// replaces the tail with a "+N more lines" hint. The dismiss hint is
// absolute-positioned at the bottom-right of the popup so it stays
// visually anchored regardless of how much body content was shown.
func renderPopupBody(bay *manifest.Bay) {
	if bay == nil {
		return
	}
	fd := int(os.Stdin.Fd())
	width, height := popupDims(fd)
	content := buildPopupContent(bay, width, height)

	fmt.Print(content)

	const dismiss = "(any key to dismiss)"
	if height > 0 {
		col := width - utf8.RuneCountInString(dismiss) + 1
		if col < 1 {
			col = 1
		}
		fmt.Printf("\x1b[%d;%dH%s", height, col, dim(dismiss))
	} else {
		fmt.Print("\n\n" + dim(dismiss))
	}

	if !term.IsTerminal(fd) {
		return
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return
	}
	defer func() { _ = term.Restore(fd, oldState) }()
	_ = picker.NewKeyReader(os.Stdin).Read()
}

// buildPopupContent renders the bay header and (wrapped, possibly
// truncated) description for the popup. Isolated from the display path
// so it can be unit-tested.
func buildPopupContent(bay *manifest.Bay, width, height int) string {
	var header strings.Builder
	label := engine.BayCompactLabel(bay)
	fmt.Fprintln(&header, "\x1b[1m"+label+"\x1b[0m")
	if bay.Path != "" {
		fmt.Fprintln(&header, dim(bay.Path))
	}
	if !manifest.IsHomeBay(bay) && bay.Worktree != nil {
		var meta []string
		if bay.Worktree.Branch != "" && bay.Worktree.Branch != bay.Name {
			meta = append(meta, "branch "+bay.Worktree.Branch)
		}
		if bay.Worktree.PR != "" {
			meta = append(meta, "PR#"+bay.Worktree.PR)
		}
		if len(meta) > 0 {
			fmt.Fprintln(&header, dim(strings.Join(meta, " — ")))
		}
	}
	headerLines := strings.Count(header.String(), "\n")

	if bay.Description == "" {
		return header.String() + "\n" + dim("(no description — set one with `bay describe`)")
	}
	body := wrapText(bay.Description, width)
	body = clipBody(body, height, headerLines)
	return header.String() + "\n" + body
}

// clipBody clips body to fit within the popup's available rows, appending
// a "(+N more lines — run `bay describe` for full)" hint on the last
// line when truncated. The explicit count removes ambiguity (the bare
// ellipsis reads as stylistic, not as "content was cut"). Reservation
// accounts for the header, the blank line separating it from the body,
// and the bottom row where the caller absolute-positions the dismiss
// hint. Returns body unchanged when termHeight is 0 (size unknown —
// don't truncate blindly).
func clipBody(body string, termHeight, headerLines int) string {
	if termHeight <= 0 {
		return body
	}
	reserved := headerLines + 2
	budget := termHeight - reserved
	if budget < 1 {
		budget = 1
	}
	lines := strings.Split(body, "\n")
	if len(lines) <= budget {
		return body
	}
	keep := budget - 1
	if keep < 1 {
		keep = 1
	}
	hidden := len(lines) - keep
	hint := fmt.Sprintf("(+%d more lines — run `bay describe` for full)", hidden)
	lines = append(lines[:keep], dim(hint))
	return strings.Join(lines, "\n")
}

// popupFallbackWidth is the wrap width used when the pty size can't be
// queried (non-tty, tests). Chosen to be comfortable for prose without
// assuming an unusually wide terminal.
const popupFallbackWidth = 60

// popupBorderMargin is the column margin subtracted from the pty width so
// wrapped lines don't butt against the popup's right border.
const popupBorderMargin = 2

// popupDims returns (wrap width, pty height) for the popup. When the tty
// size can't be queried, width falls back to popupFallbackWidth and
// height to 0 — callers treat height=0 as "don't truncate".
func popupDims(fd int) (int, int) {
	w, h, err := term.GetSize(fd)
	if err != nil || w <= 0 {
		return popupFallbackWidth, 0
	}
	if w > popupBorderMargin*2 {
		w -= popupBorderMargin
	}
	return w, h
}

// wrapText word-wraps each line of text to width, preserving blank lines
// and the user's explicit line breaks. Words never split; a single word
// longer than width is emitted on its own line (wider than width is
// better than truncation for URLs and similar). Returns text unchanged
// when width <= 0.
func wrapText(text string, width int) string {
	if width <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = wrapLine(line, width)
	}
	return strings.Join(lines, "\n")
}

// wrapLine wraps a single line, preserving its leading indentation on
// continuation lines so bulleted or indented content stays aligned.
func wrapLine(line string, width int) string {
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	content := line[len(indent):]
	words := strings.Fields(content)
	if len(words) == 0 {
		return line
	}
	indentW := utf8.RuneCountInString(indent)
	effective := width - indentW
	if effective < 1 {
		effective = 1
	}
	var b strings.Builder
	b.WriteString(indent)
	lineLen := 0
	for i, w := range words {
		wlen := utf8.RuneCountInString(w)
		if i > 0 {
			if lineLen+1+wlen > effective {
				b.WriteByte('\n')
				b.WriteString(indent)
				lineLen = 0
			} else {
				b.WriteByte(' ')
				lineLen++
			}
		}
		b.WriteString(w)
		lineLen += wlen
	}
	return b.String()
}

func newBayRenameCmd() *cobra.Command {
	var dockFlag string

	cmd := &cobra.Command{
		Use:     "rename [id] <new-name>",
		Aliases: []string{"mv"},
		Short:   "Rename a bay (defaults to current)",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			var source, newName string
			if len(args) == 1 {
				source = "self"
				newName = args[0]
			} else {
				source = args[0]
				newName = args[1]
			}

			dockName, bayID, err := resolveBayArg(eng, source, dockFlag)
			if err != nil {
				return err
			}

			return eng.BayRename(dockName, bayID, newName)
		},
	}

	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (disambiguates a bare bay ID)")

	return cmd
}

// runDescribe backs `bay describe`.
func runDescribe(args []string, dockFlag string, clear, edit bool) error {
	eng, err := newEngine()
	if err != nil {
		return err
	}

	var target, desc string
	haveDesc := false
	switch len(args) {
	case 0:
		target = "self"
	case 1:
		_, bay, parseErr := parseBayArg(args[0])
		if parseErr == nil && manifest.IsReservedBayID(bay) {
			target = args[0]
		} else {
			target = "self"
			desc = args[0]
			haveDesc = true
		}
	case 2:
		target = args[0]
		desc = args[1]
		haveDesc = true
	}

	dockName, bayID, err := resolveBayArg(eng, target, dockFlag)
	if err != nil {
		return err
	}

	if manifest.IsReservedBayID(bayID) {
		return fmt.Errorf("home is reserved; describe is not supported on the home pseudo-bay")
	}

	// No-op read path: no args, no flags → print current description.
	if !haveDesc && !clear && !edit {
		bay, bayErr := eng.BayShow(dockName, bayID)
		if bayErr != nil {
			return bayErr
		}
		if bay.Description != "" {
			fmt.Println(bay.Description)
		}
		return nil
	}

	if clear {
		return eng.BayDescribe(dockName, bayID, "")
	}

	if edit {
		if haveDesc {
			return fmt.Errorf("--edit and a description argument are mutually exclusive")
		}
		bay, bayErr := eng.BayShow(dockName, bayID)
		if bayErr != nil {
			return bayErr
		}
		newDesc, editErr := editDescription(bay.Description)
		if editErr != nil {
			return editErr
		}
		return eng.BayDescribe(dockName, bayID, newDesc)
	}

	return eng.BayDescribe(dockName, bayID, desc)
}

// editDescription opens $EDITOR (or $VISUAL, or vi as a fallback) on a temp
// file seeded with the current description, waits for the editor to exit, and
// returns the edited contents with trailing whitespace trimmed. Only terminal
// editors work — the call is synchronous.
func editDescription(current string) (string, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		if _, err := exec.LookPath("vi"); err == nil {
			editor = "vi"
		} else {
			return "", fmt.Errorf("no editor found; set $EDITOR or $VISUAL")
		}
	}

	f, err := os.CreateTemp("", "bay-describe-*.txt")
	if err != nil {
		return "", err
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)
	if _, err := f.WriteString(current); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	// Split editor on whitespace so "$EDITOR" can carry flags (e.g. "nvim -u NONE").
	parts := strings.Fields(editor)
	parts = append(parts, tmpPath)
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor %s exited with error: %w", editor, err)
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\n\r\t "), nil
}

// autoBootstrap detects the CWD git checkout, creates a dock in the manifest,
// and saves. Returns the dock name to use for bay new.
func autoBootstrap(eng *engine.Engine, quiet bool) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot determine working directory")
	}

	repoRoot, err := eng.Git.RepoRoot(cwd)
	if err != nil {
		return "", fmt.Errorf("not in a git repository; specify dock name or run from a git repo")
	}

	m, _ := eng.LoadManifest()
	if m != nil {
		root := config.CanonicalPath(repoRoot)
		for i := range m.Docks {
			dock := &m.Docks[i]
			if dock.Path != "" && config.CanonicalPath(dock.Path) == root {
				return dock.Name, nil
			}
		}
	}

	// Probe for an agent on PATH and set as global default if unset.
	agentName := probeAgent()
	if agentName != "" && eng.Config.DefaultAgent == "" {
		eng.Config.DefaultAgent = agentName
		_ = eng.SaveConfig()
	}

	// Create dock if not already present in manifest.
	dockName := filepath.Base(repoRoot)
	var existing *manifest.Dock
	if m != nil {
		existing = m.FindDock(dockName)
	}
	if existing != nil {
		if existing.Path != "" && config.CanonicalPath(existing.Path) != config.CanonicalPath(repoRoot) {
			return "", fmt.Errorf("dock %q already exists for checkout %s; create a dock with a different name", dockName, config.ExpandPath(existing.Path))
		}
	} else {
		if err := eng.DockNew(dockName, repoRoot, "", "", ""); err != nil {
			return "", err
		}
		if _, initErr := eng.DockInit(dockName); initErr != nil && !quiet {
			fmt.Fprintf(os.Stderr, "Warning: dock checkout setup failed: %v\n", initErr)
		}
	}

	if !quiet {
		fmt.Printf("Auto-configured dock %s", dockName)
		if agentName != "" {
			fmt.Printf(", agent %s", agentName)
		}
		fmt.Println()
		fmt.Println("Run `bay setup` to install keybindings and shell completions.")
	}

	return dockName, nil
}

// probeAgent scans PATH for known AI agent commands.
func probeAgent() string {
	return config.ProbeAgent()
}

// resolveCurrentDock resolves the dock for listing commands. Tries:
// 1. CWD → git checkout/worktree → matching dock in the manifest
// 2. Current tmux session (if it's a bay dock)
//
// CWD wins over the tmux session: standing in a known checkout is an
// explicit signal that outranks the ambient session, so `bay new`/`bay
// ls` in dock Y's checkout act on Y even when you're attached to dock
// X's tmux session. The session is only a fallback for when CWD isn't a
// recognized checkout (a scratch dir, an unrelated repo, /tmp, ...).
func resolveCurrentDock(eng *engine.Engine) (string, error) {
	m, _ := eng.LoadManifest()

	// CWD → git checkout/worktree → matching dock.
	tryCWD := func() string {
		if m == nil {
			return ""
		}
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		repoRoot, err := eng.Git.RepoRoot(cwd)
		if err != nil {
			return ""
		}
		root := config.CanonicalPath(repoRoot)
		for i := range m.Docks {
			d := &m.Docks[i]
			if d.Path != "" && config.CanonicalPath(d.Path) == root {
				return d.Name
			}
			for j := range d.Bays {
				bay := &d.Bays[j]
				if bay.Path != "" && config.CanonicalPath(bay.Path) == root {
					return d.Name
				}
			}
		}
		return ""
	}

	// Current tmux session, when it names a bay dock. CurrentSession
	// works even from a run-shell keybinding (it anchors to the
	// invoking session), and errors outside tmux.
	trySession := func() string {
		if sess, err := eng.Tmux.CurrentSession(); err == nil && m != nil && m.FindDock(sess) != nil {
			return sess
		}
		return ""
	}

	// The CWD only reflects the user's intent when it's their real
	// shell: an interactive tmux pane (TMUX_PANE set) or a plain
	// terminal (no TMUX). When bay runs from a tmux keybinding via
	// run-shell, TMUX is set but TMUX_PANE is not, and the CWD is the
	// tmux server's start directory — unrelated to the session the user
	// is looking at. There the session is authoritative, so it must win
	// over the CWD; otherwise Option+c would create bays in whatever
	// dock the server happens to sit in.
	cwdReliable := os.Getenv("TMUX") == "" || os.Getenv("TMUX_PANE") != ""

	first, second := tryCWD, trySession
	if !cwdReliable {
		first, second = trySession, tryCWD
	}
	if dock := first(); dock != "" {
		return dock, nil
	}
	if dock := second(); dock != "" {
		return dock, nil
	}

	return "", fmt.Errorf("cannot determine current dock — not in a tmux session or a known checkout")
}

// resolveTarget resolves "self" or a bay query. Bare IDs prefer the
// current dock for disambiguation (see resolveBareBay).
func resolveTarget(eng *engine.Engine, target string) (string, string, error) {
	if target == "self" {
		return eng.ResolveSelf()
	}
	if dock, bay, err := parseBayArg(target); err == nil && dock == "" {
		return resolveBareBay(eng, bay)
	}
	return eng.ResolveBay(target)
}

func newBayLsCmd() *cobra.Command {
	var jsonOutput bool
	var rowsOutput bool
	var shortOutput bool

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List bays in the current dock",
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			dockName, err := resolveCurrentDock(eng)
			if err != nil {
				return err
			}

			docks, err := eng.List()
			if err != nil {
				return err
			}

			view := BuildListView(docks, ListViewOptions{
				Focus:     ListFocus{Kind: FocusDock, Dock: dockName},
				Recursive: false,
			})
			view.SetCurrentContext(eng)

			if jsonOutput {
				if rowsOutput {
					return printListViewJSON(view, true)
				}
				var bays []engine.BayInfo
				for _, d := range view.Docks {
					bays = append(bays, d.Bays...)
				}
				data, err := json.MarshalIndent(bays, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
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

func newBayGoCmd() *cobra.Command {
	var waiting, nextWaiting, pick bool

	cmd := &cobra.Command{
		Use:   "go [query]",
		Short: "Fuzzy find and switch to a bay in the current dock",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			if pick {
				return bayGoPick(eng)
			}
			return bayGo(eng, args, waiting, nextWaiting)
		},
	}

	cmd.Flags().BoolVar(&waiting, "waiting", false, "filter to waiting bays")
	cmd.Flags().BoolVar(&nextWaiting, "next-waiting", false, "jump to next waiting bay")
	cmd.Flags().BoolVar(&pick, "pick", false, "open picker in a popup (used by keybindings)")
	cmd.Flags().MarkHidden("pick")

	return cmd
}

func newBayNextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "next",
		Short: "Switch to the next bay in the current dock",
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return bayCycle(eng, true)
		},
	}
}

func newBayPrevCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prev",
		Short: "Switch to the previous bay in the current dock",
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return bayCycle(eng, false)
		},
	}
}

// bayGoPick checks if there are enough bays to pick from, then
// opens a tmux display-popup with "bay go". Avoids flashing an
// empty popup when there's nothing to pick.
func bayGoPick(eng *engine.Engine) error {
	currentSession, err := eng.Tmux.CurrentSession()
	if err != nil {
		return nil
	}
	m, err := eng.LoadManifest()
	if err != nil {
		return nil
	}
	entries := nav.CollectEntries(m, eng.Tmux)
	count := 0
	for _, e := range entries {
		if e.DockName == currentSession && e.TmuxWindowID != "" {
			count++
		}
	}
	if count < 2 {
		return nil
	}
	return eng.Tmux.DisplayPopup("bay go")
}

// bayGo implements bay picker scoped to the current dock.
func bayGo(eng *engine.Engine, args []string, waiting, nextWaiting bool) error {
	currentSession, err := eng.Tmux.CurrentSession()
	if err != nil {
		return fmt.Errorf("bay go requires tmux — use bay ls to see bays")
	}

	m, err := eng.LoadManifest()
	if err != nil {
		return err
	}

	entries := nav.CollectEntries(m, eng.Tmux)

	// Filter to current dock, excluding bays with no tmux presence.
	var dockEntries []nav.Entry
	for _, e := range entries {
		if e.DockName == currentSession && e.TmuxWindowID != "" {
			dockEntries = append(dockEntries, e)
		}
	}
	entries = dockEntries

	if nextWaiting {
		currentWinID, _ := eng.Tmux.CurrentWindowID()
		entry, _ := nav.NextWaiting(entries, currentWinID)
		if entry == nil {
			return nil
		}
		if err := eng.Tmux.SelectWindow(entry.TmuxWindowID); err != nil {
			return err
		}
		return nil
	}

	if waiting {
		entries = nav.FilterWaiting(entries)
	}

	query := ""
	if len(args) > 0 {
		query = args[0]
	}
	if query == manifest.HomeBayID {
		if !waiting {
			return eng.Home(currentSession)
		}
		var homeEntry *nav.Entry
		for i := range entries {
			if entries[i].BayID == manifest.HomeBayID || entries[i].BayName == manifest.HomeBayID {
				homeEntry = &entries[i]
				break
			}
		}
		if homeEntry == nil {
			fmt.Fprintln(os.Stderr, "home is not waiting")
			return nil
		}
		entries = []nav.Entry{*homeEntry}
	} else if query != "" {
		entries = nav.FuzzyMatch(entries, query)
	}

	currentWinID, _ := eng.Tmux.CurrentWindowID()

	switch len(entries) {
	case 0:
		return nil
	case 1:
		if entries[0].TmuxWindowID == currentWinID {
			return nil // already here
		}
		return eng.Tmux.SelectWindow(entries[0].TmuxWindowID)
	default:
		return pickBay(eng, entries)
	}
}

// bayCycle moves to next/prev bay in the current dock and flashes the
// new position via the cycling indicator.
func bayCycle(eng *engine.Engine, forward bool) error {
	currentSession, err := eng.Tmux.CurrentSession()
	if err != nil {
		return fmt.Errorf("not in a tmux session")
	}

	m, err := eng.LoadManifest()
	if err != nil {
		return err
	}

	entries := nav.CollectEntries(m, eng.Tmux)

	var dockEntries []nav.Entry
	for _, e := range entries {
		if e.DockName == currentSession && e.TmuxWindowID != "" {
			dockEntries = append(dockEntries, e)
		}
	}

	if len(dockEntries) < 2 {
		return nil
	}

	currentWinID, _ := eng.Tmux.CurrentWindowID()
	cur := -1
	for i, e := range dockEntries {
		if e.TmuxWindowID == currentWinID {
			cur = i
			break
		}
	}

	var next int
	if forward {
		next = (cur + 1) % len(dockEntries)
	} else {
		if cur == -1 {
			next = len(dockEntries) - 1
		} else {
			next = (cur - 1 + len(dockEntries)) % len(dockEntries)
		}
	}

	return eng.Tmux.SelectWindow(dockEntries[next].TmuxWindowID)
}

// pickBay shows the built-in picker for bay selection.
func pickBay(eng *engine.Engine, entries []nav.Entry) error {
	currentWinID, _ := eng.Tmux.CurrentWindowID()
	items, currentIdx := formatBayItems(entries, currentWinID)

	selected, err := defaultPicker.Pick(items, picker.Options{Prompt: "bay> ", Selected: currentIdx})
	if err != nil || selected < 0 {
		return nil
	}
	return eng.Tmux.SelectWindow(entries[selected].TmuxWindowID)
}

func formatBayItems(entries []nav.Entry, currentWinID string) ([]picker.Item, int) {
	currentIdx := 0
	maxBay, maxDesc, maxBranch, maxPR := 0, 0, 0, 0
	descs := make([]string, len(entries))
	labels := make([]string, len(entries))
	for i, e := range entries {
		labels[i] = e.DisplayLabel()
		descs[i] = engine.TruncateName(engine.DescriptionFirstLine(e.Description), pickerDescMaxLen)
		if len(labels[i]) > maxBay {
			maxBay = len(labels[i])
		}
		if len(descs[i]) > maxDesc {
			maxDesc = len(descs[i])
		}
		if len(e.Branch) > maxBranch {
			maxBranch = len(e.Branch)
		}
		pr := ""
		if e.PR != "" {
			pr = "#" + e.PR
		}
		if len(pr) > maxPR {
			maxPR = len(pr)
		}
		if e.TmuxWindowID == currentWinID {
			currentIdx = i
		}
	}
	items := make([]picker.Item, len(entries))
	for i, e := range entries {
		pr := ""
		if e.PR != "" {
			pr = "#" + e.PR
		}
		tags := ""
		if e.Pending {
			tags += "  PENDING"
		}
		if e.Setup != "" {
			tags += "  SETUP " + e.Setup
		}
		if e.Waiting {
			tags += "  WAITING"
		}
		if maxDesc > 0 {
			s := fmt.Sprintf("%-*s  %-*s  %-*s  %-*s%s",
				maxBay, labels[i], maxDesc, descs[i], maxBranch, e.Branch, maxPR, pr, tags)
			items[i] = picker.Item{Display: s, Value: i}
			continue
		}
		s := fmt.Sprintf("%-*s  %-*s  %-*s%s",
			maxBay, labels[i], maxBranch, e.Branch, maxPR, pr, tags)
		items[i] = picker.Item{Display: s, Value: i}
	}
	return items, currentIdx
}

// pickerDescMaxLen caps description width in the bay picker. Larger than
// tab names (where space is scarce) but still bounded to keep the picker
// scannable.
const pickerDescMaxLen = 40
