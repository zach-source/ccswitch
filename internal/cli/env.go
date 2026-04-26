package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newEnvCmd)
}

func newEnvCmd() *cobra.Command {
	var unset bool
	var credsFile string
	var configDir string

	cmd := &cobra.Command{
		Use:   "env [<hash|email|index>]",
		Short: "Emit shell export lines to bind a shell to a specific account",
		Long: "Output eval-able shell exports. Use with: eval \"$(ccswitch env <id>)\"\n" +
			"  --unset:       unset CLAUDE_CONFIG_DIR (revert to global)\n" +
			"  --creds-file:  use an arbitrary credentials file (e.g. mounted secret)\n" +
			"  --config-dir:  override the isolated config directory path",
		RunE: func(cmd *cobra.Command, args []string) error {
			if unset {
				fmt.Println("unset CLAUDE_CONFIG_DIR")
				fmt.Fprintln(os.Stderr, "[ccswitch] Reverted to global account")
				return nil
			}

			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}

			home, _ := os.UserHomeDir()
			sharedDir := filepath.Join(home, ".claude")

			// Mode 1: credentials file supplied directly.
			if credsFile != "" {
				if _, err := os.Stat(credsFile); err != nil {
					fmt.Fprintf(os.Stderr, "echo 'Error: Credentials file not found: %s'\n", credsFile)
					return nil
				}
				dir := configDir
				if dir == "" {
					dir = filepath.Join(home, ".claude-env-file")
				}
				if err := setupIsolatedDir(dir, sharedDir); err != nil {
					return err
				}
				// Symlink credentials file.
				dest := filepath.Join(dir, ".credentials.json")
				_ = os.Remove(dest)
				abs, _ := filepath.Abs(credsFile)
				if err := os.Symlink(abs, dest); err != nil {
					return fmt.Errorf("symlink credentials: %w", err)
				}
				fmt.Printf("export CLAUDE_CONFIG_DIR=%q\n", dir)
				fmt.Fprintf(os.Stderr, "[ccswitch] Shell bound to credentials file: %s (CLAUDE_CONFIG_DIR=%s)\n", credsFile, dir)
				return nil
			}

			// Mode 2: managed account identifier.
			if len(args) == 0 {
				fmt.Fprintln(os.Stderr, "Usage: eval \"$(ccswitch env <hash|email|index>)\"")
				fmt.Fprintln(os.Stderr, "       eval \"$(ccswitch env --creds-file /path/to/creds.json)\"")
				fmt.Fprintln(os.Stderr, "Unset: eval \"$(ccswitch env --unset)\"")
				return nil
			}

			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			id := seq.Resolve(args[0])
			if id == "" {
				fmt.Fprintf(os.Stderr, "echo 'Error: Account not found: %s'\n", args[0])
				return nil
			}
			acct := seq.Accounts[id]

			dir := configDir
			if dir == "" {
				dir = filepath.Join(home, fmt.Sprintf(".claude-env-%s", id))
			}
			if err := setupIsolatedDir(dir, sharedDir); err != nil {
				return err
			}

			// Read credentials and write them into the isolated dir.
			b, err := resolveBackend(cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "echo 'Error: backend not available: %v'\n", err)
				return nil
			}
			ctx := context.Background()
			activeID := seq.ActiveAccountID
			var credsData []byte
			if id == activeID {
				credsData, err = b.Read(ctx, "Claude Code-credentials")
			} else {
				key := fmt.Sprintf("Claude Code Account - %s-%s", id, acct.Email)
				credsData, err = b.Read(ctx, key)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "echo 'Error: No credentials found for %s (%s)'\n", id, acct.Email)
				return nil
			}

			credsDest := filepath.Join(dir, ".credentials.json")
			if err := os.WriteFile(credsDest, credsData, 0o600); err != nil {
				return fmt.Errorf("write credentials: %w", err)
			}

			fmt.Printf("export CLAUDE_CONFIG_DIR=%q\n", dir)
			fmt.Fprintf(os.Stderr, "[ccswitch] Shell bound to %s %s (CLAUDE_CONFIG_DIR=%s)\n", id, acct.Email, dir)
			return nil
		},
	}

	cmd.Flags().BoolVar(&unset, "unset", false, "Unset CLAUDE_CONFIG_DIR (revert to global account)")
	cmd.Flags().StringVar(&credsFile, "creds-file", "", "Path to a credentials file to use directly")
	cmd.Flags().StringVar(&configDir, "config-dir", "", "Override the isolated CLAUDE_CONFIG_DIR path")
	return cmd
}

// setupIsolatedDir creates dir and symlinks well-known shared resources from sharedDir.
func setupIsolatedDir(dir, sharedDir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o700); err != nil {
		return err
	}
	shared := []string{"settings.json", "CLAUDE.md", "mcp_servers.json", "hooks", "skills", "agents", "plugins", "commands", "scripts"}
	for _, item := range shared {
		src := filepath.Join(sharedDir, item)
		dst := filepath.Join(dir, item)
		if _, err := os.Lstat(src); err != nil {
			continue // source doesn't exist; skip
		}
		if _, err := os.Lstat(dst); err == nil {
			continue // dest already exists; leave it
		}
		_ = os.Symlink(src, dst)
	}
	return nil
}
