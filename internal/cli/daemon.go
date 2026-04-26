package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/config"
	syncpkg "github.com/zach-source/ccswitch/internal/sync"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newDaemonCmd)
}

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the sync loop continuously, honoring SIGINT/SIGTERM",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			engine, err := newEngine(cmd, cfg)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return syncpkg.NewDaemon(engine, cfg.Sync.Interval, nil).Run(ctx)
		},
	}
}
