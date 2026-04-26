package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/config"
	"github.com/zach-source/ccswitch/internal/credentials"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newPushCmd)
}

func newPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Push local credentials + sequence.json to 1Password (or active backend)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}

			b, err := resolveBackend(cfg)
			if err != nil {
				return fmt.Errorf("backend not available: %w", err)
			}

			if err := b.HealthCheck(context.Background()); err != nil {
				return fmt.Errorf("backend health check failed: %w", err)
			}

			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			if len(seq.Sequence) == 0 {
				return fmt.Errorf("no local sequence file")
			}

			fmt.Println("Pushing credentials to 1Password...")
			if cfg.Backend == backend.TypeOnePassword || cfg.Backend == backend.TypeAuto {
				fmt.Printf("  Vault: %s\n", cfg.OnePassword.Vault)
			}
			fmt.Println()

			ctx := context.Background()
			pushed, skipped := 0, 0

			for _, id := range seq.Sequence {
				acct := seq.Accounts[id]
				var credsData []byte
				if id == seq.ActiveAccountID {
					credsData, err = b.Read(ctx, "Claude Code-credentials")
				} else {
					key := fmt.Sprintf("Claude Code Account - %s-%s", id, acct.Email)
					credsData, err = b.Read(ctx, key)
				}
				if err != nil {
					fmt.Printf("  %s %s: no local credentials, skipping\n", id, acct.Email)
					skipped++
					continue
				}

				key := fmt.Sprintf("Claude Code Account - %s-%s", id, acct.Email)
				if err := b.Write(ctx, key, credsData); err != nil {
					fmt.Printf("  %s %s: push failed\n", id, acct.Email)
					continue
				}

				creds, _ := credentials.Parse(credsData)
				hoursLeft := 0.0
				if creds != nil {
					hoursLeft = creds.HoursLeft()
				}
				fmt.Printf("  %s %s: pushed (expires in %.1fh)\n", id, acct.Email, hoursLeft)
				pushed++
			}

			// Push sequence metadata (without switchLog).
			seqCopy := *seq
			seqCopy.SwitchLog = nil
			seqData, err := json.MarshalIndent(seqCopy, "", "  ")
			if err == nil {
				fmt.Println()
				fmt.Print("Pushing sequence metadata... ")
				seqKey := fmt.Sprintf("%s - _sequence", cfg.OnePassword.ItemPrefix)
				if err := b.Write(ctx, seqKey, seqData); err != nil {
					fmt.Println("✗")
				} else {
					fmt.Println("✓")
				}
			}

			fmt.Println()
			fmt.Printf("Summary: %d pushed, %d skipped\n", pushed, skipped)
			return nil
		},
	}
}
