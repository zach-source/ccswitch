package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newListCmd)
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Tabular list of all managed accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			if len(seq.Sequence) == 0 {
				fmt.Println("No accounts are managed yet.")
				return nil
			}

			// Determine who is actually active right now from .claude.json.
			activeEmail := currentEmail()
			activeID := ""
			if activeEmail != "" {
				activeID = account.HashEmail(activeEmail)
			}

			fmt.Println("Accounts:")
			for _, id := range seq.Sequence {
				acct := seq.Accounts[id]
				org := acct.OrgName
				if strings.HasSuffix(org, "'s Organization") {
					org = "Personal"
				}
				if org == "" {
					org = "Unknown"
				}
				if id == activeID {
					fmt.Printf("  %s  %s  [%s] (active)\n", id, acct.Email, org)
				} else {
					fmt.Printf("  %s  %s  [%s]\n", id, acct.Email, org)
				}
			}
			return nil
		},
	}
}

// currentEmail reads the email from ~/.claude/.claude.json. Returns "" on any error.
func currentEmail() string {
	data, err := os.ReadFile(claudeConfigPath())
	if err != nil {
		return ""
	}
	var j struct {
		OAuthAccount struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &j); err != nil {
		return ""
	}
	return j.OAuthAccount.EmailAddress
}
