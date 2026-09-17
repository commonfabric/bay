package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/commonfabric/bay/internal/describe"
	"github.com/spf13/cobra"
)

func newDescribeWorkerCmd() *cobra.Command {
	var dockName string
	var bayID string
	cmd := &cobra.Command{
		Use:    "describe-worker",
		Short:  "Generate a bay description from its agent conversation",
		Hidden: true,
		Annotations: map[string]string{
			noMonitorAutostartAnnotation: "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			p := bayPaths()
			worker := describe.Worker{
				Config:       eng.Config,
				ManifestPath: p.ManifestFile,
				DataDir:      p.DataDir,
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			err = worker.Run(ctx, describe.Options{Dock: dockName, Bay: bayID})
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&dockName, "dock", "", "dock name")
	cmd.Flags().StringVar(&bayID, "bay", "", "bay ID")
	_ = cmd.MarkFlagRequired("dock")
	_ = cmd.MarkFlagRequired("bay")
	return cmd
}
