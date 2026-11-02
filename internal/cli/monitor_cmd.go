package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/commonfabric/bay/internal/monitor"
	"github.com/spf13/cobra"
)

func newMonitorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Manage the pane monitor",
		// Don't auto-start the monitor when the user is explicitly
		// running monitor subcommands — `monitor stop` auto-starting
		// then stopping is absurd, `monitor status` should be a pure
		// inspector, and `monitor run` is the monitor itself.
		Annotations: map[string]string{noMonitorAutostartAnnotation: "true"},
	}

	cmd.AddCommand(
		newMonitorStartCmd(),
		newMonitorStopCmd(),
		newMonitorStatusCmd(),
		newMonitorRunCmd(),
	)

	return cmd
}

func newMonitorWithConfig() (*monitor.Monitor, error) {
	eng, err := newEngine()
	if err != nil {
		return nil, err
	}
	p := bayPaths()
	cfg := eng.Config
	mon := monitor.NewWithGit(eng.Tmux, eng.Git, p.ManifestFile, p.PatternsFile, p.PIDFile, cfg.Monitor.EffectiveInterval())
	mon.SetEngine(eng)
	mon.SetRuntimeStatus(p.MonitorStatus, cliVersion)
	return mon, nil
}

func newMonitorStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the pane monitor as a background process",
		RunE: func(cmd *cobra.Command, args []string) error {
			mon, err := newMonitorWithConfig()
			if err != nil {
				return err
			}
			return mon.Start()
		},
	}
}

func newMonitorRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "run",
		Short:  "Run the monitor in the foreground (used by 'start')",
		Hidden: true, // internal subcommand
		RunE: func(cmd *cobra.Command, args []string) error {
			mon, err := newMonitorWithConfig()
			if err != nil {
				return err
			}

			// Write our own PID
			if err := monitor.WritePIDFile(bayPaths().PIDFile, os.Getpid()); err != nil {
				return err
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Handle signals for clean shutdown
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
			go func() {
				<-sigCh
				cancel()
			}()

			err = mon.Run(ctx)
			// context.Canceled is the normal shutdown path (SIGINT/
			// SIGTERM received). Don't report it as an error — the
			// monitor shares stderr with the terminal, so "Error:
			// context canceled" would confuse users running
			// `bay monitor stop`.
			if err != nil && err == context.Canceled {
				return nil
			}
			return err
		},
	}
}

func newMonitorStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the pane monitor",
		RunE: func(cmd *cobra.Command, args []string) error {
			mon, err := newMonitorWithConfig()
			if err != nil {
				return err
			}
			return mon.Stop()
		},
	}
}

