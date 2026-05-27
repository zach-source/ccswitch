package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newAddAccountCmd)
}

func newAddAccountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add-account",
		Short: "Add the currently-active Claude account to the ccswitch managed set",
		Long: "Reads ~/.claude/.claude.json + .credentials.json, derives a stable 8-char " +
			"ID from the email address, and writes/updates sequence.json.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			_ = cfg // backend used only when creds are written via write_account_credentials (future)

			// Ensure backup dir exists.
			if err := os.MkdirAll(backupDir(), 0o700); err != nil {
				return fmt.Errorf("create backup dir: %w", err)
			}

			// Read the live Claude identity from .claude.json.
			identity := readClaudeIdentity()
			if identity.Email == "" {
				return fmt.Errorf("no active Claude account found — please log in to Claude first")
			}

			// Load or create sequence.
			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}

			id := account.HashEmail(identity.Email)
			if _, exists := seq.Accounts[id]; exists {
				fmt.Printf("Account %s is already managed.\n", identity.Email)
				return nil
			}

			acct := account.Account{
				Email:       identity.Email,
				AccountUUID: identity.UUID,
				OrgName:     identity.Org,
				AddedAt:     time.Now().UTC().Format(time.RFC3339),
			}
			// Capture the full oauthAccount block so a future `switch-to`
			// can restore this account's identity into ~/.claude.json
			// alongside its credentials. Without this, switching only swaps
			// the OAuth token; the identity Claude Code displays stays on
			// whichever account was active when the file was last written.
			if block, err := readClaudeOAuthBlock(); err == nil && len(block) > 0 {
				acct.OAuthAccount = block
			}
			seq.Add(id, acct)

			if err := seq.Save(sequencePath()); err != nil {
				return fmt.Errorf("save sequence: %w", err)
			}

			fmt.Printf("Added account %s: %s\n", id, identity.Email)
			return nil
		},
	}
}
