package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/config"
	"github.com/zach-source/ccswitch/internal/credentials"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newPullCmd)
}

func newPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Pull credentials + sequence.json from 1Password to local",
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

			fmt.Println("Pulling credentials from 1Password...")
			if cfg.Backend == backend.TypeOnePassword || cfg.Backend == backend.TypeAuto {
				fmt.Printf("  Vault: %s\n", cfg.OnePassword.Vault)
			}
			fmt.Println()

			ctx := context.Background()

			// Pull sequence.json first.
			seqKey := fmt.Sprintf("%s - _sequence", cfg.OnePassword.ItemPrefix)
			seqData, err := b.Read(ctx, seqKey)
			if err != nil {
				return fmt.Errorf("no sequence item found in backend — run 'ccswitch push' first from a machine with credentials")
			}

			// Backup existing sequence.
			sp := sequencePath()
			if _, err := os.Stat(sp); err == nil {
				_ = os.Rename(sp, sp+".pre-pull")
			} else {
				if mkErr := os.MkdirAll(backupDir(), 0o700); mkErr != nil {
					return mkErr
				}
			}

			var remoteSeq account.Sequence
			if err := json.Unmarshal(seqData, &remoteSeq); err != nil {
				return fmt.Errorf("parse remote sequence: %w", err)
			}
			if remoteSeq.Accounts == nil {
				remoteSeq.Accounts = map[string]account.Account{}
			}

			// Preserve local switchLog.
			localSeq, _ := account.LoadSequence(sp + ".pre-pull")
			remoteSeq.SwitchLog = localSeq.SwitchLog

			if err := remoteSeq.Save(sp); err != nil {
				return fmt.Errorf("save sequence: %w", err)
			}
			fmt.Println("Pulled sequence metadata ✓")
			fmt.Println()

			pulled, skipped := 0, 0
			for _, id := range remoteSeq.Sequence {
				acct := remoteSeq.Accounts[id]
				key := fmt.Sprintf("Claude Code Account - %s-%s", id, acct.Email)
				credsData, err := b.Read(ctx, key)
				if err != nil {
					fmt.Printf("  %s %s: not in backend, skipping\n", id, acct.Email)
					skipped++
					continue
				}

				if id == remoteSeq.ActiveAccountID {
					if werr := b.Write(ctx, "Claude Code-credentials", credsData); werr != nil {
						fmt.Fprintf(os.Stderr, "  %s %s: warn: write active slot: %v\n", id, acct.Email, werr)
					}
				}
				if werr := b.Write(ctx, key, credsData); werr != nil {
					fmt.Printf("  %s %s: write failed\n", id, acct.Email)
					continue
				}

				creds, _ := credentials.Parse(credsData)
				hoursLeft := 0.0
				if creds != nil {
					hoursLeft = creds.HoursLeft()
				}
				fmt.Printf("  %s %s: pulled (expires in %.1fh)\n", id, acct.Email, hoursLeft)
				pulled++
			}

			fmt.Println()
			fmt.Printf("Summary: %d pulled, %d skipped\n", pulled, skipped)
			return nil
		},
	}
}
