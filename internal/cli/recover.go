package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newRecoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recover",
		Short: "Reconstruct tmux state after reboot",
		Long: `Reconstruct tmux state from the manifest.

Inside a dock: recovers just that dock.
Outside tmux: recovers all docks.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			// Save current window so we can restore focus after recovery
			currentWinID, _ := eng.Tmux.CurrentWindowID()
			currentSession, _ := eng.Tmux.CurrentSession()

			// Inside a dock: recover just that dock
			if session, sessionErr := eng.Tmux.CurrentSession(); sessionErr == nil {
				result, err := eng.DockRecover(session)
				if err != nil && len(result.Recovered) == 0 {
					// Not a bay dock — fall through to full recovery
				} else {
					if len(result.Recovered) == 0 {
						fmt.Printf("Dock %q: nothing to recover.\n", session)
					} else {
						fmt.Printf("Dock %q: recovered %s\n", session, strings.Join(result.Recovered, ", "))
					}
					printRecoveryWarnings(result.Warnings)
					if currentWinID != "" {
						_ = eng.Tmux.SelectWindow(session, currentWinID)
					}
					startMonitor()
					return err
				}
			}

			// Outside tmux or not a bay dock: recover all
			results, err := eng.Recover()
			if len(results) == 0 {
				if err != nil {
					return err
				}
				fmt.Println("Nothing to recover.")
				return nil
			}
			for _, r := range results {
				if len(r.Recovered) == 0 {
					fmt.Printf("Dock %q: nothing to recover.\n", r.Dock)
				} else {
					fmt.Printf("Dock %q: recovered %s\n", r.Dock, strings.Join(r.Recovered, ", "))
				}
				printRecoveryWarnings(r.Warnings)
			}
			fmt.Println("\nAttach with:")
			for _, r := range results {
				fmt.Printf("  %s\n", r.AttachCmd)
			}

			// Restore focus if we were in tmux
			if currentWinID != "" && currentSession != "" {
				_ = eng.Tmux.SelectWindow(currentSession, currentWinID)
			}

			startMonitor()
			return err
		},
	}
}

func printRecoveryWarnings(warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}
}

// startMonitor is the verbose recover-time start: prints "Monitor
// started." on success and a warning on failure. ensureMonitor is the
// quiet variant used by the auto-start hook.
func startMonitor() {
	mon, monErr := newMonitorWithConfig()
	if monErr == nil {
		running, _, _ := mon.Status()
		if !running {
			if startErr := mon.Start(); startErr != nil {
				fmt.Printf("Warning: could not start monitor: %v\n", startErr)
			} else {
				fmt.Println("Monitor started.")
			}
		}
	}
}
