package cli

import (
	"fmt"

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

			// Resolve the live active account from .claude.json, not the
			// (possibly stale) recorded activeAccountId — saving the active
			// slot under the wrong account's backup key would misfile it.
			id := activeID(seq)
			if id == "" {
				return fmt.Errorf("no active Claude account found")
			}
			activeAcct, ok := seq.Accounts[id]
			if !ok {
				return fmt.Errorf("active account %s is not managed; run `ccswitch add-account` first", id)
			}

			// Read active credentials from the configured backend.
			b, err := resolveBackend(cfg)
			if err != nil {
				return fmt.Errorf("backend not available: %w", err)
			}

			ctx := cmd.Context()
			credsData, err := b.Read(ctx, account.ActiveCredKey)
			if err != nil {
				return fmt.Errorf("read active credentials: %w", err)
			}

			creds, err := credentials.Parse(credsData)
			if err != nil {
				return fmt.Errorf("parse credentials: %w", err)
			}

			backupKey := account.BackupCredKey(id, activeAcct.Email)
			if err := b.Write(ctx, backupKey, credsData); err != nil {
				return fmt.Errorf("write backup credentials: %w", err)
			}

			hoursLeft := creds.HoursLeft()
			fmt.Printf("Saved credentials for %s (%s, expires in %.1fh)\n",
				id, activeAcct.Email, hoursLeft)
			return nil
		},
	}
}
