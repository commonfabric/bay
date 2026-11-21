package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/occupant"
	"github.com/spf13/cobra"
)

// peerResult is one row of `bay peers --json`: a whoResult for an
// occupied-or-not Claude surface, plus whether it is the caller's own.
type peerResult struct {
	whoResult
	Self bool `json:"self,omitempty"`
}

func newPeersCmd() *cobra.Command {
	var jsonOutput bool
	var dockFlag string

	cmd := &cobra.Command{
		Use:   "peers",
		Short: "List the Claude sessions in bay surfaces, across Claude logins",
		Long: `List every bay surface that has a Claude Code session in it, whichever
Claude login the session runs under, with its bay address and the address
to reach it with SendMessage.

Unlike Claude Code's own ListAgents, which shows only sessions under the
caller's login, this covers all logins. Rows are keyed by bay address
(dock:bay:surface), which does not change when a session restarts or moves
to another login. Only bay surfaces are listed; Claude sessions outside
bays are not. Read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			rows, err := runPeers(eng, occupant.RealSystem{}, dockFlag, os.Stderr)
			if err != nil {
				return err
			}
			if jsonOutput {
				if rows == nil {
					rows = []peerResult{}
				}
				data, err := json.MarshalIndent(rows, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}
			printPeers(os.Stdout, rows)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	cmd.Flags().StringVar(&dockFlag, "dock", "", "only this dock")
	return cmd
}

// runPeers lists the Claude occupants of every bay surface, in manifest
// order. A dock whose tmux state can't be read is skipped, and a session
// whose sockets can't be inspected is listed without an address, each
// with a warning on warn, so one failure doesn't hide the rest.
func runPeers(eng *engine.Engine, sys occupant.System, dockFilter string, warn io.Writer) ([]peerResult, error) {
	m, err := eng.LoadManifest()
	if err != nil {
		return nil, err
	}
	if dockFilter != "" && m.FindDock(dockFilter) == nil {
		return nil, fmt.Errorf("unknown dock %q", dockFilter)
	}
	procs, err := sys.Processes()
	if err != nil {
		return nil, err
	}
	selfPane, _ := eng.Tmux.CurrentPaneID()

	// Pass 1: find each surface's Claude process. Pass 2 describes them
	// all against one batched socket lookup.
	var rows []peerResult
	var found []occupant.Process
	for i := range m.Docks {
		dock := &m.Docks[i]
		if dockFilter != "" && dock.Name != dockFilter {
			continue
		}
		panePIDs, err := eng.LiveDockPanePIDs(dock)
		if err != nil {
			fmt.Fprintf(warn, "warning: skipping dock %s: %v\n", dock.Name, err)
			continue
		}
		for j := range dock.Bays {
			bay := &dock.Bays[j]
			for _, s := range bay.Surfaces {
				pid, ok := panePIDs[bay.ID][s.Name]
				if !ok {
					continue
				}
				proc := occupant.FindClaude(procs, pid)
				if proc == nil {
					continue
				}
				rows = append(rows, peerResult{
					whoResult: whoResult{
						Dock:       dock.Name,
						Bay:        bay.ID,
						Surface:    s.Name,
						BayAddress: dock.Name + ":" + bay.ID + ":" + s.Name,
						Pane:       s.Tmux.PaneID,
					},
					Self: selfPane != "" && s.Tmux.PaneID == selfPane,
				})
				found = append(found, *proc)
			}
		}
	}

	pids := make([]int, len(found))
	for i, p := range found {
		pids[i] = p.PID
	}
	sys = occupant.Prefetch(sys, pids)
	for i := range rows {
		if err := describeOccupant(&rows[i].whoResult, sys, found[i]); err != nil {
			fmt.Fprintf(warn, "warning: %s: %v\n", rows[i].BayAddress, err)
			rows[i].Reason = err.Error()
		}
	}
	return rows, nil
}

// printPeers prints one row per peer. The caller's own surface is marked
// with "*".
func printPeers(w io.Writer, rows []peerResult) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No Claude sessions in bay surfaces.")
		return
	}
	home, _ := os.UserHomeDir()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BAY ADDRESS\tSTATUS\tLOGIN\tSESSION\tVERSION\tADDRESS")
	for _, r := range rows {
		addr := r.BayAddress
		if r.Self {
			addr += " *"
		}
		login := r.Login
		if home != "" && strings.HasPrefix(login, home+"/") {
			login = "~" + login[len(home):]
		}
		target := r.Address
		if target == "" {
			target = "none (" + r.Reason + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", addr, dash(r.Status), dash(login), dash(r.SessionName), dash(r.Version), target)
	}
	tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
