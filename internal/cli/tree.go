package cli

import (
	"fmt"

	"github.com/commonfabric/bay/internal/manifest"
	"github.com/spf13/cobra"
)

func newTreeCmd() *cobra.Command {
	var jsonOutput bool
	var rowsOutput bool
	var dirtyOnly bool
	var longOutput bool
	var shortOutput bool

	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Show full hierarchy (docks, bays, surfaces)",
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			docks, err := eng.List()
			if err != nil {
				return err
			}

			// User-interest signal: any bay the user is viewing that still
			// has no PR despite having a branch gets marked stale so the
			// monitor's next tick re-queries gh, rather than waiting for the
			// full TTL.
			_ = eng.MarkAllPRChecksStale()

			if dirtyOnly {
				docks = filterDirtyBays(docks)
			}

			view := BuildListView(docks, ListViewOptions{
				Focus:     ListFocus{Kind: FocusAll},
				Recursive: true,
			})

			view.SetCurrentContext(eng)

			if jsonOutput {
				return printListViewJSON(view, rowsOutput)
			}

			fmt.Print(FormatListView(view, longOutput, shortOutput))

			// Print advice for stale or missing bays visible in the output.
			hasStale, hasMissing := false, false
			for _, d := range view.Docks {
				for _, bay := range d.Bays {
					if bay.SyncStatus == manifest.SyncStatusStale {
						hasStale = true
					}
					if bay.SyncStatus == manifest.SyncStatusMissing {
						hasMissing = true
					}
				}
			}
			if hasStale {
				fmt.Println("\n[stale] = tmux window was closed. Run 'bay recover' to recreate, or 'bay close <id>' to remove.")
			}
			if hasMissing {
				fmt.Println("\n[missing] = worktree directory was deleted. Run 'bay close <id> --force' to clean up.")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	cmd.Flags().BoolVar(&rowsOutput, "rows", false, "output JSON as denormalized rows")
	cmd.Flags().BoolVar(&dirtyOnly, "dirty", false, "only show dirty bays")
	cmd.Flags().BoolVarP(&longOutput, "long", "l", false, "show extended details such as tmux IDs")
	cmd.Flags().BoolVarP(&shortOutput, "short", "s", false, "compact output without labels or key names")

	return cmd
}
