package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/commonfabric/bay/internal/engine"
	gitpkg "github.com/commonfabric/bay/internal/git"
	"github.com/commonfabric/bay/internal/manifest"
	tmuxpkg "github.com/commonfabric/bay/internal/tmux"
	"github.com/spf13/cobra"
)

func newStatusLineCmd() *cobra.Command {
	// --width is a string (not int) so unexpanded tmux format vars
	// like "#{status-right-length}" degrade gracefully to the full
	// format instead of surfacing a parse error in the status line.
	var widthStr string

	// --window takes an explicit tmux window ID (e.g. "@7") instead
	// of asking tmux which window is "current". See the long comment
	// in RunE for why this matters for correctness.
	var windowID string

	cmd := &cobra.Command{
		Use:   "status-line <field>",
		Short: "Output bay info for tmux status line",
		Long: `Output a single field for the current bay, resolved by tmux window ID.
Designed for use in tmux status-format strings. Outputs empty string if not in a bay.

Fields: id, name, dir, branch, pr, status, dock, merged, full

The full field outputs label branch #PR status. Use --width to enable
adaptive truncation (pass #{status-right-length} from tmux). Non-numeric
width values are treated as 0 (full format, no truncation).

Pass --window #{window_id} so each window gets its own #() cache entry
in tmux; otherwise the status line can show stale data from a different
window until the next status-interval tick.`,
		Args: cobra.ExactArgs(1),
		// Tmux status-right runs this command on every refresh — do
		// NOT fork the monitor from here.
		Annotations: map[string]string{noMonitorAutostartAnnotation: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			field := args[0]

			// Fast path: skip newEngine() entirely. status-line
			// only needs a tmux connection (for the current window
			// ID) and the manifest (for bay data). No config
			// parse, no git interface, no engine construction.
			t := tmuxpkg.NewReal()
			g := gitpkg.NewReal()

			// Resolve the window this status line represents.
			//
			// Prefer --window over asking tmux for the "current"
			// window. Reason: tmux caches #(shell-cmd) output per
			// client, keyed by the literal command string, and only
			// refreshes on status-interval ticks (default 15s).
			// If the config is
			//     set -g status-right '#(bay status-line full)'
			// then every window/session sees the same cached output
			// — whichever window happened to be "current" when the
			// command last ran. Switching windows or docks shows
			// stale info until the next tick.
			//
			// When callers interpolate #{window_id} into the command
			// string:
			//     set -g status-right '#(bay status-line full --window #{window_id})'
			// tmux expands it to a different string per window
			// ("@1", "@2", ...), giving each its own cache entry.
			// The cache miss on a new window triggers a fresh run
			// with the correct --window, and bay reports that
			// window's bay rather than whichever one tmux
			// reports as "current".
			//
			// Fall back to CurrentWindowID() for backward compat
			// with configs that don't pass --window yet.
			winID := windowID
			if winID == "" {
				var err error
				winID, err = t.CurrentWindowID()
				if err != nil {
					return nil // not in tmux
				}
			}

			p := bayPaths()
			m, err := manifest.Load(p.ManifestFile)
			if err != nil {
				return nil // can't load manifest
			}

			dockName, bay := resolveWindowID(m, winID)
			if bay == nil {
				return nil // not a bay window
			}

			out, err := statusLineOutput(field, dockName, bay, m, g, widthStr)
			if err != nil {
				return err
			}

			if out != "" {
				fmt.Print(out)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&widthStr, "width", "", "available width in cells; enables adaptive truncation")
	cmd.Flags().StringVar(&windowID, "window", "", "tmux window ID (pass #{window_id} so each window gets its own #() cache entry)")

	return cmd
}

func statusLineOutput(field, dockName string, bay *manifest.Bay, m *manifest.Manifest, g gitpkg.Interface, widthStr string) (string, error) {
	home := manifest.IsHomeBay(bay)

	switch field {
	case "id":
		return bay.ID, nil
	case "name":
		// Canonical bay label (e.g. "home", "b1", "b1.auth-fix") — matches
		// the picker, tmux tabs, and `bay ls`. Never blank for a bay that
		// has either an ID or a Name.
		return engine.BayCompactLabel(bay), nil
	case "dir":
		return engine.BayDirTag(bay), nil
	case "branch":
		if !home && bay.Worktree != nil {
			return bay.Worktree.Branch, nil
		}
	case "pr":
		if !home && bay.Worktree != nil && bay.Worktree.PR != "" {
			return "#" + bay.Worktree.PR, nil
		}
	case "status":
		if !home && bay.Path != "" {
			if dirty, err := g.IsDirty(bay.Path); err == nil && dirty {
				return "dirty", nil
			}
		}
		if !home && bay.IsMerged() {
			return "merged", nil
		}
	case "dock":
		return dockName, nil
	case "merged":
		dock := m.FindDock(dockName)
		if dock != nil {
			count := 0
			for i := range dock.Bays {
				w := &dock.Bays[i]
				if !manifest.IsHomeBay(w) && w.IsMerged() {
					count++
				}
			}
			if count > 0 {
				return fmt.Sprintf("%d merged", count), nil
			}
		}
	case "full":
		branch := ""
		pr := ""
		if !home && bay.Worktree != nil {
			branch = bay.Worktree.Branch
			pr = bay.Worktree.PR
		}

		status := ""
		if !home && bay.Path != "" {
			if dirty, err := g.IsDirty(bay.Path); err == nil && dirty {
				status = "dirty"
			} else if bay.IsMerged() {
				status = "merged"
			}
		} else if !home && bay.Worktree != nil && bay.Worktree.Merged {
			status = "merged"
		}

		width, _ := strconv.Atoi(widthStr)
		return formatStatusLine(bay, branch, pr, status, width), nil
	default:
		return "", fmt.Errorf("unknown field %q; valid fields: id, name, dir, branch, pr, status, dock, merged, full", field)
	}
	return "", nil
}

// formatStatusLine builds the status-line output with optional width-adaptive
// truncation. When width is 0, the full format is returned.
//
// Tiers (tried in order until output fits within width):
//
//	full:             label branch #PR status
//	label-cropped:    b4.auth branch #PR status
//	branch-cropped:   b4.auth bran.. #PR status
//	compact-status:   b4.auth bran.. #PR *
//	minimal:          b4.auth bran.. *
//	label-only:       b4.auth
func formatStatusLine(bay *manifest.Bay, branch, pr, status string, width int) string {
	label := engine.BayCompactLabel(bay)
	full := buildStatusLine(label, branch, pr, status)
	if width <= 0 || len(full) <= width {
		return full
	}

	// First crop only the label, preserving branch/PR/status.
	if out, ok := fitByCroppingLabel(bay, branch, pr, status, width); ok {
		return out
	}

	// Then truncate branch metadata. Keep full status first.
	if branch != "" {
		if out, ok := fitWithTruncatedBranch(bay, branch, pr, status, width); ok {
			return out
		}
	}

	shortStatus := abbreviateStatus(status)
	if branch != "" {
		if out, ok := fitWithTruncatedBranch(bay, branch, pr, shortStatus, width); ok {
			return out
		}
		if out, ok := fitWithTruncatedBranch(bay, branch, "", shortStatus, width); ok {
			return out
		}
	}

	if label != "" {
		if out, ok := fitByCroppingLabel(bay, "", "", shortStatus, width); ok {
			return out
		}
		return engine.TruncateBayCompactLabel(bay, width)
	}
	if shortStatus != "" && len(shortStatus) <= width {
		return shortStatus
	}
	return ""
}

func fitByCroppingLabel(bay *manifest.Bay, branch, pr, status string, width int) (string, bool) {
	rest := buildStatusLine("", branch, pr, status)
	budget := width
	if rest != "" {
		budget -= len(rest) + 1
	}
	if budget <= 0 {
		return "", false
	}
	if engine.BayCompactLabel(bay) != "" {
		minBudget := 1
		if dirTag := engine.BayDirTag(bay); dirTag != "" {
			minBudget = len(dirTag)
		}
		if budget < minBudget {
			return "", false
		}
	}
	label := engine.TruncateBayCompactLabel(bay, budget)
	out := buildStatusLine(label, branch, pr, status)
	return out, out != "" && len(out) <= width
}

func fitWithTruncatedBranch(bay *manifest.Bay, branch, pr, status string, width int) (string, bool) {
	label := engine.BayCompactLabel(bay)
	maxLabelBudget := len(label)
	if maxLabelBudget > width {
		maxLabelBudget = width
	}
	minLabelBudget := 0
	if label != "" {
		minLabelBudget = 1
		if dirTag := engine.BayDirTag(bay); dirTag != "" && len(dirTag) <= width {
			minLabelBudget = len(dirTag)
		}
	}
	for labelBudget := maxLabelBudget; labelBudget >= minLabelBudget; labelBudget-- {
		truncatedLabel := ""
		if labelBudget > 0 {
			truncatedLabel = engine.TruncateBayCompactLabel(bay, labelBudget)
		}
		branchBudget := statusLineBranchBudget(width, truncatedLabel, pr, status)
		if branchBudget < 4 {
			continue
		}
		out := buildStatusLine(truncatedLabel, engine.TruncateName(branch, branchBudget), pr, status)
		if len(out) <= width {
			return out, true
		}
	}
	return "", false
}

func statusLineBranchBudget(width int, label, pr, status string) int {
	used := 0
	parts := 1 // branch
	if label != "" {
		used += len(label)
		parts++
	}
	if pr != "" {
		used += len("#") + len(pr)
		parts++
	}
	if status != "" {
		used += len(status)
		parts++
	}
	return width - used - (parts - 1)
}

func buildStatusLine(label, branch, pr, status string) string {
	var parts []string
	if label != "" {
		parts = append(parts, label)
	}
	if branch != "" {
		parts = append(parts, branch)
	}
	if pr != "" {
		parts = append(parts, "#"+pr)
	}
	if status != "" {
		parts = append(parts, status)
	}
	return strings.Join(parts, " ")
}

// abbreviateStatus returns a short status indicator: * for dirty, M for merged.
func abbreviateStatus(status string) string {
	switch status {
	case "dirty":
		return "*"
	case "merged":
		return "M"
	default:
		return ""
	}
}

// resolveWindowID finds the dock and bay that own a tmux window ID.
// Returns ("", nil) if no match is found.
func resolveWindowID(m *manifest.Manifest, windowID string) (string, *manifest.Bay) {
	for i := range m.Docks {
		dock := &m.Docks[i]
		for j := range dock.Bays {
			bay := &dock.Bays[j]
			for _, s := range bay.Surfaces {
				if s.Tmux != nil && s.Tmux.WindowID == windowID {
					return dock.Name, bay
				}
			}
		}
	}
	return "", nil
}
