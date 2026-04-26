package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newInitConfigCmd)
}

const configTemplate = `# ccswitch configuration
# Documentation: https://github.com/zach-source/ccswitch
#
# Precedence (highest wins): env vars > this file > built-in defaults

[backend]
# Where ccswitch stores credentials.
# Options: "auto" (keychain on macOS, file on Linux), "keychain", "file",
#          "1password", "vault"
type = "auto"

# ─── 1Password backend ─────────────────────────────────────────────────────
# Two modes:
#   A. Signed-in mode — ` + "`op`" + ` CLI signed into an account (desktop app or CLI).
#      Requires biometric unlock on some operations.
#   B. Connect mode — routes through a 1Password Connect server. No biometric.
#      Set connect_host below. Token is read from macOS Keychain (or ~/.config/
#      ccswitch/connect-token on Linux). Run ` + "`ccswitch setup-op-connect`" + `.
[backend.onepassword]
vault = "Private"
item_prefix = "Claude Code Account"
# account = ""   # optional: op --account shorthand (signed-in mode only)

# Connect mode (takes precedence when set; ` + "`account`" + ` is ignored):
# connect_host = "https://op-connect.example.com"
# connect_token_keychain_service = "ccswitch-op-connect-token"
# connect_token_keychain_account = "ccswitch"

# ─── HashiCorp Vault / OpenBao backend ─────────────────────────────────────
# Requires: ` + "`vault`" + ` or ` + "`bao`" + ` CLI on PATH.
# Auth: token is read from config, CCSWITCH_VAULT_TOKEN env, or VAULT_TOKEN.
[backend.vault]
# addr = "https://vault.example.com"
path = "secret/data/ccswitch"
# token = ""

# ─── Sync (1Password daemon) ───────────────────────────────────────────────
[sync]
# How often the sync daemon reconciles local with 1Password (in seconds)
interval = 300

# ─── Refresh behavior ──────────────────────────────────────────────────────
[refresh]
# A token is considered "expired" this many minutes before actual expiry
expiry_buffer_minutes = 5
`

func newInitConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init-config",
		Short: "Write a commented TOML template to ~/.config/ccswitch/config.toml",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.DefaultPath()
			if _, err := os.Stat(path); err == nil {
				fmt.Printf("Config already exists at: %s\n", path)
				fmt.Println("Remove or move it first to regenerate.")
				return nil
			}

			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return fmt.Errorf("create config dir: %w", err)
			}
			if err := os.WriteFile(path, []byte(configTemplate), 0o600); err != nil {
				return fmt.Errorf("write config: %w", err)
			}

			fmt.Printf("Created config template: %s\n", path)
			fmt.Println()
			fmt.Println("Next steps:")
			fmt.Println("  1. Edit the file and set [backend].type to your preferred backend")
			fmt.Println("  2. Run 'ccswitch config' to verify")
			fmt.Println("  3. For 1Password: run 'ccswitch push' to seed the vault")
			return nil
		},
	}
}
