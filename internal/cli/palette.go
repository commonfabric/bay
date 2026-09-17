package cli

import (
	"fmt"
	"os"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/palette"
	"github.com/spf13/cobra"
)

// newPaletteCmd is the hidden `bay palette` command that powers the
// tmux popup. It's invoked by the M-p keybinding registered by
// `bay setup`.
func newPaletteCmd() *cobra.Command {
	var splitMode string

	cmd := &cobra.Command{
		Use:    "palette",
		Short:  "Open the command palette (used by keybindings)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := palette.ModePane
			switch splitMode {
			case "", "pane":
				mode = palette.ModePane
			case "window":
				mode = palette.ModeWindow
			default:
				return fmt.Errorf("invalid --split %q (window or pane)", splitMode)
			}
			return runPalette(mode)
		},
	}

	cmd.Flags().StringVar(&splitMode, "split", "pane", "initial mode: window or pane")
	return cmd
}

// runPalette boots the palette: loads engine, resolves the current scope,
// loads recents and live hotkeys, builds the 24-entry table, and hands off
// to palette.Run.
//
// The popup is invoked via tmux display-popup, which gives us our own PTY
// on os.Stdin/Stdout. We don't explicitly pick those — palette.Run defaults
// to them.
func runPalette(mode palette.Mode) error {
	eng, err := newEngine()
	if err != nil {
		// Fail silently outside a bay context — matches the || true
		// suffix the keybindings install with.
		return nil
	}
	ctx, _ := eng.CurrentContext()
	scope := detectScope(ctx)
	// The palette is a bay-session tool. Outside bay, exit silently so
	// the M-p binding isn't visible to users who haven't set things up.
	if ctx == nil || (scope == palette.ScopeAnywhere && ctx.Dock == "") {
		return nil
	}

	recents := palette.LoadRecents(bayPaths().PaletteRecents)
	hotkeys := palette.LoadHotkeys()

	env := &paletteEnv{
		Engine:  eng,
		Scope:   scope,
		Ctx:     ctx,
		Recents: recents,
		Hotkeys: hotkeys,
		In:      os.Stdin,
		Out:     os.Stdout,
	}

	build := func(m palette.Mode) []palette.Entry {
		return buildPaletteEntries(env, m)
	}

	return palette.Run(palette.RunOptions{
		Scope:   scope,
		Mode:    mode,
		Entries: build,
		Recents: recents,
		In:      os.Stdin,
		Out:     os.Stdout,
	})
}

// detectScope maps an engine.Context to the palette's tri-state scope.
func detectScope(ctx *engine.Context) palette.Scope {
	if ctx == nil {
		return palette.ScopeAnywhere
	}
	if ctx.BayID != "" || ctx.Bay != "" {
		return palette.ScopeInBay
	}
	if ctx.Dock != "" {
		return palette.ScopeInDock
	}
	return palette.ScopeAnywhere
}
