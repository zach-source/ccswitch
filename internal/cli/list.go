package cli

import (
	"fmt"

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
			active := activeID(seq)

			fmt.Println("Accounts:")
			for _, id := range seq.Sequence {
				acct := seq.Accounts[id]
				org := displayOrg(acct.OrgName)
				if id == active {
					fmt.Printf("  %s  %s  [%s] (active)\n", id, acct.Email, org)
				} else {
					fmt.Printf("  %s  %s  [%s]\n", id, acct.Email, org)
				}
			}
			return nil
		},
	}
}

// currentEmail returns the live Claude Code account email, or "" if there is
// no usable account. Thin wrapper over readClaudeIdentity.
func currentEmail() string {
	return readClaudeIdentity().Email
}
