package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/config"
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

			interval := cfg.Sync.Interval
			if interval <= 0 {
				interval = 5 * time.Minute
			}

			home, _ := os.UserHomeDir()
			logFile := os.Getenv("CCSWITCH_DAEMON_LOG")
			if logFile == "" {
				logFile = filepath.Join(home, ".claude-switch-backup", "daemon.log")
			}

			fmt.Printf("ccswitch sync daemon starting (interval: %s, log: %s)\n", interval, logFile)
			_ = os.MkdirAll(filepath.Dir(logFile), 0o700)

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					msg := fmt.Sprintf("[%s] daemon stopped", time.Now().UTC().Format(time.RFC3339))
					logToFile(logFile, msg)
					return nil
				case t := <-ticker.C:
					msg := fmt.Sprintf("[%s] sync tick", t.UTC().Format(time.RFC3339))
					logToFile(logFile, msg)
					if err := runSync(cfg, true); err != nil {
						logToFile(logFile, fmt.Sprintf("sync error: %v", err))
					}
				}
			}
		},
	}
}
