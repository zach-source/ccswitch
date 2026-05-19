package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newPushCmd)
}

func newPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Push local credentials + sequence.json to the configured backend",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			engine, err := newEngine(cmd, cfg)
			if err != nil {
				return err
			}
			fmt.Printf("Pushing credentials to %s...\n", resolvedBackendName(cfg))
			res, err := engine.Push(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("Summary: %d pushed, %d errors\n", res.Pushed, res.Errors)
			return nil
		},
	}
}
