package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newCurrentCmd)
}

func newCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print the active account's email and org",
		RunE: func(cmd *cobra.Command, args []string) error {
			id := readClaudeIdentity()
			if id.Email == "" {
				fmt.Println("No active Claude account")
				return nil
			}
			fmt.Printf("%s (%s)\n", id.Email, displayOrg(id.Org))
			return nil
		},
	}
}
