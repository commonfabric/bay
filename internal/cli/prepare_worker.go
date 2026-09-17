package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/commonfabric/bay/internal/prepare"
	"github.com/spf13/cobra"
)

func newPrepareWorkerCmd() *cobra.Command {
	var dockName string
	var bayID string
	cmd := &cobra.Command{
		Use:    "prepare-worker",
		Short:  "Run bay prepare steps",
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
			worker := prepare.Worker{
				Config:       eng.Config,
				ManifestPath: p.ManifestFile,
				DataDir:      p.DataDir,
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			err = worker.Run(ctx, prepare.Options{Dock: dockName, Bay: bayID})
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
