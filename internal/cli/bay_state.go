package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/spf13/cobra"
)

// runState backs `bay state`.
func runState(eng *engine.Engine, out io.Writer, args []string, dockFlag string, clr bool) error {
	target, state, haveState := splitItemArgs(args)
	dock, bayID, err := resolveBayArg(eng, target, dockFlag)
	if err != nil {
		return err
	}
	switch {
	case clr:
		if haveState {
			return fmt.Errorf("--clear takes no state")
		}
		return eng.BaySetState(dock, bayID, "")
	case haveState:
		return eng.BaySetState(dock, bayID, state)
	}
	bay, err := eng.BayShow(dock, bayID)
	if err != nil {
		return err
	}
	if bay.State != "" {
		fmt.Fprintln(out, manifest.StateMarkOf(bay.State)+" "+bay.State)
	}
	return nil
}

// stateHelp lists the states, one line each, for the command's help.
func stateHelp() string {
	var b strings.Builder
	for _, m := range manifest.StateMarks {
		fmt.Fprintf(&b, "  %s %-10s %s\n", m.Mark, m.Word, m.Meaning)
	}
	return b.String()
}

func newBayStateCmd() *cobra.Command {
	var dockFlag string
	var clr bool

	cmd := &cobra.Command{
		Use:   "state [id] [<state>]",
		Short: "Read or set a bay's lifecycle state",
		Long: `Read or set the lifecycle state of a bay: one mark that says where the
bay's work is. The bay's session sets it at each change of state. The
docket list shows the mark on the bay's row. The state stays when the
session ends, and goes when the bay is closed.

Give a state by its word or its mark:

` + stateHelp() + `
  bay state                 print the current bay's state
  bay state b3              print bay b3's state
  bay state pr              set the current bay's state
  bay state b3 judging      set by bay ID
  bay state --clear         remove the state`,
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return runState(eng, os.Stdout, args, dockFlag, clr)
		},
	}

	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (disambiguates a bare bay ID)")
	cmd.Flags().BoolVar(&clr, "clear", false, "remove the state")
	return cmd
}
