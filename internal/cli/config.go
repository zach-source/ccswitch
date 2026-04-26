package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/backend"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newConfigCmd)
}

func newConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print effective configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}

			resolved := resolvedBackendName(cfg)

			fmt.Println("ccswitch configuration:")
			fmt.Println()
			fmt.Printf("  Config file:    %s\n", cfg.ConfigFile)
			if _, err := os.Stat(cfg.ConfigFile); err == nil {
				fmt.Println("  Config exists:  yes")
			} else {
				fmt.Println("  Config exists:  no (using defaults — run 'ccswitch init-config' to create)")
			}
			fmt.Println()
			fmt.Println("[backend]")
			fmt.Printf("  type = %q   (resolved: %s)\n", cfg.Backend, resolved)
			fmt.Println()

			if resolved == string(backend.TypeOnePassword) || cfg.Backend == backend.TypeOnePassword {
				fmt.Println("[backend.onepassword]")
				fmt.Printf("  vault       = %q\n", cfg.OnePassword.Vault)
				fmt.Printf("  item_prefix = %q\n", cfg.OnePassword.ItemPrefix)
				if cfg.OnePassword.Account != "" {
					fmt.Printf("  account     = %q\n", cfg.OnePassword.Account)
				}
				if cfg.OnePassword.ConnectHost != "" {
					fmt.Println(`  mode        = Connect (HTTP)`)
					fmt.Printf("  connect_host = %q\n", cfg.OnePassword.ConnectHost)
				} else {
					fmt.Println("  mode        = Signed-in CLI")
				}
				fmt.Println()
			}

			if resolved == string(backend.TypeVault) || cfg.Backend == backend.TypeVault {
				fmt.Println("[backend.vault]")
				addr := cfg.Vault.Addr
				if addr == "" {
					addr = "<not set>"
				}
				fmt.Printf("  addr  = %q\n", addr)
				fmt.Printf("  path  = %q\n", cfg.Vault.Path)
				if cfg.Vault.Token != "" {
					fmt.Println("  token = <set>")
				} else {
					fmt.Println("  token = <not set>")
				}
				fmt.Println()
			}

			fmt.Println("[sync]")
			fmt.Printf("  interval = %.0f  # seconds\n", cfg.Sync.Interval.Seconds())
			fmt.Println()
			fmt.Println("[refresh]")
			fmt.Printf("  expiry_buffer_minutes = %.0f\n", cfg.Refresh.ExpiryBuffer.Minutes())
			return nil
		},
	}
}
