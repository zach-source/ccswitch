package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/config"
	"github.com/zach-source/ccswitch/internal/credentials"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newSaveCmd)
}

func newSaveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "save",
		Short: "Save current ~/.claude state into the active account's backup slot",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}

			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			if seq.ActiveAccountID == "" {
				return fmt.Errorf("no active account in sequence.json")
			}

			activeEmail := currentEmail()
			if activeEmail == "" {
				return fmt.Errorf("no active Claude account found")
			}

			activeAcct := seq.Accounts[seq.ActiveAccountID]
			if activeEmail != activeAcct.Email {
				fmt.Fprintf(os.Stderr, "Warning: active account (%s) doesn't match expected (%s)\n",
					activeEmail, activeAcct.Email)
			}

			// Read active credentials from the configured backend.
			b, err := resolveBackend(cfg)
			if err != nil {
				return fmt.Errorf("backend not available: %w", err)
			}

			ctx := context.Background()
			credsData, err := b.Read(ctx, "Claude Code-credentials")
			if err != nil {
				return fmt.Errorf("read active credentials: %w", err)
			}

			creds, err := credentials.Parse(credsData)
			if err != nil {
				return fmt.Errorf("parse credentials: %w", err)
			}

			// Write backup.
			backupKey := fmt.Sprintf("Claude Code Account - %s-%s", seq.ActiveAccountID, activeAcct.Email)
			if err := b.Write(ctx, backupKey, credsData); err != nil {
				return fmt.Errorf("write backup credentials: %w", err)
			}

			hoursLeft := creds.HoursLeft()
			fmt.Printf("Saved credentials for %s (%s, expires in %.1fh)\n",
				seq.ActiveAccountID, activeAcct.Email, hoursLeft)
			return nil
		},
	}
}
