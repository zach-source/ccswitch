package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zach-source/ccswitch/internal/account"
	"github.com/zach-source/ccswitch/internal/config"
)

func init() {
	subcommandBuilders = append(subcommandBuilders, newRemoveAccountCmd)
}

func newRemoveAccountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove-account <hash|email|index>",
		Short: "Remove a managed account by hash, email, or 1-based index",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return err
			}

			seq, err := account.LoadSequence(sequencePath())
			if err != nil {
				return err
			}
			if len(seq.Sequence) == 0 {
				return fmt.Errorf("no accounts are managed yet")
			}

			id := seq.Resolve(args[0])
			if id == "" {
				return fmt.Errorf("no account found matching: %s", args[0])
			}
			acct := seq.Accounts[id]

			if seq.ActiveAccountID == id {
				fmt.Fprintf(os.Stderr, "Warning: Account %s (%s) is currently active\n", id, acct.Email)
			}

			fmt.Printf("Are you sure you want to permanently remove %s (%s)? [y/N] ", id, acct.Email)
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Scan()
			ans := strings.TrimSpace(scanner.Text())
			if ans != "y" && ans != "Y" {
				fmt.Println("Cancelled")
				return nil
			}

			// Delete credentials from the configured backend.
			b, backendErr := resolveBackend(cfg)
			if backendErr == nil {
				key := account.BackupCredKey(id, acct.Email)
				if delErr := b.Delete(cmd.Context(), key); delErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not delete backend credentials: %v\n", delErr)
				}
			} else {
				fmt.Fprintf(os.Stderr, "Warning: backend not available (%v); skipping credential deletion\n", backendErr)
			}

			// Remove the isolated env directory, if any — it can hold a
			// cached plaintext .credentials.json for this account.
			if envDir := envDirPath(id); envDir != "" {
				if rmErr := os.RemoveAll(envDir); rmErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not remove %s: %v\n", envDir, rmErr)
				}
			}

			seq.Remove(id)
			if err := seq.Save(sequencePath()); err != nil {
				return fmt.Errorf("save sequence: %w", err)
			}

			fmt.Printf("Removed %s (%s)\n", id, acct.Email)
			return nil
		},
	}
}
