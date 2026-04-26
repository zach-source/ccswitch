package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/config"
	syncpkg "github.com/zach-source/ccswitch/internal/sync"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newSyncCmd)
}

func newSyncCmd() *cobra.Command {
	var quiet bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Bi-directional sync between local and 1Password (newest-expiresAt-wins per item)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			res, err := runSync(cmd, cfg)
			if err != nil {
				return err
			}
			if !quiet {
				fmt.Printf("Sync complete: %d pushed, %d pulled, %d unchanged, %d errors\n",
					res.Pushed, res.Pulled, res.Unchanged, res.Errors)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Suppress informational output")
	return cmd
}

// runSync wires the cobra command to internal/sync.Engine. The same
// path is used by `daemon` (via NewDaemon).
func runSync(cmd *cobra.Command, cfg *config.Config) (syncpkg.Result, error) {
	var zero syncpkg.Result
	engine, err := newEngine(cmd, cfg)
	if err != nil {
		return zero, err
	}
	return engine.Run(cmd.Context())
}

// newEngine constructs a sync.Engine wired to the configured local +
// remote backends and the on-disk sequence file. The local backend is
// always the auto-resolved default (keychain on darwin, file otherwise);
// the remote backend is whatever cfg.Backend selects.
func newEngine(cmd *cobra.Command, cfg *config.Config) (*syncpkg.Engine, error) {
	remote, err := resolveBackend(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve backend: %w", err)
	}
	if err := remote.HealthCheck(cmd.Context()); err != nil {
		return nil, fmt.Errorf("backend health check: %w", err)
	}

	localCfg := *cfg
	localCfg.Backend = autoLocalBackend()
	local, err := resolveBackend(&localCfg)
	if err != nil {
		return nil, fmt.Errorf("resolve local backend: %w", err)
	}

	seq, err := account.LoadSequence(sequencePath())
	if err != nil {
		return nil, err
	}

	return syncpkg.New(local, remote, seq, syncpkg.Options{
		ExpiryBuffer: cfg.Refresh.ExpiryBuffer,
	}), nil
}
