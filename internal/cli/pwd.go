package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/commonfabric/bay/internal/engine"
	"github.com/spf13/cobra"
)

func newPwdCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "pwd",
		Short: "Show the current Bay context",
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := newEngine()
			if err != nil {
				return err
			}

			ctx, err := eng.CurrentContext()
			if err != nil {
				return err
			}

			if jsonOutput {
				data, err := json.MarshalIndent(ctx, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			fmt.Println(formatPWD(ctx))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func formatPWD(ctx *engine.Context) string {
	parts := make([]string, 0, 5)
	if ctx.Dock != "" {
		parts = append(parts, labelValue("dock", ctx.Dock))
	}
	// The canonical bay label already carries the dir tag (e.g. "b1.auth-fix"),
	// so no separate "(dir b1)" parenthetical is needed.
	if label := ctx.BayLabel(); label != "" {
		parts = append(parts, labelValue("bay", label))
	}
	if ctx.Surface != "" {
		parts = append(parts, labelValue("surface", ctx.Surface))
	}
	return strings.Join(parts, dimmedSeparator(" / "))
}
