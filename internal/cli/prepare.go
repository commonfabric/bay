package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/commonfabric/bay/internal/manifest"
	"github.com/commonfabric/bay/internal/prepare"
	"github.com/spf13/cobra"
)

func newPrepareCmd() *cobra.Command {
	var dockFlag string
	var retry bool
	var wait bool
	var timeout time.Duration
	var logOutput bool
	var follow bool
	var kill bool

	cmd := &cobra.Command{
		Use:   "prepare [bay-id]",
		Short: "Inspect or control bay prepare steps",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if logOutput && (retry || wait || kill) {
				return fmt.Errorf("--log cannot be combined with --retry, --wait, or --kill")
			}
			if kill && (retry || wait) {
				return fmt.Errorf("--kill cannot be combined with --retry or --wait")
			}
			if follow && !logOutput {
				return fmt.Errorf("-f requires --log")
			}

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
				return err
			}

			paths := bayPaths()
			manager := prepare.Manager{
				Config:       eng.Config,
				ManifestPath: paths.ManifestFile,
				DataDir:      paths.DataDir,
			}
			opts := prepare.Options{Dock: dockName, Bay: bayID}
			out := cmd.OutOrStdout()

			if logOutput {
				return runPrepareLog(cmd.Context(), out, manager, opts, follow, timeout)
			}
			if kill {
				names, err := manager.Kill(cmd.Context(), opts, 5*time.Second)
				if err != nil {
					return err
				}
				if len(names) == 0 {
					fmt.Fprintln(out, "prepare not running")
				} else {
					fmt.Fprintf(out, "prepare stopped: %s\n", strings.Join(names, ","))
				}
				return nil
			}
			if retry {
				names, err := manager.Retry(opts)
				if err != nil {
					return err
				}
				if len(names) == 0 {
					fmt.Fprintln(out, "prepare is already ready")
				} else {
					if err := eng.DispatchPrepareWorker(dockName, bayID); err != nil {
						return err
					}
					fmt.Fprintf(out, "prepare retry started: %s\n", strings.Join(names, ","))
				}
			}
			if wait {
				statuses, err := waitForPrepare(cmd.Context(), manager, opts, timeout)
				if err != nil {
					return err
				}
				return printPrepareStatus(out, dockName, bayID, statuses)
			}

			statuses, err := manager.Status(opts)
			if err != nil {
				return err
			}
			return printPrepareStatus(out, dockName, bayID, statuses)
		},
	}

	cmd.Flags().StringVar(&dockFlag, "dock", "", "dock name (disambiguates a bare bay ID)")
	cmd.Flags().BoolVar(&retry, "retry", false, "retry failed, stale, or pending prepare steps")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until prepare reaches a terminal status")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "maximum wait duration")
	cmd.Flags().BoolVar(&logOutput, "log", false, "print today's prepare log for the bay")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow the in-flight prepare log")
	cmd.Flags().BoolVar(&kill, "kill", false, "stop running prepare worker steps")

	return cmd
}

func printPrepareStatus(out io.Writer, dockName, bayID string, statuses []prepare.StepStatus) error {
	if len(statuses) == 0 {
		_, err := fmt.Fprintf(out, "%s:%s has no prepare steps\n", dockName, bayID)
		return err
	}
	if _, err := fmt.Fprintf(out, "bay %s:%s\n", dockName, bayID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "%-24s %-8s %s\n", "step", "status", "pid"); err != nil {
		return err
	}
	for _, status := range statuses {
		pid := ""
		if status.PID != 0 {
			pid = fmt.Sprintf("%d", status.PID)
		}
		if _, err := fmt.Fprintf(out, "%-24s %-8s %s\n", status.Name, status.Status, pid); err != nil {
			return err
		}
	}
	return nil
}

func waitForPrepare(ctx context.Context, manager prepare.Manager, opts prepare.Options, timeout time.Duration) ([]prepare.StepStatus, error) {
	waitCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		statuses, err := manager.Status(opts)
		if err != nil {
			return nil, err
		}
		if prepare.AllTerminal(statuses) {
			return statuses, nil
		}
		select {
		case <-waitCtx.Done():
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("timed out waiting for prepare after %s", timeout)
			}
			return nil, waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func runPrepareLog(ctx context.Context, out io.Writer, manager prepare.Manager, opts prepare.Options, follow bool, timeout time.Duration) error {
	logPath := prepare.TodayLogPath(manager.DataDir, opts.Dock, time.Now())
	if !follow {
		return prepare.FilterRunsForBayFile(out, logPath, opts.Bay)
	}

	followCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		followCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	offset, ok, err := waitForRunLogOffset(followCtx, manager, opts)
	if err != nil {
		return err
	}
	if !ok {
		return prepare.FilterRunsForBayFile(out, logPath, opts.Bay)
	}
	return followPrepareLog(followCtx, out, logPath, offset, manager, opts)
}

func waitForRunLogOffset(ctx context.Context, manager prepare.Manager, opts prepare.Options) (int64, bool, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		statuses, err := manager.Status(opts)
		if err != nil {
			return 0, false, err
		}
		for _, status := range statuses {
			if status.Status == manifest.PrepareStatusRunning {
				return status.RunLogOffset, true, nil
			}
		}
		if prepare.AllTerminal(statuses) {
			return 0, false, nil
		}
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func followPrepareLog(ctx context.Context, out io.Writer, path string, offset int64, manager prepare.Manager, opts prepare.Options) error {
	pos := offset
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := copyPrepareLogFrom(out, path, &pos); err != nil {
			return err
		}
		statuses, err := manager.Status(opts)
		if err != nil {
			return err
		}
		if prepare.AllTerminal(statuses) {
			return copyPrepareLogFrom(out, path, &pos)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func copyPrepareLogFrom(out io.Writer, path string, pos *int64) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	if _, err := f.Seek(*pos, io.SeekStart); err != nil {
		return err
	}
	n, err := io.Copy(out, f)
	*pos += n
	return err
}
