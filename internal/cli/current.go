package cli

import (
	"encoding/json"
	"fmt"
	"os"

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
			configPath := claudeConfigPath()
			data, err := os.ReadFile(configPath)
			if err != nil {
				fmt.Println("No active Claude account")
				return nil
			}
			var claudeJSON struct {
				OAuthAccount struct {
					EmailAddress     string `json:"emailAddress"`
					OrganizationName string `json:"organizationName"`
				} `json:"oauthAccount"`
			}
			if err := json.Unmarshal(data, &claudeJSON); err != nil || claudeJSON.OAuthAccount.EmailAddress == "" {
				fmt.Println("No active Claude account")
				return nil
			}
			email := claudeJSON.OAuthAccount.EmailAddress
			fmt.Printf("%s (%s)\n", email, displayOrg(claudeJSON.OAuthAccount.OrganizationName))
			return nil
		},
	}
}
