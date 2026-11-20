package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/occupant"
	"github.com/spf13/cobra"
)

// defaultWhoSurface is the surface `bay who <bay>` looks at first.
const defaultWhoSurface = "agent"

// whoResult is the JSON shape of `bay who --json`.
type whoResult struct {
	Dock        string `json:"dock"`
	Bay         string `json:"bay"`
	Surface     string `json:"surface,omitempty"`
	BayAddress  string `json:"bay_address"`
	Pane        string `json:"pane,omitempty"`
	Occupied    bool   `json:"occupied"`
	PID         int    `json:"pid,omitempty"`
	Login       string `json:"login,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	Status      string `json:"status,omitempty"`
	Version     string `json:"version,omitempty"`
	Address     string `json:"address,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func newWhoCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "who [[dock:]bay[:surface]]",
		Short: "Show the Claude session in a bay surface and its messaging address",
		Long: `Show the Claude Code session occupying a bay surface, whichever Claude
login it runs under, and the address to reach it with SendMessage.

The argument is a bay address: b100, loom:b100, b100:agent2, or
loom:b100:agent2. With two parts, the first is a dock if one has that
name, otherwise a bay.
Without a surface, the surface named "agent" is used; if it has no Claude
session, the one surface in the bay that does is used instead. Several
candidates and no "agent" surface is an error that lists them. With no
argument, reports the current pane's surface (use it to sign messages).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			res, err := runWho(eng, occupant.RealSystem{}, target)
			if err != nil {
				return err
			}
			if jsonOutput {
				data, err := json.MarshalIndent(res, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}
			printWho(res)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

// parseWhoTarget splits a bay address: bay, dock:bay or bay:surface,
// or dock:bay:surface. An empty target is the current pane's surface.
func parseWhoTarget(eng *engine.Engine, target string) (dockName, bayName, surface string, err error) {
	switch target {
	case "":
		return surfaceOfCurrentPane(eng)
	case "self":
		return resolveSurfaceArgOrSelf(eng, "self", "", "")
	}
	parts := strings.Split(target, ":")
	for _, p := range parts {
		if p == "" {
			return "", "", "", fmt.Errorf("invalid bay address %q (expected bay, dock:bay, or dock:bay:surface)", target)
		}
	}
	switch len(parts) {
	case 1:
		dockName, bayName, err = resolveBareBay(eng, parts[0])
	case 2:
		// dock:bay when the first part names a dock (the bay-address
		// form), otherwise bay:surface as in `bay sf show b1:agent`.
		m, loadErr := eng.LoadManifest()
		if loadErr != nil {
			return "", "", "", loadErr
		}
		if m.FindDock(parts[0]) != nil {
			dockName, bayName, err = eng.ResolveBay(parts[0] + ":" + parts[1])
		} else {
			dockName, bayName, err = resolveBareBay(eng, parts[0])
			surface = parts[1]
		}
	case 3:
		dockName, bayName, err = eng.ResolveBay(parts[0] + ":" + parts[1])
		surface = parts[2]
	default:
		err = fmt.Errorf("invalid bay address %q (expected bay, dock:bay, or dock:bay:surface)", target)
	}
	return dockName, bayName, surface, err
}

// surfaceOfCurrentPane finds the bay surface that owns the current tmux
// pane, searching every bay. The pane, not the working directory, is
// the caller's identity: an agent may have cd'd into another bay's
// worktree, and a bay may have a surface literally named "self".
func surfaceOfCurrentPane(eng *engine.Engine) (string, string, string, error) {
	paneID, err := eng.Tmux.CurrentPaneID()
	if err != nil || paneID == "" {
		return "", "", "", fmt.Errorf("cannot determine current pane")
	}
	m, err := eng.LoadManifest()
	if err != nil {
		return "", "", "", err
	}
	for _, d := range m.Docks {
		for _, b := range d.Bays {
			for _, s := range b.Surfaces {
				if s.Tmux != nil && s.Tmux.PaneID == paneID {
					return d.Name, b.ID, s.Name, nil
				}
			}
		}
	}
	return "", "", "", fmt.Errorf("current pane %s is not a bay surface", paneID)
}

func runWho(eng *engine.Engine, sys occupant.System, target string) (*whoResult, error) {
	dockName, bayName, sName, err := parseWhoTarget(eng, target)
	if err != nil {
		return nil, err
	}
	if bayName == "" {
		return nil, fmt.Errorf("dock-level surfaces have no bay address")
	}
	// No SyncAll: it can close or prune other bays, and this command is
	// read-only. LiveSurfacePanePIDs checks the recorded panes instead.
	m, err := eng.LoadManifest()
	if err != nil {
		return nil, err
	}
	dock := m.FindDock(dockName)
	if dock == nil {
		return nil, fmt.Errorf("unknown dock %q", dockName)
	}
	bay := dock.FindBayByID(bayName)
	if bay == nil {
		return nil, fmt.Errorf("bay %q not found in dock %q", bayName, dockName)
	}
	panePIDs, err := eng.LiveSurfacePanePIDs(dock, bay)
	if err != nil {
		return nil, err
	}
	procs, err := sys.Processes()
	if err != nil {
		return nil, err
	}
	paneProc := func(s *manifest.Surface) *occupant.Process {
		pid, ok := panePIDs[s.Name]
		if !ok {
			return nil
		}
		return occupant.FindClaude(procs, pid)
	}

	res := &whoResult{Dock: dockName, Bay: bayName}
	var surf *manifest.Surface
	var proc *occupant.Process
	if sName != "" {
		surf = bay.FindSurface(sName)
		if surf == nil {
			return nil, fmt.Errorf("surface %q not found in bay %s:%s", sName, dockName, bayName)
		}
		proc = paneProc(surf)
	} else {
		surf, proc, err = pickWhoSurface(bay, paneProc)
	}
	if err != nil {
		return nil, fmt.Errorf("bay %s:%s: %w", dockName, bayName, err)
	}

	res.BayAddress = dockName + ":" + bayName
	if surf != nil {
		res.Surface = surf.Name
		res.BayAddress += ":" + surf.Name
		if surf.Tmux != nil {
			res.Pane = surf.Tmux.PaneID
		}
	}
	switch {
	case surf == nil:
		res.Reason = "no surface in this bay is running Claude"
		return res, nil
	case surf.Tmux == nil:
		res.Reason = "surface is not a tmux pane"
		return res, nil
	case panePIDs[surf.Name] == 0:
		res.Reason = "surface's recorded pane is not live in the dock's tmux session"
		return res, nil
	case proc == nil:
		res.Reason = "no Claude process in the surface's pane"
		return res, nil
	}

	occ, err := occupant.Describe(sys, *proc)
	if occ != nil {
		res.PID = occ.PID
		res.Login = occ.LoginDir
		res.SessionID = occ.SessionID
		res.SessionName = occ.Name
		res.Status = occ.Status
		res.Version = occ.Version
	}
	if errors.Is(err, occupant.ErrNoSocket) {
		res.Reason = err.Error()
		return res, nil
	}
	if err != nil {
		return nil, err
	}
	res.Occupied = true
	res.Address = occ.Address()
	return res, nil
}

// pickWhoSurface chooses the surface for an address without one. The
// surface named "agent" wins when Claude runs in it. Otherwise the single
// surface running Claude is used, found by process rather than surface
// type (Claude often runs in a surface bay labels "shell"). Several
// candidates with none named "agent" is an error listing them; sending
// to all of them would have two agents act on one request. No candidates
// reports the "agent" surface, if any, as unoccupied.
func pickWhoSurface(bay *manifest.Bay, paneProc func(*manifest.Surface) *occupant.Process) (*manifest.Surface, *occupant.Process, error) {
	def := bay.FindSurface(defaultWhoSurface)
	if def != nil {
		if p := paneProc(def); p != nil {
			return def, p, nil
		}
	}
	type cand struct {
		s *manifest.Surface
		p *occupant.Process
	}
	var cands []cand
	for i := range bay.Surfaces {
		s := &bay.Surfaces[i]
		if s == def {
			continue
		}
		if p := paneProc(s); p != nil {
			cands = append(cands, cand{s, p})
		}
	}
	switch len(cands) {
	case 0:
		return def, nil, nil
	case 1:
		return cands[0].s, cands[0].p, nil
	}
	names := make([]string, len(cands))
	for i, c := range cands {
		names[i] = c.s.Name + " (pid " + strconv.Itoa(c.p.PID) + ")"
	}
	return nil, nil, fmt.Errorf("several surfaces run Claude and none is named %q; name one: %s",
		defaultWhoSurface, strings.Join(names, ", "))
}

func printWho(r *whoResult) {
	rows := []showRow{{"bay", r.BayAddress}}
	if r.Pane != "" {
		rows = append(rows, showRow{"pane", r.Pane})
	}
	if !r.Occupied {
		rows = append(rows, showRow{"occupant", "none — " + r.Reason})
	}
	if r.PID != 0 {
		rows = append(rows, showRow{"pid", strconv.Itoa(r.PID)})
	}
	for _, kv := range []showRow{
		{"login", r.Login},
		{"session", r.SessionName},
		{"status", r.Status},
		{"version", r.Version},
		{"address", r.Address},
	} {
		if kv.value != "" {
			rows = append(rows, kv)
		}
	}
	printAlignedRows(rows)
}
