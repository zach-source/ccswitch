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
	var force bool

	cmd := &cobra.Command{
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
			res, err := engine.Push(cmd.Context(), force)
			if err != nil {
				return err
			}
			fmt.Printf("Summary: %d pushed, %d skipped, %d errors\n", res.Pushed, res.Unchanged, res.Errors)
			if res.Unchanged > 0 && !force {
				fmt.Println("Skipped accounts are fresher on the remote — " +
					"run `ccswitch pull` first, or `ccswitch push --force` to overwrite.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"Overwrite remote entries even when the remote copy is fresher")
	return cmd
}
