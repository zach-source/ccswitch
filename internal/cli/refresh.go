package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/config"
	"github.com/zach-source/ccswitch/internal/refresh"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newRefreshAllCmd)
	subcommandBuilders = append(subcommandBuilders, newLoginCmd)
}

// cliLogger returns a slog.Logger that prints compact "msg key=val" lines to
// stderr — readable as CLI progress output rather than a log file. When quiet
// is set, only warnings and errors are emitted.
func cliLogger(quiet bool) *slog.Logger {
	level := slog.LevelInfo
	if quiet {
		level = slog.LevelWarn
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// Drop time/level keys — this is user-facing, not a log file.
			if a.Key == slog.TimeKey || a.Key == slog.LevelKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

func newRefreshAllCmd() *cobra.Command {
	var quiet bool

	cmd := &cobra.Command{
		Use:   "refresh-all",
		Short: "Refresh expired OAuth tokens for all managed accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			if len(seq.Sequence) == 0 {
				fmt.Println("No accounts are managed yet.")
				return nil
			}
			b, err := resolveBackend(cfg)
			if err != nil {
				return fmt.Errorf("backend not available: %w", err)
			}
			// claude 2.x on macOS writes refreshed credentials to the local
			// keychain, not to CLAUDE_CONFIG_DIR. RefreshOne captures from
			// the local active slot when the file is absent, so the local
			// backend has to be passed in.
			localCfg := *cfg
			localCfg.Backend = autoLocalBackend()
			local, err := resolveBackend(&localCfg)
			if err != nil {
				return fmt.Errorf("local backend not available: %w", err)
			}

			ctx := cmd.Context()

			// Mirror ccswitch.sh step 1: snapshot the active account's live
			// credentials into its backup slot so a freshly-authenticated
			// active account is never reported as stale.
			syncActiveToBackup(ctx, b, seq)

			n, err := refresh.RefreshAll(ctx, seq, b, local, cfg.Refresh.ExpiryBuffer, cliLogger(quiet))
			// Print the summary either way, then surface any partial-failure
			// error so the process exits non-zero for cron/launchd wrappers.
			fmt.Printf("refresh-all: %d account(s) refreshed.\n", n)
			return err
		},
	}
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Only print warnings and the final summary")
	return cmd
}

func newLoginCmd() *cobra.Command {
	var only string
	var force bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Interactively (re-)authenticate accounts with missing or expired credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			if len(seq.Sequence) == 0 {
				fmt.Println("No accounts are managed yet.")
				return nil
			}
			b, err := resolveBackend(cfg)
			if err != nil {
				return fmt.Errorf("backend not available: %w", err)
			}
			// claude 2.x on macOS writes credentials to the login keychain,
			// not a file under CLAUDE_CONFIG_DIR. LoginRotate captures from
			// the local backend's active slot when the legacy file is
			// absent, so the local backend has to be passed in.
			localCfg := *cfg
			localCfg.Backend = autoLocalBackend()
			local, err := resolveBackend(&localCfg)
			if err != nil {
				return fmt.Errorf("local backend not available: %w", err)
			}

			// --only narrows the sequence to a single account.
			target := seq
			if only != "" {
				id := seq.Resolve(only)
				if id == "" {
					return fmt.Errorf("no account found matching: %s", only)
				}
				narrowed := *seq
				narrowed.Sequence = []string{id}
				target = &narrowed
			}

			_, err = refresh.LoginRotate(cmd.Context(), target, b, local,
				cfg.Refresh.ExpiryBuffer, force, cliLogger(false))
			return err
		},
	}
	cmd.Flags().StringVar(&only, "only", "", "Log in to a single account (hash|email|index)")
	cmd.Flags().BoolVar(&force, "force", false, "Re-login every selected account even if its credentials are valid")
	return cmd
}

// syncActiveToBackup copies the active credential slot into the active
// account's backup key. Best-effort: any error is swallowed because the
// subsequent refresh pass surfaces real problems per-account.
func syncActiveToBackup(ctx context.Context, b backend.Backend, seq *account.Sequence) {
	id := activeID(seq)
	if id == "" {
		return
	}
	acct, ok := seq.Accounts[id]
	if !ok {
		return
	}
	data, err := b.Read(ctx, account.ActiveCredKey)
	if err != nil || len(data) == 0 {
		return
	}
	_ = b.Write(ctx, account.BackupCredKey(id, acct.Email), data)
}
