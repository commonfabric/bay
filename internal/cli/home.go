package cli

import (
	"github.com/commonfabric/bay/internal/engine"
	"github.com/spf13/cobra"
)

func newHomeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "home",
		Short: "Focus or create the dock checkout shell",
		Long: `Focus the dock checkout shell. If home has no surfaces yet,
create a shell surface at the dock checkout path.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}
			return runHome(eng)
		},
	}
}

func runHome(eng *engine.Engine) error {
	dockName, err := resolveCurrentDock(eng)
	if err != nil {
		return err
	}
	return eng.Home(dockName)
}
