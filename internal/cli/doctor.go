package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/commonfabric/bay/internal/config"
	"github.com/commonfabric/bay/internal/engine"
	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/monitor"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run health checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return fmt.Errorf("config: %v", err)
			}
			runDoctor(eng, os.Stdout)
			return nil
		},
	}
}

// runDoctor writes health-check results to w. Shared by `bay doctor` and
// the palette's "Run doctor" entry (which captures the output to show it
// in the popup rather than the underlying pane).
func runDoctor(eng *engine.Engine, w io.Writer) {
	ok := true

	// Check tmux installed
	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Fprintln(w, "[WARN] tmux not found in PATH")
		ok = false
	} else {
		fmt.Fprintln(w, "[OK] tmux installed")
	}

	// Check agents on PATH
	for name, agent := range eng.Config.Agents {
		if _, err := exec.LookPath(agent.Command); err != nil {
			fmt.Fprintf(w, "[INFO] agent %q (%s) not found in PATH\n", name, agent.Command)
		} else {
			fmt.Fprintf(w, "[OK] agent %q available\n", name)
		}
	}

	// Check configured editor
	if eng.Config.DefaultEditor != "" {
		if _, err := exec.LookPath(eng.Config.DefaultEditor); err != nil {
			fmt.Fprintf(w, "[WARN] configured editor %q not found in PATH\n", eng.Config.DefaultEditor)
			ok = false
		} else {
			fmt.Fprintf(w, "[OK] editor %q available\n", eng.Config.DefaultEditor)
		}
	}

	// Check config validation
	errs := eng.Config.Validate()
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(w, "[WARN] config: %s\n", e)
		}
		ok = false
	} else {
		fmt.Fprintln(w, "[OK] config valid")
	}

	// Check dock and bay paths in manifest.
	m, loadErr := eng.LoadManifest()
	if loadErr != nil {
		fmt.Fprintf(w, "[WARN] could not load manifest: %v\n", loadErr)
		ok = false
	} else {
		missingCount := 0
		worktreesWithBranches := 0
		for i := range m.Docks {
			dock := &m.Docks[i]
			if dock.Path != "" {
				path := config.ExpandPath(dock.Path)
				if _, err := os.Stat(path); err != nil {
					fmt.Fprintf(w, "[WARN] dock %q checkout path %s not accessible\n", dock.Name, path)
					ok = false
				} else {
					fmt.Fprintf(w, "[OK] dock %q checkout accessible\n", dock.Name)
					checkDockAwareness(eng, dock, path, w)
				}
			}
			for j := range dock.Bays {
				bay := &dock.Bays[j]
				if bay.Worktree != nil && bay.Worktree.Branch != "" {
					worktreesWithBranches++
				}
				if bay.Path == "" {
					continue
				}
				bayPath := config.ExpandPath(bay.Path)
				if _, err := os.Stat(bayPath); err != nil {
					fmt.Fprintf(w, "[WARN] bay %s:%s path missing: %s\n", dock.Name, engine.BayCompactLabel(bay), bayPath)
					missingCount++
					ok = false
				}
			}
		}
		if missingCount == 0 {
			fmt.Fprintln(w, "[OK] all bay paths exist")
		}

		// Warn if gh is missing but we have worktrees that would
		// benefit from PR auto-detection.
		if worktreesWithBranches > 0 {
			if _, ghErr := exec.LookPath("gh"); ghErr != nil {
				fmt.Fprintln(w, "[INFO] gh not installed — PR numbers won't be auto-detected for branches")
			} else {
				fmt.Fprintln(w, "[OK] gh available (PR auto-detection enabled)")
			}
		}

		manifestWarnings := checkManifestConsistency(m, eng.Config)
		if len(manifestWarnings) > 0 {
			for _, warning := range manifestWarnings {
				fmt.Fprintf(w, "[WARN] manifest: %s\n", warning)
			}
			ok = false
		} else {
			fmt.Fprintln(w, "[OK] manifest consistent")
		}
	}

	// Check tmux keybindings
	tmuxConfPath := tmuxConfPath()
	if data, err := os.ReadFile(tmuxConfPath); err != nil {
		fmt.Fprintf(w, "[WARN] tmux keybindings not installed (%s not readable)\n", tmuxConfPath)
		ok = false
	} else {
		missing := missingKeybindings(string(data))
		if len(missing) > 0 {
			fmt.Fprintf(w, "[WARN] tmux keybindings missing: %s\n", strings.Join(missing, ", "))
			ok = false
		} else {
			fmt.Fprintln(w, "[OK] tmux keybindings installed")
		}
	}

	// Check monitor.
	mon, monCfgErr := newMonitorWithConfig()
	if monCfgErr != nil {
		fmt.Fprintf(w, "[WARN] monitor: %v\n", monCfgErr)
		ok = false
	} else {
		assessment, monErr := mon.StatusAssessment(cliVersion)
		if monitorAssessmentLooksUnstarted(assessment, monErr) {
			// Lazy retry: the auto-start hook in
			// PersistentPreRunE forks `bay monitor run` before
			// doctor's RunE runs, but the forked child writes
			// its PID file asynchronously. A first-call check
			// that runs before the child has finished startup may
			// report "not running" or live-without-metadata. The
			// retry only fires on that negative path so the steady
			// state is still instant.
			time.Sleep(150 * time.Millisecond)
			assessment, monErr = mon.StatusAssessment(cliVersion)
		}
		switch {
		case monErr != nil || !assessment.Running:
			fmt.Fprintln(w, "[WARN] monitor not running")
			ok = false
		case monitorAssessmentDegraded(assessment):
			fmt.Fprintf(w, "[WARN] %s\n", lowerFirst(formatMonitorStatusOneLine(assessment)))
			ok = false
		default:
			fmt.Fprintf(w, "[OK] %s\n", lowerFirst(formatMonitorStatusOneLine(assessment)))
		}
	}

	if !ok {
		fmt.Fprintln(w, "\nSome checks failed. Run `bay setup` to fix.")
	} else {
		fmt.Fprintln(w, "\nAll checks passed.")
	}
}

