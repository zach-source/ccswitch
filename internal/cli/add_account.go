package cli

import (
	"encoding/json"
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

			// Read active Claude identity from .claude.json.
			configPath := claudeConfigPath()
			data, err := os.ReadFile(configPath)
			if err != nil {
				return fmt.Errorf("cannot read %s: %w — please log in to Claude first", configPath, err)
			}
			var claudeJSON struct {
				OAuthAccount struct {
					EmailAddress     string `json:"emailAddress"`
					AccountUUID      string `json:"accountUuid"`
					OrganizationName string `json:"organizationName"`
				} `json:"oauthAccount"`
			}
			if err := json.Unmarshal(data, &claudeJSON); err != nil {
				return fmt.Errorf("parse %s: %w", configPath, err)
			}
			email := claudeJSON.OAuthAccount.EmailAddress
			if email == "" {
				return fmt.Errorf("no active Claude account found — please log in first")
			}

			// Load or create sequence.
			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}

			id := account.HashEmail(email)
			if _, exists := seq.Accounts[id]; exists {
				fmt.Printf("Account %s is already managed.\n", email)
				return nil
			}

			acct := account.Account{
				Email:       email,
				AccountUUID: claudeJSON.OAuthAccount.AccountUUID,
				OrgName:     claudeJSON.OAuthAccount.OrganizationName,
				AddedAt:     time.Now().UTC().Format(time.RFC3339),
			}
			seq.Add(id, acct)

			if err := seq.Save(sequencePath()); err != nil {
				return fmt.Errorf("save sequence: %w", err)
			}

			fmt.Printf("Added account %s: %s\n", id, email)
			return nil
		},
	}
}