func newMonitorStatusCmd() *cobra.Command {
	var verbose bool
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check if the monitor is running",
		RunE: func(cmd *cobra.Command, args []string) error {
			if verbose && jsonOutput {
				return fmt.Errorf("--verbose and --json are mutually exclusive")
			}
			assessment, err := monitorStatusAssessment()
			if err != nil {
				if jsonOutput {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Monitor status unavailable: %v\n", err)
				return nil
			}
			if jsonOutput {
				data, err := json.MarshalIndent(assessment, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return nil
			}
			if verbose {
				fmt.Fprint(cmd.OutOrStdout(), formatMonitorStatusVerbose(assessment))
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), formatMonitorStatusOneLine(assessment))
			return nil
		},
	}
	cmd.Flags().BoolVar(&verbose, "verbose", false, "show detailed monitor status")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

// monitorStatusString returns the one-line status of the pane monitor.
// Shared by `bay monitor status` and the palette's "Monitor status" entry.
// Any error (config load, status probe) is folded into the string so
// callers can always display it directly.
func monitorStatusString() string {
	assessment, err := monitorStatusAssessment()
	if err != nil {
		return fmt.Sprintf("Monitor status unavailable: %v", err)
	}
	return formatMonitorStatusOneLine(assessment)
}

func monitorStatusAssessment() (monitor.StatusAssessment, error) {
	mon, err := newMonitorWithConfig()
	if err != nil {
		return monitor.StatusAssessment{}, err
	}
	return mon.StatusAssessment(cliVersion)
}

func formatMonitorStatusOneLine(assessment monitor.StatusAssessment) string {
	if !assessment.Running {
		return "Monitor not running"
	}
	return fmt.Sprintf("Monitor running (pid %d, %s)", assessment.PID, monitorStatusDetail(assessment))
}

func monitorStatusDetail(assessment monitor.StatusAssessment) string {
	currentShort := shortVersion(assessment.Current.Version)
	switch assessment.Freshness {
	case monitor.StatusFreshnessCurrent:
		if assessment.Reason == monitor.StatusReasonHeartbeatStale {
			return fmt.Sprintf("current %s, heartbeat stale: seen %s ago", currentShort, monitorSeenAgo(assessment))
		}
		return fmt.Sprintf("current %s, seen %s ago", currentShort, monitorSeenAgo(assessment))
	case monitor.StatusFreshnessStale:
		if assessment.Reason == monitor.StatusReasonVersionMismatch && assessment.Monitor != nil {
			return fmt.Sprintf("stale: monitor %s, bay %s", shortVersion(assessment.Monitor.Version), currentShort)
		}
		return fmt.Sprintf("stale: %s", humanMonitorStatusReason(assessment.Reason))
	case monitor.StatusFreshnessUnknown:
		return fmt.Sprintf("version unknown: %s", humanMonitorStatusReason(assessment.Reason))
	case monitor.StatusFreshnessDifferentExecutable:
		path := ""
		if assessment.Monitor != nil {
			path = assessment.Monitor.Path
		}
		if path == "" {
			path = "unknown path"
		}
		return fmt.Sprintf("current %s, different path: %s", currentShort, path)
	default:
		return "status unknown"
	}
}

func formatMonitorStatusVerbose(assessment monitor.StatusAssessment) string {
	var sb strings.Builder
	if assessment.Running {
		fmt.Fprintln(&sb, "running: yes")
	} else {
		fmt.Fprintln(&sb, "running: no")
	}
	fmt.Fprintf(&sb, "pid: %d\n", assessment.PID)
	fmt.Fprintf(&sb, "freshness: %s\n", assessment.Freshness)
	if assessment.Reason != "" {
		fmt.Fprintf(&sb, "reason: %s\n", assessment.Reason)
	}
	if assessment.Monitor != nil {
		fmt.Fprintf(&sb, "monitor version: %s\n", assessment.Monitor.Version)
		fmt.Fprintf(&sb, "monitor executable: %s\n", assessment.Monitor.Path)
		fmt.Fprintf(&sb, "monitor binary: %s\n", formatBinaryIdentity(assessment.Monitor.BinaryIdentity))
		fmt.Fprintf(&sb, "started: %s\n", formatStatusTime(assessment.Monitor.StartedAt))
		fmt.Fprintf(&sb, "last heartbeat: %s\n", formatStatusTime(assessment.Monitor.LastSeenAt))
		fmt.Fprintf(&sb, "reload generation: %d\n", assessment.Monitor.Generation)
	}
	fmt.Fprintf(&sb, "current bay version: %s\n", assessment.Current.Version)
	fmt.Fprintf(&sb, "current executable: %s\n", assessment.Current.Path)
	fmt.Fprintf(&sb, "current binary: %s\n", formatBinaryIdentity(assessment.Current.BinaryIdentity))
	if p := bayPaths().MonitorStatus; p != "" {
		fmt.Fprintf(&sb, "status file: %s\n", p)
	}
	return sb.String()
}

func shortVersion(version string) string {
	version = strings.TrimSpace(version)
	if open := strings.LastIndex(version, "("); open >= 0 && strings.HasSuffix(version, ")") {
		inner := strings.TrimSpace(strings.TrimSuffix(version[open+1:], ")"))
		if inner != "" {
			return inner
		}
	}
	if version == "" {
		return "unknown"
	}
	if len(version) > 8 {
		return version[:8]
	}
	return version
}

func monitorSeenAgo(assessment monitor.StatusAssessment) string {
	if assessment.Monitor == nil || assessment.Monitor.LastSeenAt.IsZero() {
		return "unknown"
	}
	age := time.Since(assessment.Monitor.LastSeenAt)
	if age < 0 {
		age = 0
	}
	return age.Round(time.Second).String()
}

func humanMonitorStatusReason(reason string) string {
	switch reason {
	case monitor.StatusReasonMetadataMissing:
		return "metadata missing"
	case monitor.StatusReasonMetadataInvalid:
		return "metadata invalid"
	case monitor.StatusReasonMetadataPIDMismatch:
		return "metadata belongs to another pid"
	case monitor.StatusReasonHeartbeatStale:
		return "heartbeat stale"
	case monitor.StatusReasonVersionMismatch:
		return "version mismatch"
	case monitor.StatusReasonBinaryChangedSamePath:
		return "binary changed at same path"
	case monitor.StatusReasonDifferentExecutable:
		return "different executable"
	case "":
		return "no reason reported"
	default:
		return reason
	}
}

func formatBinaryIdentity(id monitor.BinaryIdentity) string {
	var parts []string
	if id.Ino != 0 {
		parts = append(parts, fmt.Sprintf("ino=%d", id.Ino))
	}
	if id.Dev != 0 {
		parts = append(parts, fmt.Sprintf("dev=%d", id.Dev))
	}
	if id.MTimeUnixNano != 0 {
		parts = append(parts, "mtime="+formatStatusTime(time.Unix(0, id.MTimeUnixNano)))
	}
	if id.Size != 0 {
		parts = append(parts, fmt.Sprintf("size=%d", id.Size))
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " ")
}

func formatStatusTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format(time.RFC3339)
}