// checkDockAwareness prints INFO lines for missing bay-awareness setup
// in a dock's worktree dir and checkout (path is the expanded checkout
// path). Non-git checkouts are skipped entirely: `bay dock init` leaves
// them alone — no worktree bays, no gitignore — so there is nothing to
// nudge toward.
func checkDockAwareness(eng *engine.Engine, dock *manifest.Dock, path string, w io.Writer) {
	if !eng.Git.IsGitRepo(path) {
		return
	}
	wtAwareness := filepath.Join(dock.EffectiveWorktreeDir(), engine.WorktreeAwarenessFile)
	if data, err := os.ReadFile(wtAwareness); err != nil || !strings.Contains(string(data), engine.BayAgentGuideRef) {
		fmt.Fprintf(w, "[INFO] dock %q: worktree dir missing bay awareness — run `bay dock init %s`\n", dock.Name, dock.Name)
	}
	for _, pf := range eng.Config.AgentProjectFiles() {
		data, readErr := os.ReadFile(filepath.Join(path, pf.File))
		if readErr == nil && strings.Contains(string(data), engine.BayAgentGuideRef) {
			continue
		}
		ignored, _ := eng.Git.IsIgnored(path, pf.File)
		switch {
		case !ignored:
			fmt.Fprintf(w, "[INFO] dock %q checkout: %s not gitignored — bay leaves it alone; gitignore it and run `bay dock init %s` for checkout bay awareness\n", dock.Name, pf.File, dock.Name)
		case readErr != nil:
			fmt.Fprintf(w, "[INFO] dock %q checkout: %s not found — run `bay dock init %s`\n", dock.Name, pf.File, dock.Name)
		default:
			fmt.Fprintf(w, "[INFO] dock %q checkout: %s missing bay awareness for %s — run `bay dock init %s`\n", dock.Name, pf.File, pf.Agent, dock.Name)
		}
	}
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// monitorAssessmentLooksUnstarted returns true if the assessment looks like
// the monitor was just forked but hasn't finished writing its files yet.
func monitorAssessmentLooksUnstarted(a monitor.StatusAssessment, err error) bool {
	if err != nil || !a.Running {
		return true
	}
	return a.Freshness == monitor.StatusFreshnessUnknown && a.Reason == monitor.StatusReasonMetadataMissing
}

// monitorAssessmentDegraded reports whether a running monitor is in a state
// that warrants a doctor warning rather than an OK.
func monitorAssessmentDegraded(a monitor.StatusAssessment) bool {
	if a.Freshness == monitor.StatusFreshnessStale || a.Freshness == monitor.StatusFreshnessUnknown {
		return true
	}
	return a.Reason == monitor.StatusReasonHeartbeatStale
}

func missingKeybindings(content string) []string {
	kept := map[string]bool{}
	if block, found := extractBayBlock(content); found {
		kept = keptKeys(block)
	}
	activeLines := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		activeLines[trimmed] = true
	}
	var missing []string
	for _, kb := range bayKeybindings {
		if kept[kb.id()] {
			continue
		}
		line := kb.canonicalLine()
		if !activeLines[line] {
			missing = append(missing, line)
		}
	}
	return missing
}

func tmuxKeybindingLines() []string {
	var lines []string
	for _, kb := range bayKeybindings {
		lines = append(lines, kb.canonicalLine())
	}
	return lines
}

func tmuxConfPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tmux.conf")
}

func checkManifestConsistency(m *manifest.Manifest, cfg *config.Config) []string {
	var warnings []string

	for i := range m.Docks {
		dock := &m.Docks[i]
		seenNames := map[string]bool{}
		for j := range dock.Bays {
			bay := &dock.Bays[j]
			if bay.Name != "" {
				if seenNames[bay.Name] {
					warnings = append(warnings, fmt.Sprintf("dock %s has duplicate bay name %q", dock.Name, bay.Name))
				}
				seenNames[bay.Name] = true
			}

			bayLabel := engine.BayCompactLabel(bay)
			seenSurfaceIDs := map[int]bool{}
			seenSurfaceNames := map[string]bool{}
			for _, s := range bay.Surfaces {
				if seenSurfaceIDs[s.ID] {
					warnings = append(warnings, fmt.Sprintf("bay %s:%s has duplicate surface id %d", dock.Name, bayLabel, s.ID))
				}
				seenSurfaceIDs[s.ID] = true
				if seenSurfaceNames[s.Name] {
					warnings = append(warnings, fmt.Sprintf("bay %s:%s has duplicate surface name %q", dock.Name, bayLabel, s.Name))
				}
				seenSurfaceNames[s.Name] = true
				for _, e := range s.Validate() {
					warnings = append(warnings, fmt.Sprintf("bay %s:%s: %s", dock.Name, bayLabel, e))
				}
			}
		}
	}

	return warnings
}
