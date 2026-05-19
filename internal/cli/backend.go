package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newBackendCmd)
}

func newBackendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backend",
		Short: "Show the resolved backend name",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}
			fmt.Println(resolvedBackendName(cfg))
			return nil
		},
	}
}
