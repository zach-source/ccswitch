package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
			org := claudeJSON.OAuthAccount.OrganizationName
			if strings.HasSuffix(org, "'s Organization") {
				org = "Personal"
			}
			if org == "" {
				org = "Personal"
			}
			fmt.Printf("%s (%s)\n", email, org)
			return nil
		},
	}
}
