package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newPullCmd)
}

func newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Pull credentials + sequence.json from the configured backend",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			engine, err := newEngine(cmd, cfg)
			if err != nil {
				return err
			}
			fmt.Printf("Pulling credentials from %s...\n", resolvedBackendName(cfg))
			res, err := engine.Pull(cmd.Context())
			if err != nil {
				return err
			}
			// Persist the merged sequence (Engine.Pull updated the in-memory copy).
			if err := engine.Sequence().Save(sequencePath()); err != nil {
				return fmt.Errorf("save sequence: %w", err)
			}
			fmt.Printf("Summary: %d pulled, %d errors\n", res.Pulled, res.Errors)
			return nil
		},
	}
}

// Compile-time check that Engine exposes the Sequence accessor we need.
var _ = (*account.Sequence)(nil)
