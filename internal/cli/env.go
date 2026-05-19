package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

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
				if err := secureCredsFile(credsFile); err != nil {
					fmt.Fprintf(os.Stderr, "echo 'Error: %v'\n", err)
					return nil
				}
				dir := configDir
				if dir == "" {
					dir = filepath.Join(home, ".claude-env-file")
				}
				if err := secureDir(dir); err != nil {
					return err
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
				dir = envDirPath(id)
			}
			if err := secureDir(dir); err != nil {
				return err
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
			ctx := cmd.Context()
			activeID := seq.ActiveAccountID
			var credsData []byte
			if id == activeID {
				credsData, err = b.Read(ctx, account.ActiveCredKey)
			} else {
				credsData, err = b.Read(ctx, account.BackupCredKey(id, acct.Email))
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

// secureDir verifies that an existing path is safe to use as an isolated
// CLAUDE_CONFIG_DIR: a real directory (not a symlink), owned by the current
// user, with no group/other permission bits. A non-existent path is fine —
// the caller creates it 0700. claude reads hooks and runs scripts out of this
// directory, so an attacker-controlled one would mean code execution with the
// user's credentials.
func secureDir(dir string) error {
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing config dir %s: it is a symlink", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("refusing config dir %s: not a directory", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("refusing config dir %s: group/world-accessible (mode %o) — chmod 700 it",
			dir, fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("refusing config dir %s: owned by uid %d, not the current user (uid %d)",
			dir, st.Uid, os.Getuid())
	}
	return nil
}

// secureCredsFile verifies that a user-supplied --creds-file resolves to a
// regular file (not a directory, device, or socket). Ownership is not
// checked — --creds-file exists to point at mounted secrets, which
// legitimately belong to another uid.
func secureCredsFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("credentials file %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("credentials file %s is not a regular file", path)
	}
	return nil
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
